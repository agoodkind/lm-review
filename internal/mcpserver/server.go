// Package mcpserver exposes lm-review as an MCP tool server (stdio transport).
// Claude Code connects to this process and can trigger reviews as tools.
package mcpserver

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"goodkind.io/lm-review/api/reviewpb"
	"goodkind.io/lm-review/internal/daemon"
)

// Serve starts the MCP stdio server and blocks until the client disconnects.
func Serve(ctx context.Context) error {
	s := mcp.NewServer(&mcp.Implementation{Name: "lm-review", Version: "1.0.0"}, nil)

	type DiffInput struct {
		Deep bool `json:"deep" jsonschema:"use the deep model (slower, more thorough)"`
	}
	type PRInput struct {
		Deep bool `json:"deep" jsonschema:"use the deep model (slower, more thorough)"`
	}
	type RepoInput struct{}

	mcp.AddTool(s,
		&mcp.Tool{Name: "review_diff", Description: "Review staged git changes for code quality, style, and correctness."},
		func(ctx context.Context, req *mcp.CallToolRequest, in DiffInput) (*mcp.CallToolResult, ReviewOutput, error) {
			diff, err := gitOutput("diff", "--cached")
			if err != nil {
				return nil, ReviewOutput{}, fmt.Errorf("git diff: %w", err)
			}
			resp, err := callDaemon(ctx, func(c *daemon.Client) (*reviewpb.ReviewResponse, error) {
				return c.ReviewDiff(ctx, diff, in.Deep)
			})
			if err != nil {
				return nil, ReviewOutput{}, err
			}
			out := toOutput(resp)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: out.Markdown}}}, out, nil
		},
	)

	mcp.AddTool(s,
		&mcp.Tool{Name: "review_pr", Description: "Review all changes on the current branch vs main."},
		func(ctx context.Context, req *mcp.CallToolRequest, in PRInput) (*mcp.CallToolResult, ReviewOutput, error) {
			diff, err := prDiff()
			if err != nil {
				return nil, ReviewOutput{}, fmt.Errorf("git diff vs main: %w", err)
			}
			resp, err := callDaemon(ctx, func(c *daemon.Client) (*reviewpb.ReviewResponse, error) {
				return c.ReviewPR(ctx, diff, in.Deep)
			})
			if err != nil {
				return nil, ReviewOutput{}, err
			}
			out := toOutput(resp)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: out.Markdown}}}, out, nil
		},
	)

	mcp.AddTool(s,
		&mcp.Tool{Name: "review_repo", Description: "Full repository health review: tech debt, structural issues, improvement opportunities."},
		func(ctx context.Context, req *mcp.CallToolRequest, in RepoInput) (*mcp.CallToolResult, ReviewOutput, error) {
			files, err := repoSnapshot()
			if err != nil {
				return nil, ReviewOutput{}, fmt.Errorf("repo snapshot: %w", err)
			}
			resp, err := callDaemon(ctx, func(c *daemon.Client) (*reviewpb.ReviewResponse, error) {
				return c.ReviewRepo(ctx, files, true)
			})
			if err != nil {
				return nil, ReviewOutput{}, err
			}
			out := toOutput(resp)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: out.Markdown}}}, out, nil
		},
	)

	if err := s.Run(ctx, &mcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
	return nil
}

func callDaemon(ctx context.Context, fn func(*daemon.Client) (*reviewpb.ReviewResponse, error)) (*reviewpb.ReviewResponse, error) {
	client, err := daemon.Connect(ctx)
	if err != nil {
		return nil, fmt.Errorf("lm-review daemon unavailable: %w", err)
	}
	defer client.Close()
	return fn(client)
}

func toOutput(resp *reviewpb.ReviewResponse) ReviewOutput {
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
	return ReviewOutput{
		Verdict:  resp.Verdict,
		Summary:  resp.Summary,
		Markdown: sb.String(),
	}
}

type ReviewOutput struct {
	Verdict  string `json:"verdict"`
	Summary  string `json:"summary"`
	Markdown string `json:"markdown"`
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
