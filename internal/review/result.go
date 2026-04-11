package review

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Verdict is the overall outcome of a review.
type Verdict string

const (
	VerdictPass  Verdict = "pass"
	VerdictWarn  Verdict = "warn"
	VerdictBlock Verdict = "block"
)

// Issue is a single finding from the review.
type Issue struct {
	Severity string `json:"severity"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	Rule     string `json:"rule"`
	Message  string `json:"message"`
}

// Result is the structured output from the LLM.
type Result struct {
	Verdict Verdict `json:"verdict"`
	Summary string  `json:"summary"`
	Issues  []Issue `json:"issues"`
	Model   string  `json:"-"`
	Scope   string  `json:"-"`
}

// Parse extracts a Result from raw LLM output.
// The LLM is instructed to return only JSON, but may include markdown fences.
func Parse(raw string) (*Result, error) {
	raw = strings.TrimSpace(raw)

	// Strip markdown code fences if present.
	if strings.HasPrefix(raw, "```") {
		lines := strings.Split(raw, "\n")
		if len(lines) > 2 {
			raw = strings.Join(lines[1:len(lines)-1], "\n")
		}
	}

	var result Result
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return nil, fmt.Errorf("parse LLM response as JSON: %w\nraw: %s", err, raw)
	}

	return &result, nil
}

// ExitCode returns the process exit code for this result.
// block = 1 (fails make), warn/pass = 0.
func (r *Result) ExitCode() int {
	if r.Verdict == VerdictBlock {
		return 1
	}
	return 0
}

// Markdown renders the result as a GitHub PR comment body.
func (r *Result) Markdown() string {
	var b strings.Builder

	icon := map[Verdict]string{
		VerdictPass:  "✅",
		VerdictWarn:  "⚠️",
		VerdictBlock: "🚫",
	}[r.Verdict]

	scopeLabel := map[string]string{
		"diff": "Fast Review",
		"pr":   "PR Review",
		"repo": "Repo Health",
	}[r.Scope]
	if scopeLabel == "" {
		scopeLabel = "Review"
	}

	fmt.Fprintf(&b, "## 🤖 %s (%s)\n\n", scopeLabel, r.Model)
	fmt.Fprintf(&b, "**Verdict:** %s %s\n\n", icon, strings.ToUpper(string(r.Verdict)))
	fmt.Fprintf(&b, "%s\n", r.Summary)

	if len(r.Issues) > 0 {
		fmt.Fprintf(&b, "\n### Issues\n\n")
		fmt.Fprintf(&b, "| Severity | File | Line | Rule | Message |\n")
		fmt.Fprintf(&b, "|----------|------|------|------|---------|\n")
		for _, issue := range r.Issues {
			sev := map[string]string{
				"error":   "🚫",
				"warning": "⚠️",
				"info":    "ℹ️",
			}[issue.Severity]
			fmt.Fprintf(&b, "| %s | `%s` | %d | %s | %s |\n",
				sev, issue.File, issue.Line, issue.Rule, issue.Message)
		}
	}

	fmt.Fprintf(&b, "\n<!-- lm-review:%s -->\n", r.Scope)
	return b.String()
}
