// Package tokenutil provides coarse token-count heuristics for prompt budgeting.
package tokenutil

// EstimateTokens returns a coarse token estimate for code and prompt text.
// The heuristic matches the existing 4 chars/token budget used elsewhere.
func EstimateTokens(text string) int {
	if text == "" {
		return 0
	}
	return (len(text) + 3) / 4
}

// BytesForTokens converts an estimated token budget back into bytes.
func BytesForTokens(tokens int) int {
	if tokens <= 0 {
		return 0
	}
	return tokens * 4
}
