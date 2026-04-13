// Package review implements LLM-based code review logic.
package review

import (
	"fmt"
	"path/filepath"
	"regexp"
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
- "pass": clean or only informational notes

Report each distinct finding once. If the same problem spans multiple files, pick the most actionable location and note the scope in the message.`

// reDiffFile matches file paths from unified diff headers: "+++ b/path/to/file"
var reDiffFile = regexp.MustCompile(`(?m)^\+\+\+ b/(.+)$`)

// FilesFromDiff extracts the set of file paths touched by a unified diff.
func FilesFromDiff(diff string) []string {
	matches := reDiffFile.FindAllStringSubmatch(diff, -1)
	seen := make(map[string]bool)
	var files []string
	for _, m := range matches {
		if !seen[m[1]] {
			seen[m[1]] = true
			files = append(files, m[1])
		}
	}
	return files
}

// ruleApplies returns true when a rule should be included given the files present.
// always = true forces inclusion regardless of globs.
// No globs = always applies.
// Globs set = applies when at least one file matches.
func ruleApplies(globs []string, always bool, files []string) bool {
	if always || len(globs) == 0 {
		return true
	}
	for _, g := range globs {
		for _, f := range files {
			if matched, _ := filepath.Match(g, filepath.Base(f)); matched {
				return true
			}
			if matched, _ := filepath.Match(g, f); matched {
				return true
			}
		}
	}
	return false
}

// BuildSystemPrompt constructs the system prompt from a list of rule strings.
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

// RuleFilter carries the glob and always metadata for a single rule.
type RuleFilter struct {
	Globs  []string
	Always bool
}

// FilterRules returns only the rule texts that apply to the given files.
func FilterRules(ruleTexts []string, filters []RuleFilter, files []string) []string {
	out := make([]string, 0, len(ruleTexts))
	for i, text := range ruleTexts {
		var f RuleFilter
		if i < len(filters) {
			f = filters[i]
		}
		if ruleApplies(f.Globs, f.Always, files) {
			out = append(out, text)
		}
	}
	return out
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
