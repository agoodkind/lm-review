// Package review implements LLM-based code review logic.
package review

import (
	"fmt"
	"strings"
)

const systemPromptHeader = `You are a strict code reviewer. Return ONLY valid JSON matching the schema below - no prose, no markdown fences.`

const systemPromptSchema = `
JSON schema:
{
  "verdict": "pass" | "warn" | "block",
  "summary": "one concise sentence",
  "issues": [
    {
      "severity": "error" | "warning" | "info",
      "category": "style" | "security" | "performance" | "correctness" | "readability" | "maintainability" | "dependency" | "testing",
      "file": "path/to/file.go",
      "line": 42,
      "end_line": 45,
      "rule": "short-rule-name",
      "message": "what is wrong and why",
      "suggestion": "concrete fix",
      "confidence": "high" | "medium" | "low"
    }
  ],
  "stats": { "errors": 0, "warnings": 0, "infos": 0 },
  "highlights": ["positive things worth noting"],
  "tech_debt": "overall debt assessment or empty string"
}

Verdict rules:
- "block": any error-severity issue, security vulnerability, or fundamentally broken logic
- "warn": warnings present but no blockers
- "pass": clean or only informational notes`

// BuildSystemPrompt constructs the system prompt from a list of rule strings.
// Rules come from the user's config.toml [[rules]] entries.
func BuildSystemPrompt(rules []string) string {
	var sb strings.Builder
	sb.WriteString(systemPromptHeader)

	if len(rules) > 0 {
		sb.WriteString("\n\nRules to enforce:\n")
		for _, r := range rules {
			fmt.Fprintf(&sb, "- %s\n", r)
		}
	}

	sb.WriteString(systemPromptSchema)
	return sb.String()
}

// DiffPrompt builds the user message for a diff review.
func DiffPrompt(diff string) string {
	return "Review this diff:\n\n```diff\n" + diff + "\n```"
}

// RepoPrompt builds the user message for a full repo review.
func RepoPrompt(files string) string {
	return "Review this codebase for structural issues, tech debt, and improvement opportunities:\n\n" + files
}

// ChunkPrompt builds the user message for one chunk of a large codebase review.
func ChunkPrompt(files string, chunkNum, totalChunks int) string {
	return fmt.Sprintf(
		"Review chunk %d of %d of this codebase. Focus on issues within this chunk; "+
			"note cross-chunk concerns in tech_debt:\n\n%s",
		chunkNum, totalChunks, files,
	)
}
