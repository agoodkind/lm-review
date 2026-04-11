// Package mcpserver exposes lm-review as an MCP tool server (stdio transport).
// Claude Code connects to this process and can trigger reviews as tools.
package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"goodkind.io/lm-review/api/reviewpb"
	"goodkind.io/lm-review/internal/daemon"
)

// Serve starts the MCP stdio server and blocks until the client disconnects.
func Serve(ctx context.Context) error {
	server := mcp.NewServer(&mcp.Implementation{Name: "lm-review", Version: "1.0.0"}, nil)

	server.AddTool(
		&mcp.Tool{
			Name:        "review_diff",
			Description: "Review staged git changes for code quality, style, and correctness.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"deep": {"type": "boolean", "description": "Use the deep model (slower, more thorough)"}
				}
			}`),
		},
		handleReviewDiff,
	)

	server.AddTool(
		&mcp.Tool{
			Name:        "review_pr",
			Description: "Review all changes on the current branch vs main.",
			InputSchema: json.RawMessage(`{
				"type": "object",
				"properties": {
					"deep": {"type": "boolean", "description": "Use the deep model (slower, more thorough)"}
				}
			}`),
		},
		handleReviewPR,
	)

	server.AddTool(
		&mcp.Tool{
			Name:        "review_repo",
			Description: "Full repository health review: tech debt, structural issues, improvement opportunities.",
			InputSchema: json.RawMessage(`{"type": "object", "properties": {}}`),
		},
		handleReviewRepo,
	)

	ss, err := server.Connect(ctx, &mcp.StdioTransport{}, nil)
	if err != nil {
		return fmt.Errorf("mcp connect: %w", err)
	}

	return ss.Wait()
}

func handleReviewDiff(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Deep bool `json:"deep"`
	}
	_ = json.Unmarshal(req.Params.Arguments, &args)

	diff, err := gitOutput("diff", "--cached")
	if err != nil {
		return errResult(fmt.Sprintf("git diff --cached: %v", err)), nil
	}

	return callDaemon(ctx, func(c *daemon.Client) (*reviewpb.ReviewResponse, error) {
		return c.ReviewDiff(ctx, diff, args.Deep)
	})
}

func handleReviewPR(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var args struct {
		Deep bool `json:"deep"`
	}
	_ = json.Unmarshal(req.Params.Arguments, &args)

	diff, err := prDiff()
	if err != nil {
		return errResult(err.Error()), nil
	}

	return callDaemon(ctx, func(c *daemon.Client) (*reviewpb.ReviewResponse, error) {
		return c.ReviewPR(ctx, diff, args.Deep)
	})
}

func handleReviewRepo(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	files, err := repoSnapshot()
	if err != nil {
		return errResult(err.Error()), nil
	}

	return callDaemon(ctx, func(c *daemon.Client) (*reviewpb.ReviewResponse, error) {
		return c.ReviewRepo(ctx, files, true)
	})
}

func callDaemon(ctx context.Context, fn func(*daemon.Client) (*reviewpb.ReviewResponse, error)) (*mcp.CallToolResult, error) {
	client, err := daemon.Connect(ctx)
	if err != nil {
		return errResult(fmt.Sprintf("lm-review daemon unavailable: %v", err)), nil
	}
	defer client.Close()

	resp, err := fn(client)
	if err != nil {
		return errResult(err.Error()), nil
	}

	return okResult(resp), nil
}

func okResult(resp *reviewpb.ReviewResponse) *mcp.CallToolResult {
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

	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: sb.String()}},
	}
}

func errResult(msg string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: "❌ " + msg}},
		IsError: true,
	}
}

func gitOutput(args ...string) (string, error) {
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return "", err
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
