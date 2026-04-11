package review

import (
	"context"
	"fmt"

	openai "github.com/sashabaranov/go-openai"

	"goodkind.io/lm-review/internal/lmstudio"
)

// Reviewer runs LLM code reviews.
type Reviewer struct {
	client *lmstudio.Client
	model  string
	scope  string
}

// New creates a Reviewer using the given LM Studio client and model.
func New(client *lmstudio.Client, model, scope string) *Reviewer {
	return &Reviewer{client: client, model: model, scope: scope}
}

// ReviewDiff reviews a unified diff string.
func (r *Reviewer) ReviewDiff(ctx context.Context, diff string) (*Result, error) {
	if diff == "" {
		return &Result{Verdict: VerdictPass, Summary: "No changes to review.", Scope: r.scope, Model: r.model}, nil
	}

	raw, err := r.client.Chat(ctx, r.model, []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
		{Role: openai.ChatMessageRoleUser, Content: DiffPrompt(diff)},
	})
	if err != nil {
		return nil, fmt.Errorf("LLM chat: %w", err)
	}

	result, err := Parse(raw)
	if err != nil {
		return nil, err
	}

	result.Model = r.model
	result.Scope = r.scope
	return result, nil
}

// ReviewRepo reviews a snapshot of the full repository.
func (r *Reviewer) ReviewRepo(ctx context.Context, files string) (*Result, error) {
	raw, err := r.client.Chat(ctx, r.model, []openai.ChatCompletionMessage{
		{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
		{Role: openai.ChatMessageRoleUser, Content: RepoPrompt(files)},
	})
	if err != nil {
		return nil, fmt.Errorf("LLM chat: %w", err)
	}

	result, err := Parse(raw)
	if err != nil {
		return nil, err
	}

	result.Model = r.model
	result.Scope = r.scope
	return result, nil
}
