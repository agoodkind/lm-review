package review

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Verdict is the overall outcome of a review.
type Verdict string

const (
	VerdictPass  Verdict = "pass"
	VerdictWarn  Verdict = "warn"
	VerdictBlock Verdict = "block"
	VerdictSkip  Verdict = "skip" // nothing to review
)

// Category groups issues by concern type.
type Category string

const (
	CategoryStyle          Category = "style"
	CategorySecurity       Category = "security"
	CategoryPerformance    Category = "performance"
	CategoryCorrectness    Category = "correctness"
	CategoryReadability    Category = "readability"
	CategoryMaintainability Category = "maintainability"
	CategoryDependency     Category = "dependency"
	CategoryTesting        Category = "testing"
)

// Confidence is the LLM's self-assessed confidence in a finding.
type Confidence string

const (
	ConfidenceHigh   Confidence = "high"
	ConfidenceMedium Confidence = "medium"
	ConfidenceLow    Confidence = "low"
)

// Issue is a single finding from the review.
type Issue struct {
	Severity   string     `json:"severity"`             // error | warning | info
	Category   Category   `json:"category,omitempty"`   // style | security | performance | ...
	File       string     `json:"file"`
	Line       int        `json:"line"`
	EndLine    int        `json:"end_line,omitempty"`
	Rule       string     `json:"rule"`
	Message    string     `json:"message"`
	Suggestion string     `json:"suggestion,omitempty"` // how to fix it
	Confidence Confidence `json:"confidence,omitempty"`
}

// Stats holds issue counts by severity.
type Stats struct {
	Errors   int `json:"errors"`
	Warnings int `json:"warnings"`
	Infos    int `json:"infos"`
}

// Result is the structured output from the LLM.
type Result struct {
	Verdict    Verdict  `json:"verdict"`
	Summary    string   `json:"summary"`
	Issues     []Issue  `json:"issues"`
	Stats      Stats    `json:"stats"`
	Highlights []string `json:"highlights,omitempty"` // positive findings worth noting
	TechDebt   string   `json:"tech_debt,omitempty"`  // overall debt assessment

	// Set by caller, not the LLM.
	Model     string `json:"-"`
	Scope     string `json:"-"`
	LatencyMs int64  `json:"-"`
}

// reThinkBlock matches Qwen3/DeepSeek chain-of-thought blocks.
var reThinkBlock = regexp.MustCompile(`(?s)<think>.*?</think>`)

// Parse extracts a Result from raw LLM output. It handles:
//   - Qwen3/DeepSeek <think>...</think> reasoning blocks
//   - Markdown code fences (```json ... ```)
//   - JSON embedded anywhere in surrounding prose
//   - Stats auto-calculation if the LLM omits them
func Parse(raw string) (*Result, error) {
	raw = strings.TrimSpace(raw)

	// Strip chain-of-thought blocks before anything else.
	raw = reThinkBlock.ReplaceAllString(raw, "")
	raw = strings.TrimSpace(raw)

	// Strip markdown code fences.
	if strings.HasPrefix(raw, "```") {
		lines := strings.Split(raw, "\n")
		end := len(lines) - 1
		for end > 0 && strings.TrimSpace(lines[end]) == "```" {
			end--
		}
		if len(lines) > 1 {
			start := 1 // skip opening fence line
			raw = strings.Join(lines[start:end+1], "\n")
		}
	}

	raw = strings.TrimSpace(raw)

	// Try direct unmarshal first.
	var result Result
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		// Fall back: extract the first balanced JSON object from surrounding prose.
		// Using a greedy regex here would grab from the first { in reasoning text
		// to the last } in the document, producing invalid JSON. Instead we scan
		// forward from each { to find the one that yields a balanced object.
		match := extractFirstJSONObject(raw)
		if match == "" {
			return nil, fmt.Errorf("no JSON found in LLM response: %s", truncate(raw, 200))
		}
		if err2 := json.Unmarshal([]byte(match), &result); err2 != nil {
			return nil, fmt.Errorf("parse LLM JSON: %w\nraw: %s", err2, truncate(raw, 200))
		}
	}

	result.recalcStats()
	result.inferVerdict()

	return &result, nil
}

// recalcStats recomputes Stats from Issues in case the LLM omitted or miscounted them.
func (r *Result) recalcStats() {
	r.Stats = Stats{}
	for _, issue := range r.Issues {
		switch issue.Severity {
		case "error":
			r.Stats.Errors++
		case "warning":
			r.Stats.Warnings++
		case "info":
			r.Stats.Infos++
		}
	}
}

// inferVerdict sets Verdict from issue counts if the LLM left it empty or inconsistent.
func (r *Result) inferVerdict() {
	if r.Verdict == "" {
		switch {
		case r.Stats.Errors > 0:
			r.Verdict = VerdictBlock
		case r.Stats.Warnings > 0:
			r.Verdict = VerdictWarn
		default:
			r.Verdict = VerdictPass
		}
	}
}

