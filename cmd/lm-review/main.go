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

	"github.com/spf13/cobra"

	"goodkind.io/gklog"
	"goodkind.io/lm-review/api/reviewpb"
	"goodkind.io/lm-review/internal/daemon"
	"goodkind.io/lm-review/internal/github"
	"goodkind.io/lm-review/internal/gitutil"
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
	root := &cobra.Command{
		Use:   "lm-review",
		Short: "LLM-powered local code review using LM Studio",
	}

	root.AddCommand(newDiffCmd())
	root.AddCommand(newPRCmd())
	root.AddCommand(newRepoCmd())
	root.AddCommand(newDaemonCmd())
	root.AddCommand(newMCPCmd())
	root.AddCommand(newInitCmd())

	err := root.Execute()
	if errors.Is(err, errBlock) {
		os.Exit(1)
	}
	if err != nil {
		os.Exit(1)
	}
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
			diff, diffErr := gitutil.StagedDiff(repoRoot)
			if diffErr != nil {
				return fmt.Errorf("staged diff: %w", diffErr)
			}
			return runReview(cmd.Context(), "diff", diff, repoRoot, depth, model)
		},
	}
	cmd.Flags().StringVar(&depth, "depth", "normal", "Review depth: quick, normal, deep, ultra")
	cmd.Flags().StringVar(&model, "model", "", "Override model for this request")
	cmd.Flags().BoolVar(&deepCompat, "deep", false, "Alias for --depth deep (deprecated)")
	_ = cmd.Flags().MarkHidden("deep")
	return cmd
}

func newPRCmd() *cobra.Command {
	var depth, model string
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
			diff, err := gitutil.PRDiff(repoRoot)
			if err != nil {
				return fmt.Errorf("pr diff: %w", err)
			}
			return runReview(cmd.Context(), "pr", diff, repoRoot, depth, model)
		},
	}
	cmd.Flags().StringVar(&depth, "depth", "normal", "Review depth: quick, normal, deep, ultra")
	cmd.Flags().StringVar(&model, "model", "", "Override model for this request")
	cmd.Flags().BoolVar(&deepCompat, "deep", false, "Alias for --depth deep (deprecated)")
	_ = cmd.Flags().MarkHidden("deep")
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
	files, err := gitutil.RepoSnapshot(repoRoot, 0)
	if err != nil {
		return fmt.Errorf("repo snapshot: %w", err)
	}

	client, err := daemon.Connect(ctx)
	if err != nil {
		lmReviewLog(ctx).InfoContext(ctx, "skipping review: daemon unavailable", "err", err)
		return nil
	}
	defer client.Close()

	resp, err := client.ReviewRepo(ctx, files, repoRoot, depth, model)
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

// runReview connects to the daemon, runs a diff or PR review, prints
// and posts the result, and signals a block verdict via [errBlock].
func runReview(ctx context.Context, scope, diff, repoPath, depth, model string) error {
	lmReviewLog(ctx).InfoContext(ctx, "lm-review.runReview.begin",
		"scope", scope, "depth", depth, "model", model)

	client, err := daemon.Connect(ctx)
	if err != nil {
		lmReviewLog(ctx).InfoContext(ctx, "skipping review: daemon unavailable", "err", err)
		return nil
	}
	defer client.Close()

	var resp *reviewpb.ReviewResponse
	switch scope {
	case "diff":
		resp, err = client.ReviewDiff(ctx, diff, repoPath, depth, model)
	case "pr":
		resp, err = client.ReviewPR(ctx, diff, repoPath, depth, model)
	}
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
	label := map[string]string{"diff": "Fast Review", "pr": "PR Review", "repo": "Repo Health"}[scope]
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
