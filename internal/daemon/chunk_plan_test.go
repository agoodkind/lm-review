package daemon

import (
	"strings"
	"testing"

	"goodkind.io/lm-review/internal/config"
	"goodkind.io/lm-review/internal/review"
)

func TestBuildReviewChunkPlanReservesPromptOverhead(t *testing.T) {
	cfg := config.OpenAICompat{
		ContextLength:     1024,
		MaxResponseTokens: 256,
	}
	prepared := &preparedReviewInput{
		scope:         "repo",
		input:         strings.Repeat("a", 16000),
		fullPrompt:    review.RepoPrompt,
		chunkPrompt:   review.ChunkPrompt,
		inputSplitter: review.SplitRepoInput,
	}

	plan := buildReviewChunkPlan(cfg, prepared, []string{"check tests"}, review.BuildDeepSystemPrompt)

	if plan.contextLength != 1024 {
		t.Fatalf("context_length=%d, want 1024", plan.contextLength)
	}
	if plan.promptOverheadTokens <= 0 {
		t.Fatalf("prompt_overhead_tokens=%d, want > 0", plan.promptOverheadTokens)
	}
	if plan.chunkTokenBudget <= 0 {
		t.Fatalf("chunk_token_budget=%d, want > 0", plan.chunkTokenBudget)
	}
	if plan.chunkBytes >= cfg.ResolveReviewChunkBytes() {
		t.Fatalf("chunk_bytes=%d, want less than configured byte cap %d", plan.chunkBytes, cfg.ResolveReviewChunkBytes())
	}
}
