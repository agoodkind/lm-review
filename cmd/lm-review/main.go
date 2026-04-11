package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"goodkind.io/lm-review/api/reviewpb"
	"goodkind.io/lm-review/internal/daemon"
	"goodkind.io/lm-review/internal/mcpserver"
)

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
	cmd := &cobra.Command{
		Use:   "diff",
		Short: "Review staged changes (runs on make build)",
		RunE: func(cmd *cobra.Command, args []string) error {
			diff, err := gitOutput("diff", "--cached")
			if err != nil {
				return err
			}
			return runReview(cmd.Context(), "diff", diff, deep)
		},
	}
	cmd.Flags().BoolVar(&deep, "deep", false, "Use deep model")
	return cmd
}

func newPRCmd() *cobra.Command {
	var deep bool
	cmd := &cobra.Command{
		Use:   "pr",
		Short: "Review diff against main branch",
		RunE: func(cmd *cobra.Command, args []string) error {
			diff, err := prDiff()
			if err != nil {
				return err
			}
			return runReview(cmd.Context(), "pr", diff, deep)
		},
	}
	cmd.Flags().BoolVar(&deep, "deep", false, "Use deep model")
	return cmd
}

func newRepoCmd() *cobra.Command {
	var async bool
	cmd := &cobra.Command{
		Use:   "repo",
		Short: "Full repo health review",
		RunE: func(cmd *cobra.Command, args []string) error {
			if async {
				return runRepoAsync()
			}
			files, err := repoSnapshot()
			if err != nil {
				return err
			}

			client, err := daemon.Connect(cmd.Context())
			if err != nil {
				fmt.Fprintln(os.Stderr, skipMsg(err))
				return nil
			}
			defer client.Close()

			resp, err := client.ReviewRepo(cmd.Context(), files, true)
			if err != nil {
				return err
			}

			printResult(resp)
			return postToGitHub("repo", resp)
		},
	}
	cmd.Flags().BoolVar(&async, "async", false, "Run in background, post result when done")
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

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Create default config at XDG config path",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInit()
		},
	}
}

func runReview(ctx context.Context, scope, diff string, deep bool) error {
	client, err := daemon.Connect(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, skipMsg(err))
		return nil
	}
	defer client.Close()

	var resp *reviewpb.ReviewResponse
	switch scope {
	case "diff":
		resp, err = client.ReviewDiff(ctx, diff, deep)
	case "pr":
		resp, err = client.ReviewPR(ctx, diff, deep)
	}
	if err != nil {
		return fmt.Errorf("review: %w", err)
	}

	printResult(resp)

	if err := postToGitHub(scope, resp); err != nil {
		fmt.Fprintf(os.Stderr, "lm-review: could not post PR comment: %v\n", err)
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

func skipMsg(err error) string {
	return fmt.Sprintf("lm-review: skipping (daemon unavailable: %v)", err)
}

func runInit() error {
	fmt.Println("lm-review init: run 'lm-review mcp' to start the MCP server,")
	fmt.Println("or add it to ~/.claude/mcp.json:")
	fmt.Printf(`
{
  "mcpServers": {
    "lm-review": {
      "command": "%s",
      "args": ["mcp"]
    }
  }
}
`, mustExecPath())
	return nil
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
	fmt.Fprintln(os.Stderr, "lm-review: deep repo review running in background")
	return nil
}

func mustExecPath() string {
	p, _ := os.Executable()
	return p
}
