package review

import (
	"context"
	"fmt"
)

// Reviewer runs LLM code reviews.
type Reviewer struct {
	client       ChatClient
	scope        string
	systemPrompt string
}

// PromptBuilder is a function that constructs a system prompt from rules.
type PromptBuilder func(rules []string) string

// New creates a Reviewer. Rules are injected from config so the system prompt
// is never hardcoded in source.
func New(client ChatClient, scope string, rules []string) *Reviewer {
	return &Reviewer{
		client:       client,
		scope:        scope,
		systemPrompt: BuildSystemPrompt(rules),
	}
}

// NewWithPromptBuilder creates a Reviewer with a custom prompt builder.
// Use BuildQuickSystemPrompt for quick mode, BuildSystemPrompt for all others.
func NewWithPromptBuilder(client ChatClient, scope string, rules []string, build PromptBuilder) *Reviewer {
	return &Reviewer{
		client:       client,
		scope:        scope,
		systemPrompt: build(rules),
	}
}

// ReviewDiff reviews a unified diff string.
func (r *Reviewer) ReviewDiff(ctx context.Context, diff string) (*Result, error) {
	if diff == "" {
		return &Result{Verdict: VerdictSkip, Summary: "No changes to review.", Scope: r.scope, Model: r.client.ModelID()}, nil
	}

	raw, err := r.client.Chat(ctx, r.systemPrompt, DiffPrompt(diff))
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
	raw, err := r.client.Chat(ctx, r.systemPrompt, RepoPrompt(files))
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

// ReviewStatic synthesizes deterministic analyzer findings into a normal
// review result using the existing JSON response schema.
func (r *Reviewer) ReviewStatic(ctx context.Context, files string, analyzerSection string) (*Result, error) {
	raw, err := r.client.Chat(ctx, r.systemPrompt, StaticPrompt(files, analyzerSection))
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
