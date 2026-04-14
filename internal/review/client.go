// Package review defines the ChatClient interface for LLM providers.
package review

import "context"

// ChatClient abstracts an LLM provider. Both lmstudio and claude implementations
// satisfy this interface.
type ChatClient interface {
	// Chat sends a system prompt and user message, returning the raw response text.
	Chat(ctx context.Context, systemPrompt, userMessage string) (string, error)

	// ModelID returns the model identifier this client is configured to use.
	ModelID() string
}
