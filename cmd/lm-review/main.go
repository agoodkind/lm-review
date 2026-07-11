package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"goodkind.io/gklog"
	"goodkind.io/lm-review/api/reviewpb"
	"goodkind.io/lm-review/internal/config"
	"goodkind.io/lm-review/internal/daemon"
	"goodkind.io/lm-review/internal/github"
	"goodkind.io/lm-review/internal/gitutil"
	"goodkind.io/lm-review/internal/inference"
	"goodkind.io/lm-review/internal/inferenceservicecheck"
	"goodkind.io/lm-review/internal/mcpserver"
	"goodkind.io/lm-review/internal/version"
	"goodkind.io/lm-review/internal/xdg"
)

// errBlock signals that the daemon returned a block verdict; the
// process should exit non-zero. Distinguishing this from operational
// errors lets [main] exit cleanly while keeping all helpers exit-free.
var errBlock = errors.New("review verdict: block")

func init() {
	w := io.Writer(os.Stderr)
	logPath := xdg.DaemonLogPath()
	_ = os.MkdirAll(filepath.Dir(logPath), 0o700)
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err == nil {
		w = io.MultiWriter(os.Stderr, f)
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(w, nil).WithAttrs([]slog.Attr{
		slog.String("commit", version.Commit),
		slog.String("version", version.Version),
		slog.String("buildHash", version.BuildHash()),
		slog.String("dirty", version.Dirty),
	})))
}

// lmReviewLog returns the package-scoped slog logger drawn from ctx.
// Callers must pass a real context (cobra always provides one).
func lmReviewLog(ctx context.Context) *slog.Logger {
	return gklog.LoggerFromContext(ctx).With("component", "lm-review", "subcomponent", "cli")
}

// tryFindRepoRoot resolves the git repo root. When not in a git repo,
// it logs an info-level "skipping" message and returns ok=false so
// callers can short-circuit cleanly.
func tryFindRepoRoot(ctx context.Context) (string, bool) {
	repoRoot, err := gitutil.Root("")
	if err != nil {
		lmReviewLog(ctx).InfoContext(ctx, "skipping review: not in a git repo", "err", err)
		return "", false
	}
	return repoRoot, true
}

func main() {
	root := newRootCmd()

	err := root.Execute()
	if errors.Is(err, errBlock) {
		os.Exit(1)
	}
	if err != nil {
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "lm-review",
		Short: "LLM-powered local code review using LM Studio",
	}

	root.AddCommand(newDiffCmd())
	root.AddCommand(newPRCmd())
	root.AddCommand(newRepoCmd())
	root.AddCommand(newReviewCmd())
	root.AddCommand(newDaemonCmd())
	root.AddCommand(newInferenceCmd())
	root.AddCommand(newInferenceServiceCheckCmd())
	root.AddCommand(newMCPCmd())
	root.AddCommand(newInitCmd())
	root.AddCommand(newVersionCmd())
	root.AddCommand(newUpdateCmd())

	return root
}

func newDiffCmd() *cobra.Command {
	var depth, model string
	var deepCompat bool
	cmd := &cobra.Command{
		Use:   "diff",
		Short: "Review staged changes (runs on make build)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if deepCompat {
				depth = "deep"
			}
			repoRoot, ok := tryFindRepoRoot(cmd.Context())
			if !ok {
				return nil
			}
			return runReview(cmd.Context(), "diff", reviewpb.ReviewMode_REVIEW_MODE_STAGED_DIFF, repoRoot, depth, model, "")
		},
	}
	cmd.Flags().StringVar(&depth, "depth", "normal", "Review depth: quick, normal, deep, ultra")
	cmd.Flags().StringVar(&model, "model", "", "Override model for this request")
	cmd.Flags().BoolVar(&deepCompat, "deep", false, "Alias for --depth deep (deprecated)")
	_ = cmd.Flags().MarkHidden("deep")
	return cmd
}

func newPRCmd() *cobra.Command {
	var baseRef, depth, model string
	var deepCompat bool
	cmd := &cobra.Command{
		Use:   "pr",
		Short: "Review diff against main branch",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if deepCompat {
				depth = "deep"
			}
			repoRoot, err := gitutil.Root("")
			if err != nil {
				return fmt.Errorf("git root: %w", err)
			}
			return runReview(cmd.Context(), "pr", reviewpb.ReviewMode_REVIEW_MODE_PR, repoRoot, depth, model, baseRef)
		},
	}
	cmd.Flags().StringVar(&baseRef, "base-ref", "", "Base ref for PR diff")
	cmd.Flags().StringVar(&depth, "depth", "normal", "Review depth: quick, normal, deep, ultra")
	cmd.Flags().StringVar(&model, "model", "", "Override model for this request")
	cmd.Flags().BoolVar(&deepCompat, "deep", false, "Alias for --depth deep (deprecated)")
	_ = cmd.Flags().MarkHidden("deep")
	return cmd
}

