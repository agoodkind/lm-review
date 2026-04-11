package review

import (
	"context"
	"fmt"

	"github.com/openai/openai-go/v3"

	"goodkind.io/lm-review/internal/lmstudio"
)

// Reviewer runs LLM code reviews.
type Reviewer struct {
	client       *lmstudio.Client
	scope        string
	systemPrompt string
}

// New creates a Reviewer. Rules are injected from config so the system prompt
// is never hardcoded in source.
func New(client *lmstudio.Client, scope string, rules []string) *Reviewer {
	return &Reviewer{
		client:       client,
		scope:        scope,
		systemPrompt: BuildSystemPrompt(rules),
	}
}

// ReviewDiff reviews a unified diff string.
func (r *Reviewer) ReviewDiff(ctx context.Context, diff string) (*Result, error) {
	if diff == "" {
		return &Result{Verdict: VerdictPass, Summary: "No changes to review.", Scope: r.scope, Model: r.client.ModelID()}, nil
	}

	raw, err := r.client.Chat(ctx, []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage(r.systemPrompt),
		openai.UserMessage(DiffPrompt(diff)),
	})
	if err != nil {
		return nil, fmt.Errorf("chat: %w", err)
	}

	result, err := Parse(raw)
	if err != nil {
		return nil, err
	}

	result.Scope = r.scope
	result.Model = r.client.ModelID()
	return result, nil
}

// ReviewRepo reviews a snapshot of the full repository.
func (r *Reviewer) ReviewRepo(ctx context.Context, files string) (*Result, error) {
	raw, err := r.client.Chat(ctx, []openai.ChatCompletionMessageParamUnion{
		openai.SystemMessage(r.systemPrompt),
		openai.UserMessage(RepoPrompt(files)),
	})
	if err != nil {
		return nil, fmt.Errorf("chat: %w", err)
	}

	result, err := Parse(raw)
	if err != nil {
		return nil, err
	}

	result.Scope = r.scope
	result.Model = r.client.ModelID()
	return result, nil
}
