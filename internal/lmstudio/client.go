// Package lmstudio wraps the official openai-go/v3 SDK pointed at a local LM Studio instance.
package lmstudio

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
)

const (
	// repWindowSize is the minimum char length of a repeated block to flag.
	repWindowSize = 80

	// repThreshold is the number of times a block must repeat to abort.
	repThreshold = 4
)

// Client wraps openai-go/v3 configured for LM Studio's local API.
type Client struct {
	inner     openai.Client
	model     string
	maxTokens int64
}

// New creates a Client targeting the given LM Studio base URL with the provided token.
// maxTokens caps the response length to prevent infinite generation loops.
func New(baseURL, token, model string, maxTokens int) *Client {
	c := openai.NewClient(
		option.WithBaseURL(baseURL+"/v1"),
		option.WithAPIKey(token),
		option.WithHTTPClient(&http.Client{}),
	)
	return &Client{inner: c, model: model, maxTokens: int64(maxTokens)}
}

// Chat sends a system prompt and user message, returning the assistant reply.
func (c *Client) Chat(ctx context.Context, systemPrompt, userMessage string) (string, error) {
	resp, err := c.inner.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model: c.model,
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(systemPrompt),
			openai.UserMessage(userMessage),
		},
		Temperature: param.NewOpt[float64](0.1),
		MaxTokens:   param.NewOpt[int64](c.maxTokens),
	})
	if err != nil {
		return "", fmt.Errorf("LM Studio chat: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("LM Studio returned no choices")
	}
	content := resp.Choices[0].Message.Content
	if err := detectRepetition(content); err != nil {
		return content, err
	}
	return content, nil
}

// detectRepetition checks whether the response contains a repeated block of
// text, which indicates the model is stuck in a generation loop. Returns an
// error describing the loop if detected.
func detectRepetition(s string) error {
	if len(s) < repWindowSize*repThreshold {
		return nil
	}
	// Scan from the end: take a window and count how many times it appears.
	tail := s[len(s)-repWindowSize:]
	count := strings.Count(s, tail)
	if count >= repThreshold {
		return fmt.Errorf("repetition loop detected: %d-char block repeated %d times, response likely degenerate", repWindowSize, count)
	}
	return nil
}

// ModelID returns the model identifier this client is configured to use.
func (c *Client) ModelID() string {
	return c.model
}

// Ping checks the server is reachable and the token is valid.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.inner.Models.List(ctx)
	if err != nil {
		return fmt.Errorf("LM Studio unreachable: %w", err)
	}
	return nil
}
