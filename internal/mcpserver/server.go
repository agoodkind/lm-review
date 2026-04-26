// Package mcpserver exposes lm-review as an MCP tool server (stdio transport).
// Claude Code connects to this process and can trigger reviews as tools.
package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"goodkind.io/lm-review/api/reviewpb"
	"goodkind.io/lm-review/internal/daemon"
	"goodkind.io/lm-review/internal/gitutil"
	"goodkind.io/lm-review/internal/review"
	"goodkind.io/lm-review/internal/version"
)

// No client-side truncation; the daemon handles chunking based on context_length.

// Serve starts the MCP stdio server and blocks until the client disconnects.
func Serve(ctx context.Context) error {
	s := server.NewMCPServer("lm-review", version.Version)

	// --- Resources ---

	s.AddResource(
		mcp.Resource{
			URI:         "lm-review://getting-started",
			Name:        "Getting Started with lm-review",
			Description: "Overview of lm-review: what it does, available tools, and how to use them.",
			MIMEType:    "text/markdown",
		},
		func(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
			return []mcp.ResourceContents{
				mcp.TextResourceContents{
					URI:      "lm-review://getting-started",
					MIMEType: "text/markdown",
					Text: `# lm-review

Local LLM code review tool powered by LM Studio.

## Tools

### review_diff
Reviews staged git changes (` + "`git add`" + ` first). Best for pre-commit checks.

### review_pr
Reviews all changes on the current branch vs main. Best for PR readiness.

### review_repo
Full repository health review. Scans all Go source files for tech debt, security issues, and structural problems. Large repos are automatically chunked.

### review_static
Runs deterministic static analysis with go vet, staticcheck, custom analyzers, and optional semgrep. It can return raw findings or synthesize them through the LLM.

## Prompts

### run_review
Interactive review launcher. Picks the right scope and depth based on your intent. Arguments:
- **scope**: "diff", "pr", or "repo"
- **deep**: "true" for the deep model (slower, more thorough), "false" for fast model

## Depth

- **quick**: Security and correctness only. Fastest model, minimal output. Best for build hooks.
- **normal** (default): Full rules, fast model. Best for everyday diffs and PRs.
- **deep**: Full rules, larger model. More thorough analysis for critical PRs.
- **ultra**: Two-pass pipeline. Fast model sweeps for issues, then the largest model verifies each one to filter false positives. Most thorough but slowest.

## Configuration

Config lives at ` + "`~/.config/lm-review/config.toml`" + `. Key settings:
- ` + "`quick_model`" + ` / ` + "`fast_model`" + ` / ` + "`deep_model`" + ` / ` + "`ultra_model`" + `: which LM Studio models to use
- ` + "`context_length`" + `: token context window for model loading
- ` + "`max_response_tokens`" + `: cap on response length
- ` + "`[[rules]]`" + `: custom review rules with optional glob filters
- ` + "`[static_review]`" + `: deterministic analyzer stack defaults

Project-local rules can be added via ` + "`.lm-review.toml`" + ` in the repo root.
`,
				},
			}, nil
		},
	)

	// --- Prompts ---

	s.AddPrompt(
		mcp.Prompt{
			Name:        "getting_started",
			Description: "Get started with lm-review. Explains available tools, how to pick scope and depth, and runs an initial review.",
		},
		func(ctx context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			return &mcp.GetPromptResult{
				Description: "lm-review onboarding",
				Messages: []mcp.PromptMessage{
					{
						Role: mcp.RoleUser,
						Content: mcp.NewTextContent(`You have access to lm-review, a local LLM code review tool powered by LM Studio.

Available tools:
- review_diff: Reviews staged git changes. Run "git add" first, then call this tool. Best for pre-commit checks.
- review_pr: Reviews all changes on the current branch vs main. Best for PR readiness.
- review_repo: Full repository health review. Scans all source files for tech debt, security issues, and structural problems.
- review_static: Runs deterministic static analysis with go vet, staticcheck, custom analyzers, and optional semgrep. It supports raw analyzer findings or synthesized review output.

Each tool accepts:
- depth (string): "quick" (security+correctness only), "normal" (default), "deep" (larger model), "ultra" (two-pass verification).
- model (string): Override the model for this request.
- path (string): Path to git repo root. Auto-detected if omitted.

Start by checking if there are staged changes with "git diff --cached --stat". If there are, run review_diff. If not, check if the current branch has commits ahead of main. If so, run review_pr. Otherwise, offer to run review_repo for a full health check.`),
					},
				},
			}, nil
		},
	)

	// --- Tools ---

	modelFlag := mcp.WithString("model",
		mcp.Description("Override the model for this request (e.g. 'qwen/qwen3-coder-next'). Uses config default if omitted."),
	)

	depthFlag := mcp.WithString("depth",
		mcp.Description("Review depth: quick (security+correctness only), normal (default), deep (larger model), ultra (two-pass verification with largest model)."),
	)

	s.AddTool(
		mcp.NewTool("review_diff",
			mcp.WithDescription("Review staged git changes for code quality, style, and correctness."),
			depthFlag,
			mcp.WithString("path", mcp.Description("Path to git repo root (optional, auto-detected if omitted).")),
			modelFlag,
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			depth := req.GetString("depth", "normal")
			model := req.GetString("model", "")
			repoRoot, err := gitutil.Root(req.GetString("path", ""))
			if err != nil {
				return mcp.NewToolResultText(err.Error()), nil
			}
			diff, err := gitutil.StagedDiff(repoRoot)
			if err != nil || strings.TrimSpace(diff) == "" {
				return mcp.NewToolResultText("No staged changes to review. Stage files with `git add` first."), nil
			}
			return callDaemon(ctx, func(c *daemon.Client) (*reviewpb.ReviewResponse, error) {
				return c.ReviewDiff(ctx, diff, repoRoot, depth, model)
			})
		},
	)

	s.AddTool(
		mcp.NewTool("review_pr",
			mcp.WithDescription("Review all changes on the current branch vs main."),
			depthFlag,
			mcp.WithString("path", mcp.Description("Path to git repo root (optional, auto-detected if omitted).")),
			modelFlag,
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			depth := req.GetString("depth", "normal")
			model := req.GetString("model", "")
			repoRoot, err := gitutil.Root(req.GetString("path", ""))
			if err != nil {
				return mcp.NewToolResultText(err.Error()), nil
			}
			diff, err := gitutil.PRDiff(repoRoot)
			if err != nil || strings.TrimSpace(diff) == "" {
				return mcp.NewToolResultText("No changes vs main branch, or main branch not found."), nil
			}
			return callDaemon(ctx, func(c *daemon.Client) (*reviewpb.ReviewResponse, error) {
				return c.ReviewPR(ctx, diff, repoRoot, depth, model)
			})
		},
	)

	s.AddTool(
		mcp.NewTool("review_repo",
			mcp.WithDescription("Full repository health review: tech debt, structural issues, improvement opportunities."),
			depthFlag,
			mcp.WithString("path", mcp.Description("Path to git repo root (optional, auto-detected if omitted).")),
			modelFlag,
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			depth := req.GetString("depth", "normal")
			model := req.GetString("model", "")
			repoRoot, err := gitutil.Root(req.GetString("path", ""))
			if err != nil {
				return mcp.NewToolResultText(err.Error()), nil
			}
			files, err := gitutil.RepoSnapshot(repoRoot, 0)
			if err != nil || strings.TrimSpace(files) == "" {
				return mcp.NewToolResultText("No Go files found in repo."), nil
			}
			return callDaemon(ctx, func(c *daemon.Client) (*reviewpb.ReviewResponse, error) {
				return c.ReviewRepo(ctx, files, repoRoot, depth, model)
			})
		},
	)

	s.AddTool(
		mcp.NewTool("review_static",
			mcp.WithDescription("Run deterministic static analysis across the repo or changed Go files. Supports raw findings or LLM-synthesized review output."),
			mcp.WithString("scope", mcp.Description("Analysis scope: diff, pr, or repo.")),
			depthFlag,
			mcp.WithBoolean("synthesize", mcp.Description("When true, synthesize analyzer findings with the LLM. When false, return raw deterministic findings only.")),
			mcp.WithString("path", mcp.Description("Path to git repo root (optional, auto-detected if omitted).")),
			mcp.WithArray("disabled_sources", mcp.Description("Optional source opt-out list: vet, staticcheck, custom, semgrep."), mcp.Items(map[string]any{"type": "string"})),
			mcp.WithArray("enabled_checks", mcp.Description("Optional exact check allowlist, such as SA4006 or slog_error_without_err."), mcp.Items(map[string]any{"type": "string"})),
			modelFlag,
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			repoRoot, err := gitutil.Root(req.GetString("path", ""))
			if err != nil {
				return mcp.NewToolResultText(err.Error()), nil
			}
			scope := req.GetString("scope", "repo")
			selectedFiles, err := staticFilesForScope(repoRoot, scope)
			if err != nil {
				return mcp.NewToolResultText(err.Error()), nil
			}
			return callDaemon(ctx, func(c *daemon.Client) (*reviewpb.ReviewResponse, error) {
				return c.ReviewStatic(ctx, &reviewpb.StaticReviewRequest{
					Path:            repoRoot,
					Files:           selectedFiles,
					DisabledSources: req.GetStringSlice("disabled_sources", []string{}),
					EnabledChecks:   req.GetStringSlice("enabled_checks", []string{}),
					Synthesize:      req.GetBool("synthesize", true),
					Depth:           req.GetString("depth", "normal"),
					Model:           req.GetString("model", ""),
				})
			})
		},
	)

	return server.ServeStdio(s)
}

