package review

import (
	"context"
	"fmt"

	"goodkind.io/gklog"
	"goodkind.io/lm-review/internal/clock"
	"goodkind.io/lm-review/internal/requestmeta"
)

// Reviewer runs LLM code reviews.
type Reviewer struct {
	client       ChatClient
	scope        string
	systemPrompt string
}

// PromptBuilder is a function that constructs a system prompt from rules.
type PromptBuilder func(rules []string) string

// UserPromptBuilder constructs the user prompt for a review input.
type UserPromptBuilder func(input string) string

// NewWithPromptBuilder creates a Reviewer with a custom prompt builder.
// Use BuildQuickSystemPrompt for quick mode, BuildSystemPrompt for all others.
func NewWithPromptBuilder(client ChatClient, scope string, rules []string, build PromptBuilder) *Reviewer {
	return &Reviewer{
		client:       client,
		scope:        scope,
		systemPrompt: build(rules),
	}
}

// ReviewInput reviews an arbitrary prepared input with the supplied user
// prompt builder.
func (r *Reviewer) ReviewInput(ctx context.Context, input string, buildUserPrompt UserPromptBuilder) (*Result, error) {
	if input == "" {
		return &Result{Verdict: VerdictSkip, Summary: "No changes to review.", Scope: r.scope, Model: r.client.ModelID()}, nil
	}

	metadata := requestmeta.From(ctx)
	if metadata.RequestID() == "" {
		ctx = requestmeta.With(ctx, requestmeta.Metadata{
			ReviewID:   "",
			Scope:      "",
			Mode:       "",
			Depth:      "",
			ChunkIndex: 1,
			ChunkTotal: 1,
		})
		metadata = requestmeta.From(ctx)
	}
	userPrompt := buildUserPrompt(input)
	log := gklog.LoggerFromContext(ctx).With(
		"component", "lm-review",
		"subcomponent", "review",
		"request_id", metadata.RequestID(),
		"review_id", metadata.ReviewID,
		"scope", r.scope,
		"system_bytes", len(r.systemPrompt),
		"user_bytes", len(userPrompt),
	)
	start := clock.Now()
	log.InfoContext(ctx, "review.chat.begin")
	var raw string
	var err error
	if structuredClient, ok := r.client.(reviewChatClient); ok {
		raw, err = structuredClient.ChatReview(ctx, r.systemPrompt, userPrompt)
	} else {
		raw, err = r.client.Chat(ctx, r.systemPrompt, userPrompt)
	}
	latency := clock.Since(start)
	if err != nil {
		log.ErrorContext(ctx, "review.chat.failed", "latency_ms", latency.Milliseconds(), "err", err)
		return nil, fmt.Errorf("chat: %w", err)
	}

	result, err := Parse(raw)
	if err != nil {
		log.ErrorContext(ctx, "review.chat.parse_failed",
			"latency_ms", latency.Milliseconds(),
			"response_bytes", len(raw),
			"err", err)
		return nil, err
	}

	result.Scope = r.scope
	result.Model = r.client.ModelID()
	log.InfoContext(ctx, "review.chat.end",
		"latency_ms", latency.Milliseconds(),
		"response_bytes", len(raw),
		"issue_count", len(result.Issues),
		"verdict", result.Verdict)
	return result, nil
}

// ReviewStatic synthesizes deterministic analyzer findings into a normal
// review result using the existing JSON response schema.
func (r *Reviewer) ReviewStatic(ctx context.Context, files string, analyzerSection string) (*Result, error) {
	userPrompt := StaticPrompt(files, analyzerSection)
	metadata := requestmeta.From(ctx)
	if metadata.RequestID() == "" {
		ctx = requestmeta.With(ctx, requestmeta.Metadata{
			ReviewID:   "",
			Scope:      "",
			Mode:       "",
			Depth:      "",
			ChunkIndex: 1,
			ChunkTotal: 1,
		})
		metadata = requestmeta.From(ctx)
	}
	log := gklog.LoggerFromContext(ctx).With(
		"component", "lm-review",
		"subcomponent", "review",
		"request_id", metadata.RequestID(),
		"review_id", metadata.ReviewID,
		"scope", r.scope,
		"system_bytes", len(r.systemPrompt),
		"user_bytes", len(userPrompt),
	)
	start := clock.Now()
	log.InfoContext(ctx, "review.static.chat.begin")
	var raw string
	var err error
	if structuredClient, ok := r.client.(reviewChatClient); ok {
		raw, err = structuredClient.ChatReview(ctx, r.systemPrompt, userPrompt)
	} else {
		raw, err = r.client.Chat(ctx, r.systemPrompt, userPrompt)
	}
	latency := clock.Since(start)
	if err != nil {
		log.ErrorContext(ctx, "review.static.chat.failed", "latency_ms", latency.Milliseconds(), "err", err)
		return nil, fmt.Errorf("chat: %w", err)
	}

	result, err := Parse(raw)
	if err != nil {
		log.ErrorContext(ctx, "review.static.chat.parse_failed",
			"latency_ms", latency.Milliseconds(),
			"response_bytes", len(raw),
			"err", err)
		return nil, err
	}

	result.Scope = r.scope
	result.Model = r.client.ModelID()
	log.InfoContext(ctx, "review.static.chat.end",
		"latency_ms", latency.Milliseconds(),
		"response_bytes", len(raw),
		"issue_count", len(result.Issues),
		"verdict", result.Verdict)
	return result, nil
}
