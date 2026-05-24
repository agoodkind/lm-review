// Package daemon implements the lm-review gRPC daemon.
// It serializes requests to LM Studio, maintains the audit trail,
// and is the single point of contact for both the CLI and the MCP server.
package daemon

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/peer"

	"goodkind.io/gklog"
	"goodkind.io/lm-review/api/reviewpb"
	"goodkind.io/lm-review/internal/analyzer"
	"goodkind.io/lm-review/internal/audit"
	"goodkind.io/lm-review/internal/claude"
	"goodkind.io/lm-review/internal/config"
	"goodkind.io/lm-review/internal/gitutil"
	"goodkind.io/lm-review/internal/lmstudio"
	"goodkind.io/lm-review/internal/requestmeta"
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
	ctx := context.Background()
	slog := gklog.LoggerFromContext(ctx).With("component", "lm-review", "subcomponent", "daemon")
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

// Review runs a path-based LLM review.
func (s *Server) Review(ctx context.Context, req *reviewpb.ReviewRequest) (*reviewpb.ReviewResponse, error) {
	return s.runReview(ctx, req)
}

// ReviewStatic runs deterministic static analysis with optional LLM synthesis.
func (s *Server) ReviewStatic(ctx context.Context, req *reviewpb.StaticReviewRequest) (*reviewpb.ReviewResponse, error) {
	return s.runStaticReview(ctx, req)
}

// buildClient constructs the appropriate ChatClient and resolves the model name.
func (s *Server) buildClient(scope string, depth string, modelOverride string) (review.ChatClient, string) {
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

	return lmstudio.New(
		s.cfg.OpenAICompat.URL,
		s.cfg.OpenAICompat.Token,
		model,
		s.cfg.OpenAICompat.ResolveMaxResponseTokens(),
		s.cfg.OpenAICompat.ResolveRequestTimeout(),
	), model
}

type preparedReviewInput struct {
	scope           string
	input           string
	fullPrompt      review.UserPromptBuilder
	chunkPrompt     review.ChunkPromptBuilder
	inputSplitter   review.InputSplitter
	selectedFiles   []string
	selectedModeLog string
}

func (s *Server) runReview(ctx context.Context, req *reviewpb.ReviewRequest) (*reviewpb.ReviewResponse, error) {
	start := time.Now()
	reviewID := newReviewID()
	log := gklog.LoggerFromContext(ctx).With("component", "lm-review", "subcomponent", "daemon")
	if peerInfo, ok := peer.FromContext(ctx); ok && peerInfo.Addr != nil {
		log = log.With("peer_addr", peerInfo.Addr.String())
	}

	depth := req.GetDepth()
	if depth == "" {
		depth = "normal"
	}

	prepared, err := s.prepareReviewInput(ctx, req)
	if err != nil {
		log.ErrorContext(ctx, "review.input.prepare_failed", "review_id", reviewID, "err", err)
		return nil, fmt.Errorf("prepare review input: %w", err)
	}

	if strings.TrimSpace(prepared.input) == "" {
		return &reviewpb.ReviewResponse{
			Verdict:   string(review.VerdictSkip),
			Summary:   "No changes to review.",
			LatencyMs: time.Since(start).Milliseconds(),
		}, nil
	}

	chunkBytes := s.cfg.OpenAICompat.ResolveReviewChunkBytes()
	ctx = requestmeta.With(ctx, requestmeta.Metadata{
		ReviewID:   reviewID,
		Scope:      prepared.scope,
		Mode:       prepared.selectedModeLog,
		Depth:      depth,
		ChunkIndex: 0,
		ChunkTotal: 0,
	})
	s.logPreparedInput(ctx, req.GetPath(), depth, chunkBytes, prepared)

	client, model := s.buildClient(prepared.scope, depth, req.GetModel())
	log.InfoContext(ctx, "review.chat.configured",
		"review_id", reviewID,
		"provider", s.cfg.ResolveProvider(),
		"model", model,
		"chunk_parallelism", s.cfg.OpenAICompat.ResolveChunkParallelism(),
		"request_timeout_ms", s.cfg.OpenAICompat.ResolveRequestTimeout().Milliseconds(),
		"max_response_tokens", s.cfg.OpenAICompat.ResolveMaxResponseTokens())

	cfg := s.reviewConfig(req.GetPath(), prepared.scope)
	rules := filteredRules(cfg, prepared.selectedFiles)
	buildPrompt := promptBuilderForDepth(depth)

	result, err := executeReview(ctx, client, prepared, rules, buildPrompt, chunkBytes, s.cfg.OpenAICompat.ResolveChunkParallelism())
	model = s.verifyUltra(ctx, depth, model, result, prepared)

	latency := time.Since(start).Milliseconds()
	if err != nil {
		s.logReviewFailure(prepared, model, latency, err)
		log.ErrorContext(ctx, "review.run.failed", "review_id", reviewID, "latency_ms", latency, "err", err)
		return nil, fmt.Errorf("review failed: %w", err)
	}

	s.logReviewSuccess(prepared, model, latency, result)
	log.InfoContext(ctx, "review.run.finished",
		"review_id", reviewID,
		"latency_ms", latency,
		"verdict", result.Verdict,
		"issue_count", len(result.Issues))
	return reviewResponseFromResult(result, model, latency), nil
}

