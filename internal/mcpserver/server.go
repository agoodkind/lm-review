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
			mcp.WithDescription("Review staged git changes for code quality, style, and correctness. Automatically detects the current git repo. Optionally pass path to specify a different repo."),
			mcp.WithBoolean("deep", mcp.Description("Use the deep model (slower, more thorough).")),
			mcp.WithString("path", mcp.Description("Path to git repo root (optional, auto-detected if omitted).")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			deep := req.GetBool("deep", false)
			repoPath, err := resolveRepo(req.GetString("path", ""))
			if err != nil {
				return mcp.NewToolResultText(err.Error()), nil
			}
			diff, err := gitIn(repoPath, "diff", "--cached")
			if err != nil || strings.TrimSpace(diff) == "" {
				return mcp.NewToolResultText("No staged changes to review. Stage files with `git add` first."), nil
			}
			return runReview(ctx, diff, deep, func(c *daemon.Client) (*reviewpb.ReviewResponse, error) {
				return c.ReviewDiff(ctx, diff, deep)
			})
		},
	)

	s.AddTool(
		mcp.NewTool("review_pr",
			mcp.WithDescription("Review all changes on the current branch vs main. Automatically detects the current git repo."),
			mcp.WithBoolean("deep", mcp.Description("Use the deep model (slower, more thorough).")),
			mcp.WithString("path", mcp.Description("Path to git repo root (optional, auto-detected if omitted).")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			deep := req.GetBool("deep", false)
			repoPath, err := resolveRepo(req.GetString("path", ""))
			if err != nil {
				return mcp.NewToolResultText(err.Error()), nil
			}
			diff, err := prDiffIn(repoPath)
			if err != nil || strings.TrimSpace(diff) == "" {
				return mcp.NewToolResultText("No changes vs main branch, or main branch not found."), nil
			}
			return runReview(ctx, diff, deep, func(c *daemon.Client) (*reviewpb.ReviewResponse, error) {
				return c.ReviewPR(ctx, diff, deep)
			})
		},
	)

	s.AddTool(
		mcp.NewTool("review_repo",
			mcp.WithDescription("Full repository health review: tech debt, structural issues, improvement opportunities. Automatically detects the current git repo."),
			mcp.WithString("path", mcp.Description("Path to git repo root (optional, auto-detected if omitted).")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			repoPath, err := resolveRepo(req.GetString("path", ""))
			if err != nil {
				return mcp.NewToolResultText(err.Error()), nil
			}
			files, err := repoSnapshotIn(repoPath)
			if err != nil || strings.TrimSpace(files) == "" {
				return mcp.NewToolResultText("No Go files found in repo."), nil
			}
			return runReview(ctx, files, true, func(c *daemon.Client) (*reviewpb.ReviewResponse, error) {
				return c.ReviewRepo(ctx, files, true)
			})
		},
	)

	return server.ServeStdio(s)
}

// resolveRepo returns the git repo root to use. If explicit is non-empty it's
// used directly. Otherwise we auto-detect from CWD via git rev-parse.
func resolveRepo(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", fmt.Errorf("not in a git repo and no path provided. Pass path='/path/to/repo' to specify one")
	}
	return strings.TrimSpace(string(out)), nil
}

func runReview(ctx context.Context, content string, _ bool, fn func(*daemon.Client) (*reviewpb.ReviewResponse, error)) (*mcp.CallToolResult, error) {
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

func gitIn(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	return string(out), err
}

func prDiffIn(dir string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "diff", "main...HEAD").Output()
	if err != nil {
		out, err = exec.Command("git", "-C", dir, "diff", "origin/main...HEAD").Output()
		if err != nil {
			return "", fmt.Errorf("git diff vs main: %w", err)
		}
	}
	return string(out), nil
}

func repoSnapshotIn(dir string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "ls-files", "*.go").Output()
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
		content, readErr := exec.Command("git", "-C", dir, "show", "HEAD:"+f).Output()
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
