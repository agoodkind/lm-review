// Package daemon implements the lm-review gRPC daemon.
// It serializes requests to LM Studio, maintains the audit trail,
// and is the single point of contact for both the CLI and the MCP server.
package daemon

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"net"
	"os"
	"time"

	"google.golang.org/grpc"

	"goodkind.io/lmctl"

	"goodkind.io/lm-review/api/reviewpb"
	"goodkind.io/lm-review/internal/audit"
	"goodkind.io/lm-review/internal/claude"
	"goodkind.io/lm-review/internal/config"
	"goodkind.io/lm-review/internal/lmstudio"
	"goodkind.io/lm-review/internal/review"
	"goodkind.io/lm-review/internal/xdg"
)

// Server implements the LMReviewD gRPC service.
type Server struct {
	reviewpb.UnimplementedLMReviewDServer
	log *audit.Logger
	cfg *config.Config
}

// Run starts the daemon on the XDG runtime Unix socket.
func Run() error {
	if err := os.MkdirAll(xdg.RuntimeDir(), 0o700); err != nil {
		return fmt.Errorf("create runtime dir: %w", err)
	}

	socketPath := xdg.DaemonSocketPath()
	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale socket: %w", err)
	}

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	log, err := audit.New()
	if err != nil {
		return fmt.Errorf("open audit log: %w", err)
	}
	defer log.Close()

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", socketPath, err)
	}

	srv := &Server{log: log, cfg: cfg}
	grpcServer := grpc.NewServer()
	reviewpb.RegisterLMReviewDServer(grpcServer, srv)

	fmt.Fprintf(os.Stderr, "lm-review daemon listening on %s\n", socketPath)
	return grpcServer.Serve(listener)
}

// ReviewDiff reviews a staged diff.
func (s *Server) ReviewDiff(ctx context.Context, req *reviewpb.ReviewRequest) (*reviewpb.ReviewResponse, error) {
	return s.runReview(ctx, "diff", req)
}

// ReviewPR reviews the diff against main.
func (s *Server) ReviewPR(ctx context.Context, req *reviewpb.ReviewRequest) (*reviewpb.ReviewResponse, error) {
	return s.runReview(ctx, "pr", req)
}

// ReviewRepo reviews the full repository.
func (s *Server) ReviewRepo(ctx context.Context, req *reviewpb.ReviewRequest) (*reviewpb.ReviewResponse, error) {
	return s.runReview(ctx, "repo", req)
}

// buildClient constructs the appropriate ChatClient and resolves the model name.
// If model_priority is configured and a higher-ranked model is already loaded,
// it will be used instead of swapping to the tier's default model.
func (s *Server) buildClient(ctx context.Context, scope string, depth string, modelOverride string) (review.ChatClient, string) {
	if s.cfg.ResolveProvider() == "claude" {
		model := s.cfg.Claude.Model
		if modelOverride != "" {
			model = modelOverride
		}
		return claude.New(model), model
	}

	model := modelOverride
	if model == "" {
		model = s.cfg.LMStudio.ResolveModel(scope, depth)
	}

	// Check what's already loaded for substitution and eviction decisions.
	loaded, _ := lmctl.ListLoaded(ctx)
	loadedNames := make([]string, len(loaded))
	for i, m := range loaded {
		loadedNames[i] = m.ModelKey
	}

	// If a higher-priority model is already warm, use it instead.
	if modelOverride == "" && len(s.cfg.LMStudio.ModelPriority) > 0 && len(loaded) > 0 {
		if sub := s.cfg.LMStudio.PreferLoaded(model, loadedNames); sub != model {
			slog.Info("using warm higher-priority model",
				"requested", model, "using", sub, "depth", depth)
			model = sub
		}
	}

	// If eviction is disabled, only use what's already loaded.
	if !s.cfg.LMStudio.CanEvict() {
		for _, name := range loadedNames {
			if lmctl.BaseModelName(name) == lmctl.BaseModelName(model) {
				return lmstudio.New(s.cfg.LMStudio.URL, s.cfg.LMStudio.Token, model, s.cfg.LMStudio.ResolveMaxResponseTokens()), model
			}
		}
		// Requested model isn't loaded. Return nil client; caller handles the skip.
		slog.Warn("model not loaded and eviction disabled, skipping review",
			"model", model, "depth", depth)
		return nil, model
	}

	if err := lmctl.EnsureLoaded(ctx, model,
		lmctl.WithContextLength(s.cfg.LMStudio.ResolveContextLength()),
		lmctl.WithMaxMemoryBytes(s.cfg.LMStudio.ResolveMaxMemoryBytes()),
		lmctl.WithWarmup(s.cfg.LMStudio.URL, s.cfg.LMStudio.Token),
	); err != nil {
		s.log.Write(audit.Entry{Scope: scope, Error: fmt.Sprintf("model load: %v", err)})
	}
	return lmstudio.New(s.cfg.LMStudio.URL, s.cfg.LMStudio.Token, model, s.cfg.LMStudio.ResolveMaxResponseTokens()), model
}

