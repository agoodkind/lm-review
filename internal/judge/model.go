// Package judge implements the lm-review rule judge gRPC service.
package judge

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"goodkind.io/lm-review/api/judgepb"
	"goodkind.io/lm-review/internal/config"
	"goodkind.io/lm-review/internal/lmstudio"
)

const (
	defaultLMDBaseURL       = "http://[::1]:5400"
	decisionMaxTokens       = 4
	decisionRequestTimeout  = 90 * time.Second
	decisionTokenGroupIndex = 1
)

var (
	lmdBaseURL           = defaultLMDBaseURL
	decisionTokenPattern = regexp.MustCompile(`(?i)\b(block|allow)\b`)
)

// Complete sends a decision-only chat request to the configured
// OpenAI-compatible endpoint. baseURL and token come from the judge config
// (openai_compat); an empty baseURL falls back to the local lmd default so
// tests and local runs work without configuration.
func Complete(ctx context.Context, model string, baseURL string, token string, system string, user string, maxTokens int) (string, error) {
	if baseURL == "" {
		baseURL = lmdBaseURL
	}
	client := lmstudio.New(
		baseURL,
		token,
		model,
		maxTokens,
		decisionRequestTimeout,
		config.ChatSettings{
			Temperature:      0,
			TopP:             nil,
			TopK:             nil,
			PresencePenalty:  nil,
			FrequencyPenalty: nil,
			RepeatPenalty:    nil,
			Seed:             nil,
			Stop:             nil,
		},
	)
	content, err := client.Chat(ctx, system, user)
	if err != nil {
		slog.ErrorContext(ctx, "judge.complete.failed", "err", err)
		return "", fmt.Errorf("complete judge chat: %w", err)
	}
	return content, nil
}

// Decide asks the judge model for a block or allow verdict against the
// configured endpoint (baseURL and token from the judge config).
func Decide(ctx context.Context, model string, baseURL string, token string, system string, user string) (judgepb.Verdict, error) {
	content, err := Complete(ctx, model, baseURL, token, system, user, decisionMaxTokens)
	if err != nil {
		return judgepb.Verdict_VERDICT_UNSPECIFIED, err
	}
	return parseDecision(content)
}

func parseDecision(content string) (judgepb.Verdict, error) {
	matches := decisionTokenPattern.FindStringSubmatch(content)
	if len(matches) <= decisionTokenGroupIndex {
		return judgepb.Verdict_VERDICT_UNSPECIFIED, fmt.Errorf("judge reply missing block or allow token: %q", content)
	}
	token := strings.ToLower(matches[decisionTokenGroupIndex])
	if token == "block" {
		return judgepb.Verdict_VERDICT_BLOCK, nil
	}
	if token == "allow" {
		return judgepb.Verdict_VERDICT_ALLOW, nil
	}
	return judgepb.Verdict_VERDICT_UNSPECIFIED, fmt.Errorf("unsupported judge token: %q", token)
}
