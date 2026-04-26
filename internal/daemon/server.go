// Package daemon implements the lm-review gRPC daemon.
// It serializes requests to LM Studio, maintains the audit trail,
// and is the single point of contact for both the CLI and the MCP server.
package daemon

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc"

	"goodkind.io/gklog"
	"goodkind.io/lm-review/api/reviewpb"
	"goodkind.io/lm-review/internal/analyzer"
	"goodkind.io/lm-review/internal/audit"
	"goodkind.io/lm-review/internal/claude"
	"goodkind.io/lm-review/internal/config"
	"goodkind.io/lm-review/internal/gitutil"
	"goodkind.io/lm-review/internal/lmstudio"
	"goodkind.io/lm-review/internal/review"
	"goodkind.io/lm-review/internal/xdg"
	"goodkind.io/lmctl"
)

// Server implements the LMReviewD gRPC service.
type Server struct {
	reviewpb.UnimplementedLMReviewDServer
	log *audit.Logger
	cfg *config.Config
}

// Run starts the daemon on the XDG runtime Unix socket.
func Run() error {
	slog := gklog.LoggerFromContext(context.Background()).With("component", "lm-review", "subcomponent", "daemon")
	slog.Info("daemon.run.begin", "runtime_dir", xdg.RuntimeDir())
	if err := os.MkdirAll(xdg.RuntimeDir(), 0o700); err != nil {
		slog.Error("daemon.run.runtime_dir_failed", "err", err)
		return fmt.Errorf("create runtime dir: %w", err)
	}

	socketPath := xdg.DaemonSocketPath()
	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		slog.Error("daemon.run.remove_stale_socket_failed", "socket_path", socketPath, "err", err)
		return fmt.Errorf("remove stale socket: %w", err)
	}

	cfg, err := config.Load()
	if err != nil {
		slog.Error("daemon.run.load_config_failed", "err", err)
		return fmt.Errorf("load config: %w", err)
	}

	log, err := audit.New()
	if err != nil {
		slog.Error("daemon.run.open_audit_failed", "err", err)
		return fmt.Errorf("open audit log: %w", err)
	}
	defer log.Close()

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		slog.Error("daemon.run.listen_failed", "socket_path", socketPath, "err", err)
		return fmt.Errorf("listen on %s: %w", socketPath, err)
	}

	srv := &Server{log: log, cfg: cfg}
	grpcServer := grpc.NewServer()
	reviewpb.RegisterLMReviewDServer(grpcServer, srv)

	slog.Info("daemon.run.ready", "socket_path", socketPath)
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