func newReviewCmd() *cobra.Command {
	var baseRef, depth, mode, model string
	cmd := &cobra.Command{
		Use:   "review",
		Short: "Review the repo using a path-based mode",
		RunE: func(cmd *cobra.Command, _ []string) error {
			repoRoot, ok := tryFindRepoRoot(cmd.Context())
			if !ok {
				return nil
			}
			reviewMode, err := parseReviewMode(mode)
			if err != nil {
				return err
			}
			return runReview(cmd.Context(), "review", reviewMode, repoRoot, depth, model, baseRef)
		},
	}
	cmd.Flags().StringVar(&baseRef, "base-ref", "", "Base ref for PR mode")
	cmd.Flags().StringVar(&depth, "depth", "normal", "Review depth: quick, normal, deep, ultra")
	cmd.Flags().StringVar(&mode, "mode", "auto", "Review mode: auto, staged, worktree, pr, repo")
	cmd.Flags().StringVar(&model, "model", "", "Override model for this request")
	return cmd
}

func newRepoCmd() *cobra.Command {
	var async bool
	var depth, model string
	var deepCompat bool
	cmd := &cobra.Command{
		Use:   "repo",
		Short: "Full repo health review",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if deepCompat {
				depth = "deep"
			}
			if async {
				return runRepoAsync(cmd.Context())
			}
			return runRepoSync(cmd.Context(), depth, model)
		},
	}
	cmd.Flags().BoolVar(&async, "async", false, "Run in background, post result when done")
	cmd.Flags().StringVar(&depth, "depth", "normal", "Review depth: quick, normal, deep, ultra")
	cmd.Flags().StringVar(&model, "model", "", "Override model for this request")
	cmd.Flags().BoolVar(&deepCompat, "deep", false, "Alias for --depth deep (deprecated)")
	_ = cmd.Flags().MarkHidden("deep")
	return cmd
}

// runRepoSync runs a synchronous repo review and posts the result.
func runRepoSync(ctx context.Context, depth, model string) error {
	repoRoot, err := gitutil.Root("")
	if err != nil {
		return fmt.Errorf("git root: %w", err)
	}
	client, err := daemon.Connect(ctx)
	if err != nil {
		lmReviewLog(ctx).InfoContext(ctx, "skipping review: daemon unavailable", "err", err)
		return nil
	}
	defer client.Close()

	resp, err := client.Review(ctx, reviewpb.ReviewMode_REVIEW_MODE_REPO, repoRoot, depth, model, "")
	if err != nil {
		return fmt.Errorf("review repo: %w", err)
	}

	printResult(resp)
	postErr := github.UpsertComment("repo", formatMarkdown("repo", resp))
	if postErr != nil {
		lmReviewLog(ctx).InfoContext(ctx, "could not post PR comment", "err", postErr)
	}
	return nil
}

func newDaemonCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "daemon",
		Short:  "Start the lm-review daemon",
		Hidden: true,
		RunE: func(_ *cobra.Command, _ []string) error {
			err := daemon.Run()
			if err != nil {
				return fmt.Errorf("daemon: %w", err)
			}
			return nil
		},
	}
}

