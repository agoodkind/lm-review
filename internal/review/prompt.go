// Package review implements LLM-based code review logic.
package review

import "fmt"

const systemPrompt = `You are a strict code reviewer. Return ONLY valid JSON matching the schema below - no prose, no markdown fences.

Rules to enforce:
- No emdashes (—) or en-dashes (–) in any comments or strings
- Use slog for all logging; never fmt.Fprintf(os.Stderr) for diagnostics
- All config must be TOML (no JSON config files for user-facing config)
- DRY: flag duplicated logic
- Separated concerns: one responsibility per function/file
- Readable over clever: verbose is fine, magic is not
- Is there already a standard library or well-known package for this?
- Is there a fundamentally better approach to the problem?
- Use latest stable dependency versions
- Does the overall change make sense?
- Flag silently swallowed errors (_ = err)
- Flag missing or misleading comments
- Flag security issues (injection, path traversal, hardcoded secrets)
- Flag performance issues (unnecessary allocations, O(n^2) in hot paths)
- No IPv4 literals in code or strings - use domain names or IPv6 literals
- Docs (README, comments, CLAUDE.md) must not contain directory trees, specific file paths, or version-specific details that will drift - docs should describe behavior and concepts, not structure

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

// DiffPrompt builds the user message for a diff review.
func DiffPrompt(diff string) string {
	return "Review this diff:\n\n```diff\n" + diff + "\n```"
}

// RepoPrompt builds the user message for a full repo review.
func RepoPrompt(files string) string {
	return "Review this codebase for structural issues, tech debt, and improvement opportunities:\n\n" + files
}

// ChunkPrompt builds the user message for one chunk of a large codebase review.
// totalChunks > 1 tells the model it is seeing a partial view.
func ChunkPrompt(files string, chunkNum, totalChunks int) string {
	return fmt.Sprintf(
		"Review chunk %d of %d of this codebase. Focus on issues within this chunk; "+
			"note cross-chunk concerns in tech_debt:\n\n%s",
		chunkNum, totalChunks, files,
	)
}
