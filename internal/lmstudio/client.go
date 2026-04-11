// Package lmstudio wraps the official openai-go/v3 SDK pointed at a local LM Studio instance.
package lmstudio

import (
	"context"
	"fmt"
	"net/http"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
)

// Client wraps openai-go/v3 configured for LM Studio's local API.
type Client struct {
	inner openai.Client
	model string
}

// New creates a Client targeting the given LM Studio base URL with the provided token.
func New(baseURL, token, model string) *Client {
	c := openai.NewClient(
		option.WithBaseURL(baseURL+"/v1"),
		option.WithAPIKey(token),
		option.WithHTTPClient(&http.Client{}),
	)
	return &Client{inner: c, model: model}
}

// Chat sends messages and returns the assistant reply.
func (c *Client) Chat(ctx context.Context, messages []openai.ChatCompletionMessageParamUnion) (string, error) {
	resp, err := c.inner.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model:       c.model,
		Messages:    messages,
		Temperature: param.NewOpt[float64](0.1),
	})
	if err != nil {
		return "", fmt.Errorf("LM Studio chat: %w", err)
	}
	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("LM Studio returned no choices")
	}
	return resp.Choices[0].Message.Content, nil
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
