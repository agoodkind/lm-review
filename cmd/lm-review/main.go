package main

import (
	"context"
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

func init() {
	w := io.Writer(os.Stderr)
	logPath := xdg.DaemonLogPath()
	_ = os.MkdirAll(filepath.Dir(logPath), 0o700)
	if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600); err == nil {
		w = io.MultiWriter(os.Stderr, f)
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(w, nil).WithAttrs([]slog.Attr{
		slog.String("commit", version.Commit),
		slog.String("version", version.Version),
		slog.String("buildHash", version.BuildHash()),
		slog.String("dirty", version.Dirty),
	})))
}

func lmReviewLog(ctx context.Context) *slog.Logger {
	if ctx == nil {
		ctx = context.Background()
	}
	return gklog.LoggerFromContext(ctx).With("component", "lm-review", "subcomponent", "cli")
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

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func newDiffCmd() *cobra.Command {
	var depth, model string
	var deepCompat bool
	cmd := &cobra.Command{
		Use:   "diff",
		Short: "Review staged changes (runs on make build)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if deepCompat {
				depth = "deep"
			}
			repoRoot, err := gitutil.Root("")
			if err != nil {
				lmReviewLog(cmd.Context()).InfoContext(cmd.Context(), "skipping review: not in a git repo")
				return nil
			}
			diff, err := gitutil.StagedDiff(repoRoot)
			if err != nil {
				return err
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
		RunE: func(cmd *cobra.Command, args []string) error {
			if deepCompat {
				depth = "deep"
			}
			repoRoot, err := gitutil.Root("")
			if err != nil {
				return err
			}
			diff, err := gitutil.PRDiff(repoRoot)
			if err != nil {
				return err
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
		RunE: func(cmd *cobra.Command, args []string) error {
			if deepCompat {
				depth = "deep"
			}
			if async {
				return runRepoAsync()
			}
			repoRoot, err := gitutil.Root("")
			if err != nil {
				return err
			}
			files, err := gitutil.RepoSnapshot(repoRoot, 0)
			if err != nil {
				return err
			}

			client, err := daemon.Connect(cmd.Context())
			if err != nil {
				lmReviewLog(cmd.Context()).InfoContext(cmd.Context(), "skipping review: daemon unavailable", "err", err)
				return nil
			}
			defer client.Close()

			resp, err := client.ReviewRepo(cmd.Context(), files, repoRoot, depth, model)
			if err != nil {
				return err
			}

			printResult(resp)
			if postErr := github.UpsertComment("repo", formatMarkdown("repo", resp)); postErr != nil {
				lmReviewLog(cmd.Context()).InfoContext(cmd.Context(), "could not post PR comment", "err", postErr)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&async, "async", false, "Run in background, post result when done")
	cmd.Flags().StringVar(&depth, "depth", "normal", "Review depth: quick, normal, deep, ultra")
	cmd.Flags().StringVar(&model, "model", "", "Override model for this request")
	cmd.Flags().BoolVar(&deepCompat, "deep", false, "Alias for --depth deep (deprecated)")
	_ = cmd.Flags().MarkHidden("deep")
	return cmd
}

func newDaemonCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "daemon",
		Short:  "Start the lm-review daemon",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return daemon.Run()
		},
	}
}

func newMCPCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "Start MCP stdio server for Claude Code integration",
		RunE: func(cmd *cobra.Command, args []string) error {
			return mcpserver.Serve(cmd.Context())
		},
	}
}

func runReview(ctx context.Context, scope, diff, repoPath string, depth string, model string) error {
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
		return fmt.Errorf("review: %w", err)
	}

	printResult(resp)

	if postErr := github.UpsertComment(scope, formatMarkdown(scope, resp)); postErr != nil {
		lmReviewLog(ctx).InfoContext(ctx, "could not post PR comment", "err", postErr)
	}

	if resp.Verdict == "block" {
		os.Exit(1)
	}
	return nil
}

func printResult(resp *reviewpb.ReviewResponse) {
	icon := map[string]string{"pass": "✅", "warn": "⚠️", "block": "🚫", "skip": "⏭️"}[resp.Verdict]
	fmt.Fprintf(os.Stderr, "\nlm-review [%s] %s %s: %s\n",
		resp.Model, icon, strings.ToUpper(resp.Verdict), resp.Summary)
	for _, issue := range resp.Issues {
		fmt.Fprintf(os.Stderr, "  %s:%d [%s] %s\n", issue.File, issue.Line, issue.Rule, issue.Message)
	}
	fmt.Fprintln(os.Stderr)
}

func formatMarkdown(scope string, resp *reviewpb.ReviewResponse) string {
	icon := map[string]string{"pass": "✅", "warn": "⚠️", "block": "🚫"}[resp.Verdict]
	label := map[string]string{"diff": "Fast Review", "pr": "PR Review", "repo": "Repo Health"}[scope]
	var sb strings.Builder
	fmt.Fprintf(&sb, "## 🤖 %s (%s, %dms)\n\n**Verdict:** %s %s\n\n%s\n",
		label, resp.Model, resp.LatencyMs, icon, strings.ToUpper(resp.Verdict), resp.Summary)
	if len(resp.Issues) > 0 {
		sb.WriteString("\n| Severity | File | Line | Rule | Message |\n|---|---|---|---|---|\n")
		for _, issue := range resp.Issues {
			sev := map[string]string{"error": "🚫", "warning": "⚠️", "info": "ℹ️"}[issue.Severity]
			fmt.Fprintf(&sb, "| %s | `%s` | %d | `%s` | %s |\n", sev, issue.File, issue.Line, issue.Rule, issue.Message)
		}
	}
	fmt.Fprintf(&sb, "\n<!-- lm-review:%s -->\n", scope)
	return sb.String()
}

func runRepoAsync() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	cmd := newBgCmd(self, "repo")
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start async repo review: %w", err)
	}
	go func() { _ = cmd.Wait() }()
	bg := context.Background()
	lmReviewLog(bg).InfoContext(bg, "deep repo review running in background")
	return nil
}
