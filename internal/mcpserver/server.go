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
)

// No client-side truncation; the daemon handles chunking based on context_length.

// Serve starts the MCP stdio server and blocks until the client disconnects.
func Serve(ctx context.Context) error {
	s := server.NewMCPServer("lm-review", "1.0.0",
		server.WithResourceCapabilities(true, false),
		server.WithPromptCapabilities(true),
	)

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

## Prompts

### run_review
Interactive review launcher. Picks the right scope and depth based on your intent. Arguments:
- **scope**: "diff", "pr", or "repo"
- **deep**: "true" for the deep model (slower, more thorough), "false" for fast model

## Depth

- **fast** (default): Uses the fast model for quick feedback on diffs and PRs.
- **deep**: Uses a larger model for thorough analysis. Best for full repo reviews or critical PRs.

## Configuration

Config lives at ` + "`~/.config/lm-review/config.toml`" + `. Key settings:
- ` + "`fast_model`" + ` / ` + "`deep_model`" + `: which LM Studio models to use
- ` + "`context_length`" + `: token context window for model loading
- ` + "`max_response_tokens`" + `: cap on response length
- ` + "`[[rules]]`" + `: custom review rules with optional glob filters

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

Each tool accepts:
- deep (bool): Use the deep model for more thorough analysis. Slower but catches more.
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

	s.AddTool(
		mcp.NewTool("review_diff",
			mcp.WithDescription("Review staged git changes for code quality, style, and correctness."),
			mcp.WithBoolean("deep", mcp.Description("Use the deep model from config (or pass model= to specify any model).")),
			mcp.WithString("path", mcp.Description("Path to git repo root (optional, auto-detected if omitted).")),
			modelFlag,
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			deep := req.GetBool("deep", false)
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
				return c.ReviewDiff(ctx, diff, repoRoot, deep, model)
			})
		},
	)

	s.AddTool(
		mcp.NewTool("review_pr",
			mcp.WithDescription("Review all changes on the current branch vs main."),
			mcp.WithBoolean("deep", mcp.Description("Use the deep model from config (or pass model= to specify any model).")),
			mcp.WithString("path", mcp.Description("Path to git repo root (optional, auto-detected if omitted).")),
			modelFlag,
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			deep := req.GetBool("deep", false)
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
				return c.ReviewPR(ctx, diff, repoRoot, deep, model)
			})
		},
	)

	s.AddTool(
		mcp.NewTool("review_repo",
			mcp.WithDescription("Full repository health review: tech debt, structural issues, improvement opportunities."),
			mcp.WithBoolean("deep", mcp.Description("Use the deep model from config (or pass model= to specify any model). Defaults to false - uses repo model from config.")),
			mcp.WithString("path", mcp.Description("Path to git repo root (optional, auto-detected if omitted).")),
			modelFlag,
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			deep := req.GetBool("deep", false)
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
				return c.ReviewRepo(ctx, files, repoRoot, deep, model)
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
	icon := map[string]string{"pass": "✅", "warn": "⚠️", "block": "🚫"}[resp.Verdict]
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s **%s** (%s, %dms): %s", icon, strings.ToUpper(resp.Verdict), resp.Model, resp.LatencyMs, resp.Summary)
	if len(resp.Issues) > 0 {
		sb.WriteString("\n\n| Severity | File | Line | Rule | Message |\n|---|---|---|---|---|\n")
		for _, issue := range resp.Issues {
			sevIcon := map[string]string{"error": "🚫", "warning": "⚠️", "info": "ℹ️"}[issue.Severity]
			fmt.Fprintf(&sb, "| %s | `%s` | %d | %s | %s |\n", sevIcon, issue.File, issue.Line, issue.Rule, issue.Message)
		}
	}
	return sb.String()
}