// ExitCode returns the process exit code for this result.
// Threshold controls how many errors trigger a block (default 1).
func (r *Result) ExitCode(errorThreshold ...int) int {
	threshold := 1
	if len(errorThreshold) > 0 && errorThreshold[0] > 0 {
		threshold = errorThreshold[0]
	}
	if r.Stats.Errors >= threshold || r.Verdict == VerdictBlock {
		return 1
	}
	return 0
}

// Text renders the result for terminal output with ANSI colors.
func (r *Result) Text() string {
	var b strings.Builder

	verdictColor := map[Verdict]string{
		VerdictPass:  "\033[32m", // green
		VerdictWarn:  "\033[33m", // yellow
		VerdictBlock: "\033[31m", // red
	}
	reset := "\033[0m"
	bold := "\033[1m"
	dim := "\033[2m"

	icon := map[Verdict]string{
		VerdictPass:  "✅",
		VerdictWarn:  "⚠️",
		VerdictBlock: "🚫",
	}[r.Verdict]

	color := verdictColor[r.Verdict]

	fmt.Fprintf(&b, "\n%s%s lm-review [%s] %s%s %s%s\n",
		bold, color, r.Model,
		icon, strings.ToUpper(string(r.Verdict)),
		r.Summary, reset)

	if r.Stats.Errors+r.Stats.Warnings+r.Stats.Infos > 0 {
		fmt.Fprintf(&b, "%s  %d errors · %d warnings · %d infos%s\n",
			dim, r.Stats.Errors, r.Stats.Warnings, r.Stats.Infos, reset)
	}

	if len(r.Issues) > 0 {
		// Group by file.
		byFile := make(map[string][]Issue)
		order := []string{}
		for _, issue := range r.Issues {
			if _, seen := byFile[issue.File]; !seen {
				order = append(order, issue.File)
			}
			byFile[issue.File] = append(byFile[issue.File], issue)
		}

		b.WriteString("\n")
		for _, file := range order {
			issues := byFile[file]
			fmt.Fprintf(&b, "  %s%s%s\n", bold, file, reset)
			for _, issue := range issues {
				sevColor := map[string]string{
					"error":   "\033[31m",
					"warning": "\033[33m",
					"info":    "\033[36m",
				}[issue.Severity]
				sevIcon := map[string]string{
					"error": "✗", "warning": "⚠", "info": "·",
				}[issue.Severity]

				lineRef := fmt.Sprintf("%d", issue.Line)
				if issue.EndLine > issue.Line {
					lineRef = fmt.Sprintf("%d-%d", issue.Line, issue.EndLine)
				}

				fmt.Fprintf(&b, "    %s%s%s %s[%s:%s]%s %s\n",
					sevColor, sevIcon, reset,
					dim, issue.Rule, lineRef, reset,
					issue.Message)

				if issue.Suggestion != "" {
					fmt.Fprintf(&b, "      %s→ %s%s\n", dim, issue.Suggestion, reset)
				}
			}
			b.WriteString("\n")
		}
	}

	if len(r.Highlights) > 0 {
		fmt.Fprintf(&b, "  %s👍 Highlights%s\n", bold, reset)
		for _, h := range r.Highlights {
			fmt.Fprintf(&b, "  %s· %s%s\n", dim, h, reset)
		}
		b.WriteString("\n")
	}

	if r.TechDebt != "" {
		fmt.Fprintf(&b, "  %s🏗 Tech debt:%s %s\n\n", dim, reset, r.TechDebt)
	}

	return b.String()
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

	model := r.Model
	if r.LatencyMs > 0 {
		model = fmt.Sprintf("%s, %dms", r.Model, r.LatencyMs)
	}

	fmt.Fprintf(&b, "## 🤖 %s (%s)\n\n", scopeLabel, model)
	fmt.Fprintf(&b, "**Verdict:** %s %s — %s\n\n", icon, strings.ToUpper(string(r.Verdict)), r.Summary)

	if r.Stats.Errors+r.Stats.Warnings+r.Stats.Infos > 0 {
		fmt.Fprintf(&b, "> %d errors · %d warnings · %d infos\n\n",
			r.Stats.Errors, r.Stats.Warnings, r.Stats.Infos)
	}

	if len(r.Issues) > 0 {
		// Group by file, sort errors first.
		byFile := make(map[string][]Issue)
		order := []string{}
		for _, issue := range r.Issues {
			if _, seen := byFile[issue.File]; !seen {
				order = append(order, issue.File)
			}
			byFile[issue.File] = append(byFile[issue.File], issue)
		}

		// Sort each file's issues: error > warning > info.
		sevOrd := map[string]int{"error": 0, "warning": 1, "info": 2}
		for _, issues := range byFile {
			sort.Slice(issues, func(i, j int) bool {
				return sevOrd[issues[i].Severity] < sevOrd[issues[j].Severity]
			})
		}

		fmt.Fprintf(&b, "### Issues\n\n")
		for _, file := range order {
			issues := byFile[file]
			fmt.Fprintf(&b, "<details><summary><code>%s</code> (%d issue", file, len(issues))
			if len(issues) != 1 {
				b.WriteString("s")
			}
			b.WriteString(")</summary>\n\n")
			b.WriteString("| Severity | Line | Rule | Message | Suggestion |\n")
			b.WriteString("|----------|------|------|---------|------------|\n")
			for _, issue := range issues {
				sev := map[string]string{
					"error": "🚫 error", "warning": "⚠️ warning", "info": "ℹ️ info",
				}[issue.Severity]
				lineRef := fmt.Sprintf("%d", issue.Line)
				if issue.EndLine > issue.Line {
					lineRef = fmt.Sprintf("%d-%d", issue.Line, issue.EndLine)
				}
				suggestion := issue.Suggestion
				if suggestion == "" {
					suggestion = "—"
				}
				fmt.Fprintf(&b, "| %s | %s | `%s` | %s | %s |\n",
					sev, lineRef, issue.Rule, issue.Message, suggestion)
			}
			b.WriteString("\n</details>\n\n")
		}
	}

	if len(r.Highlights) > 0 {
		b.WriteString("### 👍 Highlights\n\n")
		for _, h := range r.Highlights {
			fmt.Fprintf(&b, "- %s\n", h)
		}
		b.WriteString("\n")
	}

	if r.TechDebt != "" {
		fmt.Fprintf(&b, "### 🏗 Tech Debt\n\n%s\n\n", r.TechDebt)
	}

	fmt.Fprintf(&b, "\n<!-- lm-review:%s -->\n", r.Scope)
	return b.String()
}

