// Package claude implements a ChatClient that shells out to the claude CLI.
package claude

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// Client calls `claude -p` for LLM inference using the user's Claude subscription.
type Client struct {
	model string
}

// New creates a Claude CLI client. model is passed as --model (e.g. "opus", "sonnet").
func New(model string) *Client {
	return &Client{model: model}
}

// Chat sends a system prompt and user message via `claude -p` and returns the response.
func (c *Client) Chat(ctx context.Context, systemPrompt, userMessage string) (string, error) {
	args := []string{
		"-p",
		"--system-prompt", systemPrompt,
		"--output-format", "text",
	}
	if c.model != "" {
		args = append(args, "--model", c.model)
	}
	args = append(args, userMessage)

	cmd := exec.CommandContext(ctx, "claude", args...)
	out, err := cmd.Output()
	if err != nil {
		var stderr string
		if exitErr, ok := err.(*exec.ExitError); ok {
			stderr = string(exitErr.Stderr)
		}
		return "", fmt.Errorf("claude -p: %w\n%s", err, stderr)
	}

	return strings.TrimSpace(string(out)), nil
}

// ModelID returns the model identifier.
func (c *Client) ModelID() string {
	if c.model == "" {
		return "claude"
	}
	return "claude:" + c.model
}