func newInferenceCmd() *cobra.Command {
	var listenAddress string
	var model string
	cmd := &cobra.Command{
		Use:   "inference",
		Short: "Start the lm-review inference gRPC service",
		RunE: func(cmd *cobra.Command, _ []string) error {
			log := lmReviewLog(cmd.Context())
			cfg, err := config.Load()
			if err != nil {
				log.ErrorContext(cmd.Context(), "inference.config.load_failed", "err", err)
				return fmt.Errorf("load config: %w", err)
			}
			if listenAddress == "" {
				listenAddress = cfg.Inference.ResolveListenAddress()
			}
			if model == "" {
				model = cfg.Inference.ResolveModel()
			}
			effectiveBackend, err := cfg.Inference.ResolveBackendCredential(cfg.OpenAICompat)
			if err != nil {
				log.ErrorContext(cmd.Context(), "inference.backend.resolve_failed", "err", err)
				return fmt.Errorf("resolve inference backend: %w", err)
			}
			log.InfoContext(cmd.Context(), "inference.serve.begin",
				"listen_address", listenAddress,
				"model", model)
			server := newInferenceServer(effectiveBackend, model)
			err = inference.Serve(cmd.Context(), listenAddress, server)
			if err != nil {
				log.ErrorContext(cmd.Context(), "inference.serve.failed", "err", err)
				return fmt.Errorf("serve inference: %w", err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&listenAddress, "listen", "", "gRPC listen address")
	cmd.Flags().StringVar(&model, "model", "", "Inference model ID")
	return cmd
}

func newInferenceServer(backend config.OpenAICompat, model string) *inference.Server {
	return inference.NewOpenAICompatibleServer(
		model,
		backend.URL,
		backend.Token,
		backend.ResolveMaxResponseTokens(),
		backend.ResolveRequestTimeout(),
		backend.ResolveChatSettings(),
	)
}

func newInferenceServiceCheckCmd() *cobra.Command {
	var listenAddress string
	var phase string
	var expectedPID int
	cmd := &cobra.Command{
		Use:    "inference-service-check",
		Short:  "Verify supervised inference listener ownership and health",
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			log := lmReviewLog(cmd.Context())
			if listenAddress == "" {
				cfg, err := config.Load()
				if err != nil {
					log.ErrorContext(cmd.Context(), "inference.service_check.config_failed", "err", err)
					return fmt.Errorf("load config: %w", err)
				}
				listenAddress = cfg.Inference.ResolveListenAddress()
			}
			checkContext, cancel := context.WithTimeout(cmd.Context(), 2*time.Second)
			defer cancel()
			if err := inferenceservicecheck.Check(
				checkContext,
				phase,
				listenAddress,
				expectedPID,
				inferenceservicecheck.DefaultDependencies(),
			); err != nil {
				log.ErrorContext(cmd.Context(), "inference.service_check.failed", "phase", phase, "err", err)
				return fmt.Errorf("inference service %s check: %w", phase, err)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&listenAddress, "listen", "", "gRPC listen address")
	cmd.Flags().StringVar(&phase, "phase", "", "Check phase: preflight or post-restart")
	cmd.Flags().IntVar(&expectedPID, "expected-pid", 0, "Expected supervised service PID")
	_ = cmd.MarkFlagRequired("phase")
	return cmd
}

func newMCPCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Start MCP stdio server for Claude Code integration",
		RunE: func(cmd *cobra.Command, _ []string) error {
			err := mcpserver.Serve(cmd.Context())
			if err != nil {
				return fmt.Errorf("mcp serve: %w", err)
			}
			return nil
		},
	}
}

// runReview connects to the daemon, runs a path-based review, prints and posts
// the result, and signals a block verdict via [errBlock].
func runReview(ctx context.Context, scope string, mode reviewpb.ReviewMode, repoPath, depth, model string, baseRef string) error {
	lmReviewLog(ctx).InfoContext(ctx, "lm-review.runReview.begin",
		"scope", scope, "mode", mode.String(), "depth", depth, "model", model)

	client, err := daemon.Connect(ctx)
	if err != nil {
		lmReviewLog(ctx).InfoContext(ctx, "skipping review: daemon unavailable", "err", err)
		return nil
	}
	defer client.Close()

	resp, err := client.Review(ctx, mode, repoPath, depth, model, baseRef)
	if err != nil {
		lmReviewLog(ctx).ErrorContext(ctx, "lm-review.runReview.failed", "scope", scope, "err", err)
		return fmt.Errorf("review: %w", err)
	}

	printResult(resp)

	postErr := github.UpsertComment(scope, formatMarkdown(scope, resp))
	if postErr != nil {
		lmReviewLog(ctx).InfoContext(ctx, "could not post PR comment", "err", postErr)
	}

	if resp.GetVerdict() == "block" {
		return errBlock
	}
	return nil
}

func printResult(resp *reviewpb.ReviewResponse) {
	icon := map[string]string{"pass": "✅", "warn": "⚠️", "block": "🚫", "skip": "⏭️"}[resp.GetVerdict()]
	fmt.Fprintf(os.Stderr, "\nlm-review [%s] %s %s: %s\n",
		resp.GetModel(), icon, strings.ToUpper(resp.GetVerdict()), resp.GetSummary())
	for _, issue := range resp.GetIssues() {
		fmt.Fprintf(os.Stderr, "  %s:%d [%s] %s\n", issue.GetFile(), issue.GetLine(), issue.GetRule(), issue.GetMessage())
	}
	fmt.Fprintln(os.Stderr)
}

func formatMarkdown(scope string, resp *reviewpb.ReviewResponse) string {
	icon := map[string]string{"pass": "✅", "warn": "⚠️", "block": "🚫"}[resp.GetVerdict()]
	label := map[string]string{"diff": "Fast Review", "pr": "PR Review", "repo": "Repo Health", "review": "Review"}[scope]
	var sb strings.Builder
	fmt.Fprintf(&sb, "## 🤖 %s (%s, %dms)\n\n**Verdict:** %s %s\n\n%s\n",
		label, resp.GetModel(), resp.GetLatencyMs(), icon, strings.ToUpper(resp.GetVerdict()), resp.GetSummary())
	if len(resp.GetIssues()) > 0 {
		sb.WriteString("\n| Severity | File | Line | Rule | Message |\n|---|---|---|---|---|\n")
		for _, issue := range resp.GetIssues() {
			sev := map[string]string{"error": "🚫", "warning": "⚠️", "info": "ℹ️"}[issue.GetSeverity()]
			fmt.Fprintf(&sb, "| %s | `%s` | %d | `%s` | %s |\n", sev, issue.GetFile(), issue.GetLine(), issue.GetRule(), issue.GetMessage())
		}
	}
	fmt.Fprintf(&sb, "\n<!-- lm-review:%s -->\n", scope)
	return sb.String()
}

type reviewModeName string

const (
	reviewModeNameEmpty    reviewModeName = ""
	reviewModeNameAuto     reviewModeName = "auto"
	reviewModeNameStaged   reviewModeName = "staged"
	reviewModeNameStagedID reviewModeName = "staged_diff"
	reviewModeNameDiff     reviewModeName = "diff"
	reviewModeNameWorktree reviewModeName = "worktree"
	reviewModeNameWorkID   reviewModeName = "worktree_diff"
	reviewModeNamePR       reviewModeName = "pr"
	reviewModeNameRepo     reviewModeName = "repo"
)

func parseReviewMode(mode string) (reviewpb.ReviewMode, error) {
	switch reviewModeName(strings.ToLower(mode)) {
	case reviewModeNameEmpty, reviewModeNameAuto:
		return reviewpb.ReviewMode_REVIEW_MODE_AUTO, nil
	case reviewModeNameStaged, reviewModeNameStagedID, reviewModeNameDiff:
		return reviewpb.ReviewMode_REVIEW_MODE_STAGED_DIFF, nil
	case reviewModeNameWorktree, reviewModeNameWorkID:
		return reviewpb.ReviewMode_REVIEW_MODE_WORKTREE_DIFF, nil
	case reviewModeNamePR:
		return reviewpb.ReviewMode_REVIEW_MODE_PR, nil
	case reviewModeNameRepo:
		return reviewpb.ReviewMode_REVIEW_MODE_REPO, nil
	default:
		return reviewpb.ReviewMode_REVIEW_MODE_UNSPECIFIED, fmt.Errorf("unknown review mode %q", mode)
	}
}

func runRepoAsync(ctx context.Context) error {
	self, err := os.Executable()
	if err != nil {
		lmReviewLog(ctx).ErrorContext(ctx, "lm-review.runRepoAsync.executable_failed", "err", err)
		return fmt.Errorf("locate executable: %w", err)
	}
	// Detach from the calling context's cancellation so the background
	// process is not killed when the parent CLI exits, while keeping
	// any context values for tracing.
	cmd := newBgCmd(context.WithoutCancel(ctx), self, "repo")
	startErr := cmd.Start()
	if startErr != nil {
		lmReviewLog(ctx).ErrorContext(ctx, "lm-review.runRepoAsync.start_failed", "err", startErr)
		return fmt.Errorf("start async repo review: %w", startErr)
	}
	go func() {
		defer func() {
			if r := recover(); r != nil {
				lmReviewLog(ctx).ErrorContext(ctx, "lm-review.runRepoAsync.wait_panicked", "panic", r)
			}
		}()
		_ = cmd.Wait()
	}()
	lmReviewLog(ctx).InfoContext(ctx, "deep repo review running in background")
	return nil
}
