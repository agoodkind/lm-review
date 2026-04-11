package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"goodkind.io/lm-review/internal/config"
	"goodkind.io/lm-review/internal/github"
	"goodkind.io/lm-review/internal/lmstudio"
	"goodkind.io/lm-review/internal/review"
)

func main() {
	root := &cobra.Command{
		Use:   "lm-review",
		Short: "LLM-powered local code review",
	}

	root.AddCommand(newDiffCmd())
	root.AddCommand(newPRCmd())
	root.AddCommand(newRepoCmd())
	root.AddCommand(newInitCmd())

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

func newDiffCmd() *cobra.Command {
	var deep bool

	cmd := &cobra.Command{
		Use:   "diff",
		Short: "Review staged diff (runs on make build)",
		RunE: func(cmd *cobra.Command, args []string) error {
			diff, err := stagedDiff()
			if err != nil {
				return err
			}

			return run(cmd.Context(), "diff", diff, deep)
		},
	}

	cmd.Flags().BoolVar(&deep, "deep", false, "Use deep model instead of fast model")
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

			return run(cmd.Context(), "pr", diff, deep)
		},
	}

	cmd.Flags().BoolVar(&deep, "deep", false, "Use deep model instead of fast model")
	return cmd
}

func newRepoCmd() *cobra.Command {
	var async bool

	cmd := &cobra.Command{
		Use:   "repo",
		Short: "Full repo review (run occasionally for debt cleanup)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if async {
				return runAsync()
			}

			files, err := repoSnapshot()
			if err != nil {
				return err
			}

			return runRepo(cmd.Context(), files)
		},
	}

	cmd.Flags().BoolVar(&async, "async", false, "Run in background and post result when done")
	return cmd
}

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Create default config at XDG config path",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := config.WriteDefault(""); err != nil {
				return err
			}

			fmt.Printf("Created config at %s\n", config.ConfigPath())
			fmt.Println("Set your LM Studio token in the config file.")
			return nil
		},
	}
}

func run(ctx context.Context, scope, diff string, deep bool) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	client := lmstudio.New(cfg.LMStudio.URL, cfg.LMStudio.Token)

	if err := client.Ping(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "lm-review: skipping (LM Studio unavailable: %v)\n", err)
		return nil
	}

	model := cfg.LMStudio.FastModel
	if deep {
		model = cfg.LMStudio.DeepModel
	}

	r := review.New(client, model, scope)

	result, err := r.ReviewDiff(ctx, diff)
	if err != nil {
		return fmt.Errorf("review: %w", err)
	}

	printResult(result)

	if err := github.UpsertComment(scope, result.Markdown()); err != nil {
		fmt.Fprintf(os.Stderr, "lm-review: could not post PR comment: %v\n", err)
	}

	os.Exit(result.ExitCode())
	return nil
}

func runRepo(ctx context.Context, files string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	client := lmstudio.New(cfg.LMStudio.URL, cfg.LMStudio.Token)
	r := review.New(client, cfg.LMStudio.DeepModel, "repo")

	result, err := r.ReviewRepo(ctx, files)
	if err != nil {
		return fmt.Errorf("review: %w", err)
	}

	printResult(result)

	if err := github.UpsertComment("repo", result.Markdown()); err != nil {
		fmt.Fprintf(os.Stderr, "lm-review: could not post PR comment: %v\n", err)
	}

	return nil
}

func runAsync() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}

	cmd := exec.Command(self, "repo")
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start async review: %w", err)
	}

	go func() { _ = cmd.Wait() }()
	fmt.Fprintln(os.Stderr, "lm-review: deep repo review running in background")
	return nil
}

func printResult(r *review.Result) {
	icon := map[review.Verdict]string{
		review.VerdictPass:  "✅",
		review.VerdictWarn:  "⚠️",
		review.VerdictBlock: "🚫",
	}[r.Verdict]

	fmt.Fprintf(os.Stderr, "\nlm-review [%s] %s %s: %s\n", r.Model, icon, strings.ToUpper(string(r.Verdict)), r.Summary)

	for _, issue := range r.Issues {
		fmt.Fprintf(os.Stderr, "  %s:%d [%s] %s\n", issue.File, issue.Line, issue.Rule, issue.Message)
	}

	fmt.Fprintln(os.Stderr)
}

func stagedDiff() (string, error) {
	out, err := exec.Command("git", "diff", "--cached").Output()
	if err != nil {
		return "", fmt.Errorf("git diff --cached: %w", err)
	}

	return string(out), nil
}

func prDiff() (string, error) {
	out, err := exec.Command("git", "diff", "main...HEAD").Output()
	if err != nil {
		out, err = exec.Command("git", "diff", "origin/main...HEAD").Output()
		if err != nil {
			return "", fmt.Errorf("git diff vs main: %w", err)
		}
	}

	return string(out), nil
}

func repoSnapshot() (string, error) {
	out, err := exec.Command("git", "ls-files", "*.go").Output()
	if err != nil {
		return "", fmt.Errorf("git ls-files: %w", err)
	}

	files := strings.Split(strings.TrimSpace(string(out)), "\n")
	var sb strings.Builder

	for _, f := range files {
		if f == "" {
			continue
		}

		content, err := os.ReadFile(f)
		if err != nil {
			continue
		}

		fmt.Fprintf(&sb, "// FILE: %s\n%s\n\n", f, content)
	}

	return sb.String(), nil
}
