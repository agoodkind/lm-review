package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"goodkind.io/lm-review/api/reviewpb"
	"goodkind.io/lm-review/internal/daemon"
	"goodkind.io/lm-review/internal/gitutil"
	"goodkind.io/lm-review/internal/github"
	"goodkind.io/lm-review/internal/mcpserver"
)

var log = slog.Default()

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
	var deep bool
	var model string
	cmd := &cobra.Command{
		Use:   "diff",
		Short: "Review staged changes (runs on make build)",
		RunE: func(cmd *cobra.Command, args []string) error {
			repoRoot, err := gitutil.Root("")
			if err != nil {
				log.Info("skipping review: not in a git repo")
				return nil
			}
			diff, err := gitutil.StagedDiff(repoRoot)
			if err != nil {
				return err
			}
			return runReview(cmd.Context(), "diff", diff, repoRoot, deep, model)
		},
	}
	cmd.Flags().BoolVar(&deep, "deep", false, "Use deep model from config")
	cmd.Flags().StringVar(&model, "model", "", "Override model for this request")
	return cmd
}

func newPRCmd() *cobra.Command {
	var deep bool
	var model string
	cmd := &cobra.Command{
		Use:   "pr",
		Short: "Review diff against main branch",
		RunE: func(cmd *cobra.Command, args []string) error {
			repoRoot, err := gitutil.Root("")
			if err != nil {
				return err
			}
			diff, err := gitutil.PRDiff(repoRoot)
			if err != nil {
				return err
			}
			return runReview(cmd.Context(), "pr", diff, repoRoot, deep, model)
		},
	}
	cmd.Flags().BoolVar(&deep, "deep", false, "Use deep model from config")
	cmd.Flags().StringVar(&model, "model", "", "Override model for this request")
	return cmd
}

func newRepoCmd() *cobra.Command {
	var async bool
	var deep bool
	var model string
	cmd := &cobra.Command{
		Use:   "repo",
		Short: "Full repo health review",
		RunE: func(cmd *cobra.Command, args []string) error {
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
				log.Info("skipping review: daemon unavailable", "err", err)
				return nil
			}
			defer client.Close()

			resp, err := client.ReviewRepo(cmd.Context(), files, repoRoot, deep, model)
			if err != nil {
				return err
			}

			printResult(resp)
			if postErr := github.UpsertComment("repo", formatMarkdown("repo", resp)); postErr != nil {
				log.Info("could not post PR comment", "err", postErr)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&async, "async", false, "Run in background, post result when done")
	cmd.Flags().BoolVar(&deep, "deep", false, "Use deep model from config")
	cmd.Flags().StringVar(&model, "model", "", "Override model for this request")
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


func runReview(ctx context.Context, scope, diff, repoPath string, deep bool, model string) error {
	client, err := daemon.Connect(ctx)
	if err != nil {
		log.Info("skipping review: daemon unavailable", "err", err)
		return nil
	}
	defer client.Close()

	var resp *reviewpb.ReviewResponse
	switch scope {
	case "diff":
		resp, err = client.ReviewDiff(ctx, diff, repoPath, deep, model)
	case "pr":
		resp, err = client.ReviewPR(ctx, diff, repoPath, deep, model)
	}
	if err != nil {
		return fmt.Errorf("review: %w", err)
	}

	printResult(resp)

	if postErr := github.UpsertComment(scope, formatMarkdown(scope, resp)); postErr != nil {
		log.Info("could not post PR comment", "err", postErr)
	}

	if resp.Verdict == "block" {
		os.Exit(1)
	}
	return nil
}

func printResult(resp *reviewpb.ReviewResponse) {
	icon := map[string]string{"pass": "✅", "warn": "⚠️", "block": "🚫"}[resp.Verdict]
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
	log.Info("deep repo review running in background")
	return nil
}