// ReviewStatic runs deterministic static analysis with optional LLM synthesis.
func (s *Server) ReviewStatic(ctx context.Context, req *reviewpb.StaticReviewRequest) (*reviewpb.ReviewResponse, error) {
	return s.runStaticReview(ctx, req)
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
		model = s.cfg.OpenAICompat.ResolveModel(scope, depth)
	}

	loaded, _ := lmctl.ListLoaded(ctx)
	loadedNames := make([]string, len(loaded))
	for i, m := range loaded {
		loadedNames[i] = m.ModelKey
	}

	if modelOverride == "" && len(s.cfg.OpenAICompat.ModelPriority) > 0 && len(loaded) > 0 {
		if sub := s.cfg.OpenAICompat.PreferLoaded(model, loadedNames); sub != model {
			log := gklog.LoggerFromContext(ctx).With("component", "lm-review", "subcomponent", "daemon")
			log.InfoContext(ctx, "using warm higher-priority model",
				"requested", model, "using", sub, "depth", depth)
			model = sub
		}
	}

	if !s.cfg.OpenAICompat.CanEvict() {
		for _, name := range loadedNames {
			if lmctl.BaseModelName(name) == lmctl.BaseModelName(model) {
				return lmstudio.New(s.cfg.OpenAICompat.URL, s.cfg.OpenAICompat.Token, model, s.cfg.OpenAICompat.ResolveMaxResponseTokens()), model
			}
		}
		log := gklog.LoggerFromContext(ctx).With("component", "lm-review", "subcomponent", "daemon")
		log.WarnContext(ctx, "model not loaded and eviction disabled, skipping review",
			"model", model, "depth", depth)
		return nil, model
	}

	if err := lmctl.EnsureLoaded(ctx, model,
		lmctl.WithContextLength(s.cfg.OpenAICompat.ResolveContextLength()),
		lmctl.WithMaxMemoryBytes(s.cfg.OpenAICompat.ResolveMaxMemoryBytes()),
		lmctl.WithWarmup(s.cfg.OpenAICompat.URL, s.cfg.OpenAICompat.Token),
	); err != nil {
		s.log.Write(audit.Entry{Scope: scope, Error: fmt.Sprintf("model load: %v", err)})
	}
	return lmstudio.New(s.cfg.OpenAICompat.URL, s.cfg.OpenAICompat.Token, model, s.cfg.OpenAICompat.ResolveMaxResponseTokens()), model
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

	cfg := s.cfg
	if req.Path != "" {
		var mergeErr error
		cfg, mergeErr = config.MergeProjectRules(s.cfg, req.Path)
		if mergeErr != nil {
			s.log.Write(audit.Entry{Scope: scope, Error: mergeErr.Error()})
		}
	}

	texts := make([]string, len(cfg.Rules))
	filters := make([]review.RuleFilter, len(cfg.Rules))
	for i, r := range cfg.Rules {
		texts[i] = r.Text
		filters[i] = review.RuleFilter{Globs: r.Globs, Always: r.Always}
	}
	files := review.FilesFromDiff(req.Diff)
	rules := review.FilterRules(texts, filters, files)

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
	repoMaxBytes := s.cfg.OpenAICompat.ResolveRepoMaxBytes()
	if scope == "repo" && len(req.Diff) > repoMaxBytes {
		result, err = review.ChunkedRepoReview(ctx, client, req.Diff, scope, rules, repoMaxBytes, s.cfg.OpenAICompat.ResolveChunkParallelism())
	} else {
		r := review.NewWithPromptBuilder(client, scope, rules, buildPrompt)
		result, err = r.ReviewDiff(ctx, req.Diff)
	}

	if err == nil && depth == "ultra" && result != nil && len(result.Issues) > 0 {
		verifyClient, verifyModel := s.buildClient(ctx, scope, "ultra", "")
		verified, verifyErr := review.VerifyIssues(ctx, verifyClient, result.Issues, req.Diff)
		if verifyErr == nil {
			beforeCount := len(result.Issues)
			result.Issues = verified
			model = verifyModel
			s.log.Write(audit.Entry{
				Scope: scope,
				Model: verifyModel,
				Error: fmt.Sprintf("ultra verify: %d->%d issues", beforeCount, len(verified)),
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

	return reviewResponseFromResult(result, model, latency), nil
}

func (s *Server) runStaticReview(ctx context.Context, req *reviewpb.StaticReviewRequest) (*reviewpb.ReviewResponse, error) {
	start := time.Now()
	if !s.cfg.StaticReview.IsEnabled() {
		return &reviewpb.ReviewResponse{
			Verdict:   string(review.VerdictSkip),
			Summary:   "Static review is disabled in config.",
			LatencyMs: time.Since(start).Milliseconds(),
		}, nil
	}

	analyzerConfig := analyzer.Config{
		DisabledSources: append([]string{}, s.cfg.StaticReview.DisabledSources...),
		EnabledChecks:   append([]string{}, s.cfg.StaticReview.EnabledChecks...),
	}
	if len(req.DisabledSources) > 0 {
		analyzerConfig.DisabledSources = append([]string{}, req.DisabledSources...)
	}
	if len(req.EnabledChecks) > 0 {
		analyzerConfig.EnabledChecks = append([]string{}, req.EnabledChecks...)
	}

	findings, sourceErrs := analyzer.Run(ctx, analyzerConfig, analyzer.RunOptions{
		RepoRoot:      req.Path,
		Files:         req.Files,
		EnabledChecks: analyzerConfig.EnabledChecks,
	})
	latency := time.Since(start).Milliseconds()

	if !req.Synthesize {
		result := rawStaticResult(findings, sourceErrs)
		s.log.Write(audit.Entry{
			Scope:      "static",
			Model:      "deterministic",
			LatencyMS:  latency,
			Verdict:    string(result.Verdict),
			IssueCount: len(result.Issues),
			Error:      summarizeErrors(sourceErrs),
		})
		return reviewResponseFromResult(result, "deterministic", latency), nil
	}

	depth := req.Depth
	if depth == "" {
		depth = "normal"
	}

	client, model := s.buildClient(ctx, "repo", depth, req.Model)
	if client == nil {
		return &reviewpb.ReviewResponse{
			Verdict:   string(review.VerdictSkip),
			Summary:   fmt.Sprintf("Skipped: model %s not loaded and eviction disabled.", model),
			Model:     model,
			LatencyMs: latency,
		}, nil
	}

	snapshot, err := staticSnapshot(req.Path, req.Files, s.cfg.OpenAICompat.ResolveRepoMaxBytes())
	if err != nil {
		return nil, fmt.Errorf("static snapshot: %w", err)
	}

	cfg := s.cfg
	if req.Path != "" {
		mergedCfg, mergeErr := config.MergeProjectRules(s.cfg, req.Path)
		if mergeErr != nil {
			s.log.Write(audit.Entry{Scope: "static", Error: mergeErr.Error()})
		} else {
			cfg = mergedCfg
		}
	}

	ruleTexts := make([]string, len(cfg.Rules))
	ruleFilters := make([]review.RuleFilter, len(cfg.Rules))
	for i, rule := range cfg.Rules {
		ruleTexts[i] = rule.Text
		ruleFilters[i] = review.RuleFilter{Globs: rule.Globs, Always: rule.Always}
	}
	selectedFiles := req.Files
	if len(selectedFiles) == 0 {
		selectedFiles = snapshotFiles(snapshot)
	}
	rules := review.FilterRules(ruleTexts, ruleFilters, selectedFiles)
	reviewer := review.NewWithPromptBuilder(client, "static", rules, review.BuildStaticSystemPrompt)
	result, err := reviewer.ReviewStatic(ctx, snapshot, analyzer.FormatForPrompt(findings))
	if err != nil {
		s.log.Write(audit.Entry{
			Scope:     "static",
			Model:     model,
			LatencyMS: latency,
			Error:     strings.TrimSpace(strings.TrimSpace(summarizeErrors(sourceErrs)+" ") + err.Error()),
		})
		return nil, fmt.Errorf("static review failed: %w", err)
	}

	s.log.Write(audit.Entry{
		Scope:      "static",
		Model:      model,
		DiffHash:   diffHash(snapshot),
		LatencyMS:  latency,
		Verdict:    string(result.Verdict),
		IssueCount: len(result.Issues),
		Error:      summarizeErrors(sourceErrs),
	})
	return reviewResponseFromResult(result, model, latency), nil
}

func rawStaticResult(findings []analyzer.Finding, errs []error) *review.Result {
	issues := make([]review.Issue, 0, len(findings))
	result := &review.Result{
		Verdict: review.VerdictPass,
		Summary: fmt.Sprintf("Static review produced %d deterministic findings.", len(findings)),
		Scope:   "static",
		Model:   "deterministic",
	}
	for _, finding := range findings {
		issue := review.Issue{
			Severity:   string(finding.Severity),
			Category:   review.CategoryCorrectness,
			File:       finding.File,
			Line:       finding.Line,
			EndLine:    finding.EndLine,
			Rule:       finding.Check,
			Message:    finding.Message,
			Suggestion: finding.Fix,
			Confidence: review.ConfidenceHigh,
		}
		issues = append(issues, issue)
		switch finding.Severity {
		case analyzer.SeverityError:
			result.Verdict = review.VerdictBlock
		case analyzer.SeverityWarning:
			if result.Verdict == review.VerdictPass {
				result.Verdict = review.VerdictWarn
			}
		}
	}
	if len(errs) > 0 {
		result.Summary = fmt.Sprintf("%s %d analyzer source(s) degraded.", result.Summary, len(errs))
	}
	result.Issues = issues
	result.Stats = review.Stats{}
	for _, issue := range issues {
		switch issue.Severity {
		case "error":
			result.Stats.Errors++
		case "warning":
			result.Stats.Warnings++
		case "info":
			result.Stats.Infos++
		}
	}
	return result
}

func staticSnapshot(repoRoot string, files []string, maxBytes int) (string, error) {
	if len(files) == 0 {
		return gitutil.RepoSnapshot(repoRoot, maxBytes)
	}
	return gitutil.FilesSnapshot(repoRoot, files, maxBytes)
}

func summarizeErrors(errs []error) string {
	if len(errs) == 0 {
		return ""
	}
	parts := make([]string, 0, len(errs))
	for _, err := range errs {
		parts = append(parts, err.Error())
	}
	return strings.Join(parts, "; ")
}

func reviewResponseFromResult(result *review.Result, model string, latency int64) *reviewpb.ReviewResponse {
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
	return resp
}

func snapshotFiles(snapshot string) []string {
	lines := strings.Split(snapshot, "\n")
	files := make([]string, 0)
	for _, line := range lines {
		if strings.HasPrefix(line, "// FILE: ") {
			files = append(files, strings.TrimPrefix(line, "// FILE: "))
		}
	}
	return files
}

func diffHash(diff string) string {
	if diff == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(diff))
	return fmt.Sprintf("%x", sum[:8])
}