func (s *Server) logPreparedInput(ctx context.Context, repoPath string, depth string, chunkBytes int, prepared *preparedReviewInput) {
	chunks := prepared.inputSplitter(prepared.input, chunkBytes)
	log := gklog.LoggerFromContext(ctx).With("component", "lm-review", "subcomponent", "daemon")
	log.InfoContext(ctx, "review.input.loaded",
		"review_id", requestmeta.From(ctx).ReviewID,
		"scope", prepared.scope,
		"mode", prepared.selectedModeLog,
		"path", repoPath,
		"depth", depth,
		"input_bytes", len(prepared.input),
		"file_count", len(prepared.selectedFiles),
		"chunk_bytes", chunkBytes,
		"chunk_count", len(chunks))
}

func (s *Server) logStaticChatConfig(ctx context.Context, log *slog.Logger, reviewID string, model string) {
	log.InfoContext(ctx, "review.static.chat.configured",
		"review_id", reviewID,
		"provider", s.cfg.ResolveProvider(),
		"model", model,
		"request_timeout_ms", s.cfg.OpenAICompat.ResolveRequestTimeout().Milliseconds(),
		"max_response_tokens", s.cfg.OpenAICompat.ResolveMaxResponseTokens())
}

func newReviewID() string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return "lmrev-" + hex.EncodeToString(raw[:])
	}
	return fmt.Sprintf("lmrev-%d", time.Now().UnixNano())
}

func (s *Server) reviewConfig(repoPath string, scope string) *config.Config {
	if repoPath == "" {
		return s.cfg
	}
	cfg, err := config.MergeProjectRules(s.cfg, repoPath)
	if err != nil {
		s.log.Write(audit.Entry{Scope: scope, Error: err.Error()})
		return s.cfg
	}
	return cfg
}

func filteredRules(cfg *config.Config, selectedFiles []string) []string {
	texts := make([]string, len(cfg.Rules))
	filters := make([]review.RuleFilter, len(cfg.Rules))
	for i, rule := range cfg.Rules {
		texts[i] = rule.Text
		filters[i] = review.RuleFilter{Globs: rule.Globs, Always: rule.Always}
	}
	return review.FilterRules(texts, filters, selectedFiles)
}

type reviewDepth string

const (
	reviewDepthQuick reviewDepth = "quick"
	reviewDepthDeep  reviewDepth = "deep"
	reviewDepthUltra reviewDepth = "ultra"
)

func promptBuilderForDepth(depth string) review.PromptBuilder {
	switch reviewDepth(depth) {
	case reviewDepthQuick:
		return review.BuildQuickSystemPrompt
	case reviewDepthDeep, reviewDepthUltra:
		return review.BuildDeepSystemPrompt
	default:
		return review.BuildSystemPrompt
	}
}

func executeReview(ctx context.Context, client review.ChatClient, prepared *preparedReviewInput, rules []string, buildPrompt review.PromptBuilder, chunkBytes int, parallelism int) (*review.Result, error) {
	log := gklog.LoggerFromContext(ctx).With("component", "lm-review", "subcomponent", "daemon")
	if len(prepared.input) > chunkBytes {
		result, err := review.ChunkedReview(ctx, client, prepared.input, prepared.scope, rules, buildPrompt, prepared.fullPrompt, prepared.chunkPrompt, prepared.inputSplitter, chunkBytes, parallelism)
		if err != nil {
			log.ErrorContext(ctx, "review.execute.chunked_failed", "err", err)
			return nil, fmt.Errorf("chunked review: %w", err)
		}
		return result, nil
	}
	reviewer := review.NewWithPromptBuilder(client, prepared.scope, rules, buildPrompt)
	result, err := reviewer.ReviewInput(ctx, prepared.input, prepared.fullPrompt)
	if err != nil {
		log.ErrorContext(ctx, "review.execute.input_failed", "err", err)
		return nil, fmt.Errorf("review input: %w", err)
	}
	return result, nil
}