func callDaemon(ctx context.Context, fn func(*daemon.Client) (*reviewpb.ReviewResponse, error)) (*mcp.CallToolResult, error) {
	client, err := daemon.Connect(ctx)
	if err != nil {
		return mcp.NewToolResultText(fmt.Sprintf("lm-review daemon unavailable (is LM Studio running?): %v", err)), nil
	}
	defer client.Close()

	resp, err := fn(client)
	if err != nil {
		return mcp.NewToolResultText(fmt.Sprintf("Review failed: %v", err)), nil
	}

	return mcp.NewToolResultText(formatResponse(resp)), nil
}

func formatResponse(resp *reviewpb.ReviewResponse) string {
	icon := map[string]string{"pass": "PASS", "warn": "WARN", "block": "BLOCK", "skip": "SKIP"}[resp.Verdict]
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s (%s, %dms): %s", icon, resp.Model, resp.LatencyMs, resp.Summary)
	if len(resp.Issues) > 0 {
		sb.WriteString("\n\n| Severity | File | Line | Rule | Message |\n|---|---|---|---|---|\n")
		for _, issue := range resp.Issues {
			fmt.Fprintf(&sb, "| %s | `%s` | %d | %s | %s |\n", issue.Severity, issue.File, issue.Line, issue.Rule, issue.Message)
		}
	}
	return sb.String()
}

func staticFilesForScope(repoRoot string, scope string) ([]string, error) {
	switch scope {
	case "diff":
		diff, err := gitutil.StagedDiff(repoRoot)
		if err != nil {
			return nil, fmt.Errorf("load staged diff: %w", err)
		}
		return goFilesOnly(review.FilesFromDiff(diff)), nil
	case "pr":
		diff, err := gitutil.PRDiff(repoRoot)
		if err != nil {
			return nil, fmt.Errorf("load PR diff: %w", err)
		}
		return goFilesOnly(review.FilesFromDiff(diff)), nil
	case "repo", "":
		return nil, nil
	default:
		return nil, fmt.Errorf("unknown static scope %q (expected diff, pr, or repo)", scope)
	}
}

func goFilesOnly(files []string) []string {
	filtered := make([]string, 0, len(files))
	for _, file := range files {
		if strings.HasSuffix(file, ".go") {
			filtered = append(filtered, file)
		}
	}
	return filtered
}
