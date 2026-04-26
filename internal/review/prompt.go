// Package review implements LLM-based code review logic.
package review

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// --- Shared issue schema (used by all tiers) ---

const issueSchema = `
    {
      "severity": "error" | "warning" | "info",
      "file": "path/to/file.go",
      "line": 42,
      "end_line": 45,
      "rule": "short-rule-name",
      "message": "what is wrong and why",
      "suggestion": "concrete fix"
    }`

const verdictRules = `
Verdict rules:
- "block": any error-severity issue
- "warn": warnings present but no blockers
- "pass": clean or only informational notes`

// --- Quick tier ---
// Same rules, same rigor, terse output. Speed comes from shorter messages
// and no extras (highlights, tech_debt). The model checks everything but
// explains minimally.

const quickPromptHeader = `You are a code reviewer. Enforce every rule below. Be terse: short messages, no elaboration.
Return ONLY valid JSON - no prose, no markdown fences.`

const quickPromptSchema = `
JSON schema:
{
  "verdict": "pass" | "warn" | "block",
  "summary": "one short sentence",
  "issues": [` + issueSchema + `
  ]
}
` + verdictRules + `

Be brief. One sentence per message. Skip suggestions unless the fix is non-obvious.`

// --- Normal tier ---
// Standard review. Explain findings, suggest fixes.

const normalPromptHeader = `You are a strict code reviewer. Enforce every rule below.
Return ONLY valid JSON matching the schema below - no prose, no markdown fences.`

const normalPromptSchema = `
JSON schema:
{
  "verdict": "pass" | "warn" | "block",
  "summary": "one concise sentence",
  "issues": [` + issueSchema + `
  ]
}
` + verdictRules + `

Report each distinct finding once. If the same problem spans multiple files, pick the most actionable location and note the scope in the message.`

// --- Deep tier ---
// Thorough review. The model is told to think harder: check edge cases,
// reason about interactions, consider what could go wrong. More findings,
// more explanation, confidence ratings.

const deepPromptHeader = `You are a thorough senior code reviewer performing a careful audit. Enforce every rule below.
Return ONLY valid JSON matching the schema below - no prose, no markdown fences.

Think carefully about each change. Look for:
- Edge cases, off-by-one errors, nil/zero-value pitfalls
- Interactions and ordering dependencies between changed files
- Missing error handling, resource leaks, deferred cleanup
- Concurrency issues: races, missing locks, shared state
- Whether the change is tested and whether existing tests still hold
- Structural problems: wrong abstraction level, misplaced responsibility`

const deepPromptSchema = `
JSON schema:
{
  "verdict": "pass" | "warn" | "block",
  "summary": "one concise sentence",
  "issues": [
    {
      "severity": "error" | "warning" | "info",
      "file": "path/to/file.go",
      "line": 42,
      "end_line": 45,
      "rule": "short-rule-name",
      "message": "what is wrong and why",
      "suggestion": "concrete fix",
      "confidence": "high" | "medium" | "low"
    }
  ],
  "highlights": ["positive things worth noting"],
  "tech_debt": "overall debt assessment or empty string"
}
` + verdictRules + `

Report each distinct finding once. Include low-confidence findings if they warrant investigation. If the same problem spans multiple files, pick the most actionable location and note the scope in the message.`

const staticPromptHeader = `You are a strict code reviewer working from deterministic static-analysis evidence.
Return ONLY valid JSON matching the schema below - no prose, no markdown fences.

You will receive pre-computed analyzer findings from go vet, staticcheck, custom analyzers, and optional semgrep.
Use them as evidence, not as unquestionable truth. Confirm strong findings, reject weak ones contradicted by code context, deduplicate overlaps, and add at most a few extra issues only when the code strongly supports them.`

const staticPromptSchema = `
JSON schema:
{
  "verdict": "pass" | "warn" | "block",
  "summary": "one concise sentence",
  "issues": [
    {
      "severity": "error" | "warning" | "info",
      "file": "path/to/file.go",
      "line": 42,
      "end_line": 45,
      "rule": "short-rule-name",
      "message": "what is wrong and why",
      "suggestion": "concrete fix",
      "confidence": "high" | "medium" | "low"
    }
  ],
  "highlights": ["positive things worth noting"],
  "tech_debt": "overall debt assessment or empty string"
}
` + verdictRules + `

Treat analyzer findings as the default candidate set. Reject findings only when the code context clearly does not support them.`

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

// buildPrompt assembles header + rules + schema into a system prompt.
func buildPrompt(header string, rules []string, schema string) string {
	var sb strings.Builder
	sb.WriteString(header)

	if len(rules) > 0 {
		sb.WriteString("\n\nRules to enforce:\n")
		for _, r := range rules {
			sb.WriteString("- ")
			sb.WriteString(r)
			sb.WriteString("\n")
		}
	}

	sb.WriteString(schema)
	return sb.String()
}

// BuildQuickSystemPrompt builds a minimal prompt for the quick tier.
// Enforces all rules but only reports bugs and security issues.
func BuildQuickSystemPrompt(rules []string) string {
	return buildPrompt(quickPromptHeader, rules, quickPromptSchema)
}

// BuildSystemPrompt builds the standard prompt for the normal tier.
func BuildSystemPrompt(rules []string) string {
	return buildPrompt(normalPromptHeader, rules, normalPromptSchema)
}

// BuildDeepSystemPrompt builds a thorough prompt for the deep tier.
func BuildDeepSystemPrompt(rules []string) string {
	return buildPrompt(deepPromptHeader, rules, deepPromptSchema)
}

// BuildStaticSystemPrompt builds the prompt used for synthesized static review.
func BuildStaticSystemPrompt(rules []string) string {
	return buildPrompt(staticPromptHeader, rules, staticPromptSchema)
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

// StaticPrompt builds the user message for synthesized static review.
func StaticPrompt(files string, analyzerSection string) string {
	var sb strings.Builder
	sb.WriteString("Review this codebase using the static-analysis findings below. Prioritize real issues and collapse overlaps.\n\n")
	if analyzerSection != "" {
		sb.WriteString(analyzerSection)
		sb.WriteString("\n")
	}
	sb.WriteString("CODE:\n\n")
	sb.WriteString(files)
	return sb.String()
}
