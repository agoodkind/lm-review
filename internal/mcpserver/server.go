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
			Name:        "run_review",
			Description: "Run an lm-review code review. Picks scope (diff, pr, repo) and depth (fast or deep).",
			Arguments: []mcp.PromptArgument{
				{
					Name:        "scope",
					Description: "What to review: 'diff' (staged changes), 'pr' (branch vs main), or 'repo' (full repository).",
					Required:    true,
				},
				{
					Name:        "deep",
					Description: "Use the deep model for more thorough analysis. 'true' or 'false' (default: false).",
					Required:    false,
				},
			},
		},
		func(ctx context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			scope := req.Params.Arguments["scope"]
			deep := req.Params.Arguments["deep"] == "true"

			var description string
			switch scope {
			case "diff":
				if deep {
					description = "Deep review of staged changes"
				} else {
					description = "Quick review of staged changes"
				}
			case "pr":
				if deep {
					description = "Deep review of PR (branch vs main)"
				} else {
					description = "Quick review of PR (branch vs main)"
				}
			case "repo":
				if deep {
					description = "Deep full repository health review"
				} else {
					description = "Full repository health review"
				}
			default:
				scope = "diff"
				description = "Quick review of staged changes"
			}

			deepStr := "false"
			if deep {
				deepStr = "true"
			}

			return &mcp.GetPromptResult{
				Description: description,
				Messages: []mcp.PromptMessage{
					{
						Role: mcp.RoleUser,
						Content: mcp.NewTextContent(fmt.Sprintf(
							"Run an lm-review %s review with deep=%s. Use the review_%s tool.",
							scope, deepStr, scope,
						)),
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
