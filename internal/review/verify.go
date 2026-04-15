package review

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const verifySystemPrompt = `You are a senior code reviewer verifying whether issues found by an automated scan are real.
For each issue, decide if it is a genuine problem worth reporting or a false positive.
Return ONLY valid JSON matching this schema - no prose, no markdown fences.

{
  "confirmed": [0, 2, 5],
  "rejected": [1, 3, 4]
}

Rules:
- "confirmed": indices of issues that are real and should be reported
- "rejected": indices of false positives that should be dropped
- Every index from the input must appear in exactly one list
- When in doubt, confirm the issue (err on the side of reporting)`

// VerifyIssues asks a verification model to filter false positives from a
// sweep pass. Issues confirmed by the verifier are kept; rejected ones are
// dropped. On parse failure, all issues are returned (fail-open).
func VerifyIssues(ctx context.Context, client ChatClient, issues []Issue, diff string) ([]Issue, error) {
	if len(issues) == 0 {
		return nil, nil
	}

	var sb strings.Builder
	sb.WriteString("An automated scan found these issues. Review each one against the diff and decide which are real.\n\n")
	sb.WriteString("ISSUES:\n")
	for i, issue := range issues {
		fmt.Fprintf(&sb, "[%d] %s:%d [%s/%s] %s\n", i, issue.File, issue.Line, issue.Severity, issue.Rule, issue.Message)
		if issue.Suggestion != "" {
			fmt.Fprintf(&sb, "    suggestion: %s\n", issue.Suggestion)
		}
	}
	sb.WriteString("\nDIFF:\n```diff\n")
	sb.WriteString(diff)
	sb.WriteString("\n```")

	raw, err := client.Chat(ctx, verifySystemPrompt, sb.String())
	if err != nil {
		return issues, nil // fail-open
	}

	var result struct {
		Confirmed []int `json:"confirmed"`
	}
	if err := json.Unmarshal([]byte(extractJSON(raw)), &result); err != nil {
		return issues, nil // fail-open
	}

	confirmed := make(map[int]bool, len(result.Confirmed))
	for _, idx := range result.Confirmed {
		confirmed[idx] = true
	}

	var kept []Issue
	for i, issue := range issues {
		if confirmed[i] {
			kept = append(kept, issue)
		}
	}

	// If verifier rejected everything, return all (likely a parse/reasoning error).
	if len(kept) == 0 && len(issues) > 0 {
		return issues, nil
	}

	return kept, nil
}

// extractJSON tries to find a JSON object in the response, handling cases
// where the model wraps it in markdown fences or adds prose.
func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	// Strip markdown fences
	if strings.HasPrefix(s, "```") {
		lines := strings.SplitN(s, "\n", 2)
		if len(lines) == 2 {
			s = lines[1]
		}
		if idx := strings.LastIndex(s, "```"); idx >= 0 {
			s = s[:idx]
		}
	}
	// Find first { and last }
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start >= 0 && end > start {
		return s[start : end+1]
	}
	return s
}