// SARIF returns a minimal SARIF 2.1.0 JSON string for tooling integration.
func (r *Result) SARIF() (string, error) {
	type sarifLocation struct {
		ArtifactLocation struct {
			URI string `json:"uri"`
		} `json:"artifactLocation"`
		Region struct {
			StartLine int `json:"startLine"`
			EndLine   int `json:"endLine,omitempty"`
		} `json:"region"`
	}
	type sarifResult struct {
		RuleID  string `json:"ruleId"`
		Level   string `json:"level"` // error | warning | note
		Message struct {
			Text string `json:"text"`
		} `json:"message"`
		Locations []sarifLocation `json:"locations,omitempty"`
	}
	type sarifRun struct {
		Tool struct {
			Driver struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"driver"`
		} `json:"tool"`
		Results []sarifResult `json:"results"`
	}
	type sarifDoc struct {
		Version string     `json:"version"`
		Schema  string     `json:"$schema"`
		Runs    []sarifRun `json:"runs"`
	}

	levelMap := map[string]string{
		"error": "error", "warning": "warning", "info": "note",
	}

	var results []sarifResult
	for _, issue := range r.Issues {
		loc := sarifLocation{}
		loc.ArtifactLocation.URI = issue.File
		loc.Region.StartLine = issue.Line
		if issue.EndLine > issue.Line {
			loc.Region.EndLine = issue.EndLine
		}

		sr := sarifResult{
			RuleID: issue.Rule,
			Level:  levelMap[issue.Severity],
			Locations: []sarifLocation{loc},
		}
		sr.Message.Text = issue.Message
		if issue.Suggestion != "" {
			sr.Message.Text += " Suggestion: " + issue.Suggestion
		}
		results = append(results, sr)
	}

	run := sarifRun{Results: results}
	run.Tool.Driver.Name = "lm-review"
	run.Tool.Driver.Version = "1.0.0"

	doc := sarifDoc{
		Version: "2.1.0",
		Schema:  "https://json.schemastore.org/sarif-2.1.0.json",
		Runs:    []sarifRun{run},
	}

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal SARIF: %w", err)
	}
	return string(out), nil
}

// IssuesByCategory returns issues grouped by category, sorted by severity within each group.
func (r *Result) IssuesByCategory() map[Category][]Issue {
	out := make(map[Category][]Issue)
	sevOrd := map[string]int{"error": 0, "warning": 1, "info": 2}
	for _, issue := range r.Issues {
		cat := issue.Category
		if cat == "" {
			cat = "uncategorized"
		}
		out[cat] = append(out[cat], issue)
	}
	for cat := range out {
		sort.Slice(out[cat], func(i, j int) bool {
			return sevOrd[out[cat][i].Severity] < sevOrd[out[cat][j].Severity]
		})
	}
	return out
}

// extractFirstJSONObject scans s for the first '{' that opens a balanced JSON
// object and returns that substring. Returns "" if none is found.
// This avoids the greedy-regex problem where reasoning prose containing bare
// '{' and '}' characters causes the extractor to return malformed input.
func extractFirstJSONObject(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] != '{' {
			continue
		}
		depth := 0
		inString := false
		escape := false
		for j := i; j < len(s); j++ {
			ch := s[j]
			if escape {
				escape = false
				continue
			}
			if ch == '\\' && inString {
				escape = true
				continue
			}
			if ch == '"' {
				inString = !inString
				continue
			}
			if inString {
				continue
			}
			if ch == '{' {
				depth++
			} else if ch == '}' {
				depth--
				if depth == 0 {
					return s[i : j+1]
				}
			}
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
