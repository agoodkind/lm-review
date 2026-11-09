package daemon

import (
	"goodkind.io/lm-review/api/reviewpb"
	"goodkind.io/lm-review/internal/audit"
	"goodkind.io/lm-review/internal/config"
	"goodkind.io/lm-review/internal/review"
)

// Server implements the LMReviewD gRPC service.
type Server struct {
	reviewpb.UnimplementedLMReviewDServer

	log *audit.Logger
	cfg *config.Config
}

type preparedReviewInput struct {
	scope           string
	input           string
	fullPrompt      review.UserPromptBuilder
	chunkPrompt     review.ChunkPromptBuilder
	inputSplitter   review.InputSplitter
	selectedFiles   []string
	selectedModeLog string
}

type reviewChunkPlan struct {
	chunkBytes           int
	chunkTokenBudget     int
	promptOverheadTokens int
	estimatedInputTokens int
	contextLength        int
}

type reviewDepth string

const (
	reviewDepthQuick reviewDepth = "quick"
	reviewDepthDeep  reviewDepth = "deep"
	reviewDepthUltra reviewDepth = "ultra"
)
