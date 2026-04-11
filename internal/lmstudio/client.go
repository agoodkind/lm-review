// Package lmstudio wraps go-openai configured for LM Studio's local API.
package lmstudio

import (
	"context"
	"fmt"

	openai "github.com/sashabaranov/go-openai"
)

// Client wraps go-openai pointed at a local LM Studio instance.
type Client struct {
	inner *openai.Client
}

// New creates a Client targeting the given LM Studio base URL with the provided token.
func New(baseURL, token string) *Client {
	cfg := openai.DefaultConfig(token)
	cfg.BaseURL = baseURL + "/v1"

	return &Client{inner: openai.NewClientWithConfig(cfg)}
}

// Chat sends a chat completion request and returns the assistant's reply.
func (c *Client) Chat(ctx context.Context, model string, messages []openai.ChatCompletionMessage) (string, error) {
	resp, err := c.inner.CreateChatCompletion(ctx, openai.ChatCompletionRequest{
		Model:       model,
		Messages:    messages,
		Temperature: 0.1,
	})
	if err != nil {
		return "", fmt.Errorf("LM Studio chat: %w", err)
	}

	if len(resp.Choices) == 0 {
		return "", fmt.Errorf("LM Studio returned no choices")
	}

	return resp.Choices[0].Message.Content, nil
}

// Ping checks that the server is reachable and the token is valid.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.inner.ListModels(ctx)
	if err != nil {
		return fmt.Errorf("LM Studio unreachable: %w", err)
	}

	return nil
}