func (s *Server) verifyUltra(ctx context.Context, depth string, model string, result *review.Result, prepared *preparedReviewInput) string {
	if depth != "ultra" || result == nil || len(result.Issues) == 0 {
		return model
	}
	verifyClient, verifyModel := s.buildClient(prepared.scope, "ultra", "")
	verified, err := review.VerifyIssues(ctx, verifyClient, result.Issues, prepared.input)
	if err != nil {
		return model
	}
	beforeCount := len(result.Issues)
	result.Issues = verified
	s.log.Write(audit.Entry{
		Timestamp:  time.Time{},
		Scope:      prepared.scope,
		Model:      verifyModel,
		DiffHash:   "",
		LatencyMS:  0,
		Verdict:    "",
		IssueCount: 0,
		Error:      fmt.Sprintf("ultra verify: %d->%d issues", beforeCount, len(verified)),
	})
	return verifyModel
}

func (s *Server) logReviewFailure(prepared *preparedReviewInput, model string, latency int64, err error) {
	s.log.Write(audit.Entry{
		Timestamp:  time.Time{},
		Scope:      prepared.scope,
		Model:      model,
		DiffHash:   diffHash(prepared.input),
		LatencyMS:  latency,
		Verdict:    "",
		IssueCount: 0,
		Error:      err.Error(),
	})
}

func (s *Server) logReviewSuccess(prepared *preparedReviewInput, model string, latency int64, result *review.Result) {
	s.log.Write(audit.Entry{
		Timestamp:  time.Time{},
		Scope:      prepared.scope,
		Model:      model,
		DiffHash:   diffHash(prepared.input),
		LatencyMS:  latency,
		Verdict:    string(result.Verdict),
		IssueCount: len(result.Issues),
		Error:      "",
	})
}

func (s *Server) prepareReviewInput(ctx context.Context, req *reviewpb.ReviewRequest) (*preparedReviewInput, error) {
	repoRoot := req.GetPath()
	if repoRoot == "" {
		return nil, fmt.Errorf("path is required")
	}

	mode := req.GetMode()
	if mode == reviewpb.ReviewMode_REVIEW_MODE_UNSPECIFIED {
		mode = reviewpb.ReviewMode_REVIEW_MODE_AUTO
	}

	switch mode {
	case reviewpb.ReviewMode_REVIEW_MODE_UNSPECIFIED:
		return prepareAutoInput(ctx, repoRoot, req.GetBaseRef())
	case reviewpb.ReviewMode_REVIEW_MODE_AUTO:
		return prepareAutoInput(ctx, repoRoot, req.GetBaseRef())
	case reviewpb.ReviewMode_REVIEW_MODE_STAGED_DIFF:
		return prepareDiffInput(ctx, "staged", func() (string, error) {
			return gitutil.StagedDiff(repoRoot)
		})
	case reviewpb.ReviewMode_REVIEW_MODE_WORKTREE_DIFF:
		return prepareDiffInput(ctx, "worktree", func() (string, error) {
			return gitutil.WorktreeDiff(repoRoot)
		})
	case reviewpb.ReviewMode_REVIEW_MODE_PR:
		return prepareDiffInput(ctx, "pr", func() (string, error) {
			return gitutil.PRDiff(repoRoot, req.GetBaseRef())
		})
	case reviewpb.ReviewMode_REVIEW_MODE_REPO:
		return prepareRepoInput(ctx, repoRoot, "repo")
	default:
		return nil, fmt.Errorf("unknown review mode: %s", mode.String())
	}
}

func prepareAutoInput(ctx context.Context, repoRoot string, baseRef string) (*preparedReviewInput, error) {
	log := gklog.LoggerFromContext(ctx).With("component", "lm-review", "subcomponent", "daemon")
	staged, err := gitutil.StagedDiff(repoRoot)
	if err != nil {
		log.ErrorContext(ctx, "review.auto.staged_diff_failed", "err", err)
		return nil, fmt.Errorf("staged diff: %w", err)
	}
	if strings.TrimSpace(staged) != "" {
		return newDiffInput(staged, "auto-staged"), nil
	}

	worktree, err := gitutil.WorktreeDiff(repoRoot)
	if err != nil {
		log.ErrorContext(ctx, "review.auto.worktree_diff_failed", "err", err)
		return nil, fmt.Errorf("worktree diff: %w", err)
	}
	if strings.TrimSpace(worktree) != "" {
		return newDiffInput(worktree, "auto-worktree"), nil
	}

	pr, err := gitutil.PRDiff(repoRoot, baseRef)
	if err == nil && strings.TrimSpace(pr) != "" {
		return newDiffInput(pr, "auto-pr"), nil
	}

	return prepareRepoInput(ctx, repoRoot, "auto-repo")
}

