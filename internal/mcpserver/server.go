// Package mcpserver exposes lm-review as an MCP tool server (stdio transport).
// Claude Code connects to this process and can trigger reviews as tools.
package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"goodkind.io/gklog"
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

	addGettingStartedResource(s)
	addGettingStartedPrompt(s)

	modelFlag := mcp.WithString("model",
		mcp.Description("Override the model for this request (e.g. 'qwen/qwen3-coder-next'). Uses config default if omitted."),
	)

	depthFlag := mcp.WithString("depth",
		mcp.Description("Review depth: quick (security+correctness only), normal (default), deep (larger model), ultra (two-pass verification with largest model)."),
	)

	addReviewTool(s, depthFlag, modelFlag)
	addStaticTool(s, depthFlag, modelFlag)

	return server.ServeStdio(s)
}

func addGettingStartedResource(s *server.MCPServer) {
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

### review
Path-based review launcher. The daemon loads staged, worktree, PR, or repo input from the git path and chunks large inputs automatically.

### review_static
Runs deterministic static analysis with go vet, staticcheck, custom analyzers, and optional semgrep. It can return raw findings or synthesize them through the LLM.

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
}

func addGettingStartedPrompt(s *server.MCPServer) {
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
- review: Path-based review launcher. Defaults to auto mode: staged diff, worktree diff, PR diff, then repo snapshot.
- review_static: Runs deterministic static analysis with go vet, staticcheck, custom analyzers, and optional semgrep. It supports raw analyzer findings or synthesized review output.

Each tool accepts:
- mode (string): "auto" (default), "staged", "worktree", "pr", or "repo".
- depth (string): "quick" (security+correctness only), "normal" (default), "deep" (larger model), "ultra" (two-pass verification).
- model (string): Override the model for this request.
- path (string): Path to git repo root. Auto-detected if omitted.

Start with review in auto mode unless the user explicitly requests a narrower mode.`),
					},
				},
			}, nil
		},
	)
}

func addReviewTool(s *server.MCPServer, depthFlag mcp.ToolOption, modelFlag mcp.ToolOption) {
	s.AddTool(
		mcp.NewTool("review",
			mcp.WithDescription("Path-based LLM review. Auto mode reviews staged, worktree, PR, then repo input without sending source over gRPC."),
			mcp.WithString("mode", mcp.Description("Review mode: auto (default), staged, worktree, pr, or repo.")),
			depthFlag,
			mcp.WithString("base_ref", mcp.Description("Optional base ref for PR mode.")),
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
			mode, err := parseReviewMode(req.GetString("mode", "auto"))
			if err != nil {
				return mcp.NewToolResultText(err.Error()), nil
			}
			return callDaemon(ctx, func(c *daemon.Client) (*reviewpb.ReviewResponse, error) {
				return c.Review(ctx, mode, repoRoot, depth, model, req.GetString("base_ref", ""))
			})
		},
	)
}

func addStaticTool(s *server.MCPServer, depthFlag mcp.ToolOption, modelFlag mcp.ToolOption) {
	s.AddTool(
		mcp.NewTool("review_static",
			mcp.WithDescription("Run deterministic static analysis across the repo or changed Go files. Supports raw findings or LLM-synthesized review output."),
			mcp.WithString("scope", mcp.Description("Analysis scope: diff, pr, or repo.")),
			depthFlag,
			mcp.WithBoolean("synthesize", mcp.Description("When true, synthesize analyzer findings with the LLM. When false, return raw deterministic findings only.")),
			mcp.WithString("path", mcp.Description("Path to git repo root (optional, auto-detected if omitted).")),
			mcp.WithArray("disabled_sources", mcp.Description("Optional source opt-out list: vet, staticcheck, custom, semgrep."), mcp.WithStringItems()),
			mcp.WithArray("enabled_checks", mcp.Description("Optional exact check allowlist, such as SA4006 or slog_error_without_err."), mcp.WithStringItems()),
			modelFlag,
		),
		callStaticReview,
	)
}

func callStaticReview(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	repoRoot, err := gitutil.Root(req.GetString("path", ""))
	if err != nil {
		return mcp.NewToolResultText(err.Error()), nil
	}
	scope := req.GetString("scope", "repo")
	selectedFiles, err := staticFilesForScope(ctx, repoRoot, scope)
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

type staticScopeName string

const (
	staticScopeNameEmpty staticScopeName = ""
	staticScopeNameDiff  staticScopeName = "diff"
	staticScopeNamePR    staticScopeName = "pr"
	staticScopeNameRepo  staticScopeName = "repo"
)

func staticFilesForScope(ctx context.Context, repoRoot string, scope string) ([]string, error) {
	log := gklog.LoggerFromContext(ctx).With("component", "lm-review", "subcomponent", "mcp")
	switch staticScopeName(scope) {
	case staticScopeNameDiff:
		diff, err := gitutil.StagedDiff(repoRoot)
		if err != nil {
			log.ErrorContext(ctx, "mcp.static_files.staged_diff_failed", "err", err)
			return nil, fmt.Errorf("load staged diff: %w", err)
		}
		return goFilesOnly(review.FilesFromDiff(diff)), nil
	case staticScopeNamePR:
		diff, err := gitutil.PRDiff(repoRoot, "")
		if err != nil {
			log.ErrorContext(ctx, "mcp.static_files.pr_diff_failed", "err", err)
			return nil, fmt.Errorf("load PR diff: %w", err)
		}
		return goFilesOnly(review.FilesFromDiff(diff)), nil
	case staticScopeNameRepo, staticScopeNameEmpty:
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
