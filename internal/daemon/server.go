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
	"time"

	"google.golang.org/grpc"

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

func (s *Server) runReview(ctx context.Context, scope string, req *reviewpb.ReviewRequest) (*reviewpb.ReviewResponse, error) {
	start := time.Now()

	// Build the appropriate ChatClient based on provider config.
	var client review.ChatClient
	var model string

	switch s.cfg.ResolveProvider() {
	case "claude":
		model = s.cfg.Claude.Model
		if req.Model != "" {
			model = req.Model
		}
		client = claude.New(model)
	default:
		model = req.Model
		if model == "" {
			model = s.cfg.LMStudio.ResolveModel(scope, req.Deep)
		}
		if err := lmstudio.EnsureLoaded(ctx, model, s.cfg.LMStudio.ResolveContextLength()); err != nil {
			s.log.Write(audit.Entry{Scope: scope, Error: fmt.Sprintf("model load: %v", err)})
		}
		client = lmstudio.New(s.cfg.LMStudio.URL, s.cfg.LMStudio.Token, model, s.cfg.LMStudio.ResolveMaxResponseTokens())
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

	var (
		result *review.Result
		err    error
	)
	repoMaxBytes := s.cfg.LMStudio.ResolveRepoMaxBytes()
	if scope == "repo" && len(req.Diff) > repoMaxBytes {
		result, err = review.ChunkedRepoReview(ctx, client, req.Diff, scope, rules, repoMaxBytes, s.cfg.LMStudio.ResolveChunkParallelism())
	} else {
		r := review.New(client, scope, rules)
		result, err = r.ReviewDiff(ctx, req.Diff)
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
