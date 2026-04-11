// Package mcpserver exposes lm-review as an MCP tool server (stdio transport).
// Claude Code connects to this process and can trigger reviews as tools.
package mcpserver

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"goodkind.io/lm-review/api/reviewpb"
	"goodkind.io/lm-review/internal/daemon"
)

// Serve starts the MCP stdio server and blocks until the client disconnects.
func Serve(ctx context.Context) error {
	s := server.NewMCPServer("lm-review", "1.0.0")

	s.AddTool(
		mcp.NewTool("review_diff",
			mcp.WithDescription("Review staged git changes for code quality, style, and correctness."),
			mcp.WithBoolean("deep", mcp.Description("Use the deep model (slower, more thorough).")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			deep := req.GetBool("deep", false)
			diff, err := gitOutput("diff", "--cached")
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("git diff: %v", err)), nil
			}
			return runReview(ctx, diff, deep, func(c *daemon.Client) (*reviewpb.ReviewResponse, error) {
				return c.ReviewDiff(ctx, diff, deep)
			})
		},
	)

	s.AddTool(
		mcp.NewTool("review_pr",
			mcp.WithDescription("Review all changes on the current branch vs main."),
			mcp.WithBoolean("deep", mcp.Description("Use the deep model (slower, more thorough).")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			deep := req.GetBool("deep", false)
			diff, err := prDiff()
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return runReview(ctx, diff, deep, func(c *daemon.Client) (*reviewpb.ReviewResponse, error) {
				return c.ReviewPR(ctx, diff, deep)
			})
		},
	)

	s.AddTool(
		mcp.NewTool("review_repo",
			mcp.WithDescription("Full repository health review: tech debt, structural issues, improvement opportunities."),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			files, err := repoSnapshot()
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return runReview(ctx, files, true, func(c *daemon.Client) (*reviewpb.ReviewResponse, error) {
				return c.ReviewRepo(ctx, files, true)
			})
		},
	)

	return server.ServeStdio(s)
}

func runReview(ctx context.Context, _ string, _ bool, fn func(*daemon.Client) (*reviewpb.ReviewResponse, error)) (*mcp.CallToolResult, error) {
	client, err := daemon.Connect(ctx)
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("lm-review daemon unavailable: %v", err)), nil
	}
	defer client.Close()

	resp, err := fn(client)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
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

func gitOutput(args ...string) (string, error) {
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
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
	const maxBytes = 80_000
	for _, f := range files {
		if f == "" {
			continue
		}
		content, readErr := exec.Command("git", "show", "HEAD:"+f).Output()
		if readErr != nil {
			continue
		}
		entry := fmt.Sprintf("// FILE: %s\n%s\n\n", f, content)
		if sb.Len()+len(entry) > maxBytes {
			fmt.Fprintf(&sb, "// ... truncated at %d bytes\n", maxBytes)
			break
		}
		sb.WriteString(entry)
	}
	return sb.String(), nil
}