func (s *Server) runReview(ctx context.Context, scope string, req *reviewpb.ReviewRequest) (*reviewpb.ReviewResponse, error) {
	start := time.Now()

	depth := req.Depth
	if depth == "" {
		depth = "normal"
	}

	client, model := s.buildClient(ctx, scope, depth, req.Model)
	if client == nil {
		return &reviewpb.ReviewResponse{
			Verdict:   "skip",
			Summary:   fmt.Sprintf("Skipped: model %s not loaded and eviction disabled.", model),
			Model:     model,
			LatencyMs: time.Since(start).Milliseconds(),
		}, nil
	}

	// Merge project-local rules from <path>/.lm-review.toml if present.
	cfg := s.cfg
	if req.Path != "" {
		var mergeErr error
		cfg, mergeErr = config.MergeProjectRules(s.cfg, req.Path)
		if mergeErr != nil {
			s.log.Write(audit.Entry{Scope: scope, Error: mergeErr.Error()})
		}
	}

	// Extract texts and globs from config rules, then filter to files in this diff.
	texts := make([]string, len(cfg.Rules))
	filters := make([]review.RuleFilter, len(cfg.Rules))
	for i, r := range cfg.Rules {
		texts[i] = r.Text
		filters[i] = review.RuleFilter{Globs: r.Globs, Always: r.Always}
	}
	files := review.FilesFromDiff(req.Diff)
	rules := review.FilterRules(texts, filters, files)

	// Pick prompt builder based on depth tier.
	var buildPrompt review.PromptBuilder
	switch depth {
	case "quick":
		buildPrompt = review.BuildQuickSystemPrompt
	case "deep", "ultra":
		buildPrompt = review.BuildDeepSystemPrompt
	default:
		buildPrompt = review.BuildSystemPrompt
	}

	var (
		result *review.Result
		err    error
	)
	repoMaxBytes := s.cfg.LMStudio.ResolveRepoMaxBytes()
	if scope == "repo" && len(req.Diff) > repoMaxBytes {
		result, err = review.ChunkedRepoReview(ctx, client, req.Diff, scope, rules, repoMaxBytes, s.cfg.LMStudio.ResolveChunkParallelism())
	} else {
		r := review.NewWithPromptBuilder(client, scope, rules, buildPrompt)
		result, err = r.ReviewDiff(ctx, req.Diff)
	}

	// Ultra: verify sweep results with the ultra model to filter false positives.
	if err == nil && depth == "ultra" && result != nil && len(result.Issues) > 0 {
		verifyClient, verifyModel := s.buildClient(ctx, scope, "ultra", "")
		verified, verifyErr := review.VerifyIssues(ctx, verifyClient, result.Issues, req.Diff)
		if verifyErr == nil {
			beforeCount := len(result.Issues)
			result.Issues = verified
			model = verifyModel // report the verify model
			s.log.Write(audit.Entry{
				Scope: scope,
				Model: verifyModel,
				Error: fmt.Sprintf("ultra verify: %d→%d issues", beforeCount, len(verified)),
			})
		}
	}

	latency := time.Since(start).Milliseconds()

	entry := audit.Entry{
		Scope:     scope,
		Model:     model,
		DiffHash:  diffHash(req.Diff),
		LatencyMS: latency,
	}

	if err != nil {
		entry.Error = err.Error()
		s.log.Write(entry)
		return nil, fmt.Errorf("review failed: %w", err)
	}

	entry.Verdict = string(result.Verdict)
	entry.IssueCount = len(result.Issues)
	s.log.Write(entry)

	resp := &reviewpb.ReviewResponse{
		Verdict:   string(result.Verdict),
		Summary:   result.Summary,
		Model:     model,
		LatencyMs: latency,
	}

	for _, issue := range result.Issues {
		resp.Issues = append(resp.Issues, &reviewpb.Issue{
			Severity:   issue.Severity,
			File:       issue.File,
			Line:       int32(issue.Line),
			EndLine:    int32(issue.EndLine),
			Rule:       issue.Rule,
			Message:    issue.Message,
			Category:   string(issue.Category),
			Suggestion: issue.Suggestion,
			Confidence: string(issue.Confidence),
		})
	}

	return resp, nil
}

func diffHash(diff string) string {
	if diff == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(diff))
	return fmt.Sprintf("%x", sum[:8])
}