func prepareDiffInput(ctx context.Context, modeLog string, load func() (string, error)) (*preparedReviewInput, error) {
	log := gklog.LoggerFromContext(ctx).With("component", "lm-review", "subcomponent", "daemon")
	diff, err := load()
	if err != nil {
		log.ErrorContext(ctx, "review.diff.load_failed", "mode", modeLog, "err", err)
		return nil, fmt.Errorf("%s diff: %w", modeLog, err)
	}
	return newDiffInput(diff, modeLog), nil
}

func prepareRepoInput(ctx context.Context, repoRoot string, modeLog string) (*preparedReviewInput, error) {
	log := gklog.LoggerFromContext(ctx).With("component", "lm-review", "subcomponent", "daemon")
	snapshot, err := gitutil.RepoSnapshot(repoRoot, 0)
	if err != nil {
		log.ErrorContext(ctx, "review.repo.snapshot_failed", "err", err)
		return nil, fmt.Errorf("repo snapshot: %w", err)
	}
	return &preparedReviewInput{
		scope:           "repo",
		input:           snapshot,
		fullPrompt:      review.RepoPrompt,
		chunkPrompt:     review.ChunkPrompt,
		inputSplitter:   review.SplitRepoInput,
		selectedFiles:   snapshotFiles(snapshot),
		selectedModeLog: modeLog,
	}, nil
}

func newDiffInput(diff string, modeLog string) *preparedReviewInput {
	return &preparedReviewInput{
		scope:           "diff",
		input:           diff,
		fullPrompt:      review.DiffPrompt,
		chunkPrompt:     review.DiffChunkPrompt,
		inputSplitter:   review.SplitDiffInput,
		selectedFiles:   review.FilesFromDiff(diff),
		selectedModeLog: modeLog,
	}
}

func (s *Server) runStaticReview(ctx context.Context, req *reviewpb.StaticReviewRequest) (*reviewpb.ReviewResponse, error) {
	start := time.Now()
	reviewID := newReviewID()
	log := gklog.LoggerFromContext(ctx).With("component", "lm-review", "subcomponent", "daemon")
	if peerInfo, ok := peer.FromContext(ctx); ok && peerInfo.Addr != nil {
		log = log.With("peer_addr", peerInfo.Addr.String())
	}
	if !s.cfg.StaticReview.IsEnabled() {
		return &reviewpb.ReviewResponse{
			Verdict:   string(review.VerdictSkip),
			Summary:   "Static review is disabled in config.",
			LatencyMs: time.Since(start).Milliseconds(),
		}, nil
	}

	analyzerConfig := s.staticAnalyzerConfig(req)
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
	ctx = requestmeta.With(ctx, requestmeta.Metadata{
		ReviewID:   reviewID,
		Scope:      "static",
		Mode:       "static",
		Depth:      depth,
		ChunkIndex: 0,
		ChunkTotal: 0,
	})

	client, model := s.buildClient("repo", depth, req.Model)
	s.logStaticChatConfig(ctx, log, reviewID, model)

	snapshot, err := staticSnapshot(req.Path, req.Files, s.cfg.OpenAICompat.ResolveRepoMaxBytes())
	if err != nil {
		return nil, fmt.Errorf("static snapshot: %w", err)
	}

	rules := s.staticRules(req, snapshot)
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

func (s *Server) staticAnalyzerConfig(req *reviewpb.StaticReviewRequest) analyzer.Config {
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
	return analyzerConfig
}

func (s *Server) staticRules(req *reviewpb.StaticReviewRequest, snapshot string) []string {
	cfg := s.cfg
	if req.Path != "" {
		mergedCfg, mergeErr := config.MergeProjectRules(s.cfg, req.Path)
		if mergeErr != nil {
			s.log.Write(audit.Entry{Scope: "static", Error: mergeErr.Error()})
		} else {
			cfg = mergedCfg
		}
	}
	selectedFiles := req.Files
	if len(selectedFiles) == 0 {
		selectedFiles = snapshotFiles(snapshot)
	}
	return filteredRules(cfg, selectedFiles)
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
		switch analyzer.Severity(issue.Severity) {
		case analyzer.SeverityError:
			result.Stats.Errors++
		case analyzer.SeverityWarning:
			result.Stats.Warnings++
		case analyzer.SeverityInfo:
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
