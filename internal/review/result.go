package review

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Verdict is the overall outcome of a review.
type Verdict string

// Verdict values returned by reviews. Closed set; new values must be
// added here and to every switch consumer.
const (
	// VerdictPass means the review found no actionable issues.
	VerdictPass Verdict = "pass"
	// VerdictWarn means warnings were found but none block.
	VerdictWarn Verdict = "warn"
	// VerdictBlock means at least one error-severity finding requires
	// the change to be revisited before merging.
	VerdictBlock Verdict = "block"
	// VerdictSkip means there was nothing to review (empty diff,
	// disabled provider, etc.).
	VerdictSkip Verdict = "skip"
)

// Category groups issues by concern type.
type Category string

// Category values used for issue grouping. Closed set.
const (
	// CategoryStyle covers formatting and stylistic issues.
	CategoryStyle Category = "style"
	// CategorySecurity covers vulnerabilities and unsafe patterns.
	CategorySecurity Category = "security"
	// CategoryPerformance covers slow or wasteful code.
	CategoryPerformance Category = "performance"
	// CategoryCorrectness covers bugs and incorrect behaviour.
	CategoryCorrectness Category = "correctness"
	// CategoryReadability covers clarity and naming concerns.
	CategoryReadability Category = "readability"
	// CategoryMaintainability covers structure and long-term health.
	CategoryMaintainability Category = "maintainability"
	// CategoryDependency covers third-party usage concerns.
	CategoryDependency Category = "dependency"
	// CategoryTesting covers missing or weak tests.
	CategoryTesting Category = "testing"
)

// Confidence is the LLM's self-assessed confidence in a finding.
type Confidence string

// Confidence values reported by the LLM. Closed set.
const (
	// ConfidenceHigh signals strong evidence backs the finding.
	ConfidenceHigh Confidence = "high"
	// ConfidenceMedium signals plausible but not certain evidence.
	ConfidenceMedium Confidence = "medium"
	// ConfidenceLow signals a speculative or weakly-supported finding.
	ConfidenceLow Confidence = "low"
)

// Issue is a single finding from the review.
type Issue struct {
	Severity   string     `json:"severity"`           // error | warning | info
	Category   Category   `json:"category,omitempty"` // style | security | performance | ...
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

// reThinkBlock matches chain-of-thought blocks from reasoning models:
// Qwen3 and DeepSeek use <think>...</think>, Phi-4 Reasoning uses
// <|thinking|>...<|/thinking|>, others use <thinking>...</thinking>
// or <reasoning>...</reasoning>.
var reThinkBlock = regexp.MustCompile(`(?s)<(?:think|thinking|reasoning|\|thinking\|)>.*?</(?:think|thinking|reasoning|\|thinking\|)>`)

// reUnclosedThink strips a trailing unclosed think block when a
// reasoning model runs out of tokens mid-thought. Only applied if no
// JSON is found otherwise.
var reUnclosedThink = regexp.MustCompile(`(?s)<(?:think|thinking|reasoning|\|thinking\|)>.*`)

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
		match := extractReviewJSON(raw)
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

// recalcStats recomputes Stats from Issues in case the LLM omitted
// or miscounted them.
func (r *Result) recalcStats() {
	r.Stats = Stats{Errors: 0, Warnings: 0, Infos: 0}
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

// inferVerdict sets Verdict from issue counts if the LLM left it
// empty or inconsistent.
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

// ANSI escape codes used by [Result.Text].
const (
	ansiReset = "\033[0m"
	ansiBold  = "\033[1m"
	ansiDim   = "\033[2m"
)

// verdictAnsiColor returns the ANSI color escape for a verdict.
func verdictAnsiColor(v Verdict) string {
	switch v {
	case VerdictPass:
		return "\033[32m"
	case VerdictWarn:
		return "\033[33m"
	case VerdictBlock:
		return "\033[31m"
	case VerdictSkip:
		return ""
	}
	return ""
}

// verdictIcon returns the emoji used to head a rendered verdict.
func verdictIcon(v Verdict) string {
	switch v {
	case VerdictPass:
		return "✅"
	case VerdictWarn:
		return "⚠️"
	case VerdictBlock:
		return "🚫"
	case VerdictSkip:
		return "⏭️"
	}
	return ""
}

// severityAnsiColor returns the ANSI color escape for a severity.
func severityAnsiColor(severity string) string {
	switch severity {
	case "error":
		return "\033[31m"
	case "warning":
		return "\033[33m"
	case "info":
		return "\033[36m"
	}
	return ""
}

// severityIcon returns a single-character icon for a severity.
func severityIcon(severity string) string {
	switch severity {
	case "error":
		return "✗"
	case "warning":
		return "⚠"
	case "info":
		return "·"
	}
	return ""
}

// formatLineRef formats a line range for display.
func formatLineRef(line, endLine int) string {
	if endLine > line {
		return fmt.Sprintf("%d-%d", line, endLine)
	}
	return strconv.Itoa(line)
}

// groupByFile groups issues by file path while preserving the order in
// which files first appear in r.Issues.
func (r *Result) groupByFile() (map[string][]Issue, []string) {
	byFile := make(map[string][]Issue)
	order := []string{}
	for _, issue := range r.Issues {
		if _, seen := byFile[issue.File]; !seen {
			order = append(order, issue.File)
		}
		byFile[issue.File] = append(byFile[issue.File], issue)
	}
	return byFile, order
}

// writeTextIssues writes the per-file issue list using ANSI styling.
func (r *Result) writeTextIssues(b *strings.Builder) {
	if len(r.Issues) == 0 {
		return
	}
	byFile, order := r.groupByFile()
	b.WriteString("\n")
	for _, file := range order {
		fmt.Fprintf(b, "  %s%s%s\n", ansiBold, file, ansiReset)
		for _, issue := range byFile[file] {
			fmt.Fprintf(b, "    %s%s%s %s[%s:%s]%s %s\n",
				severityAnsiColor(issue.Severity), severityIcon(issue.Severity), ansiReset,
				ansiDim, issue.Rule, formatLineRef(issue.Line, issue.EndLine), ansiReset,
				issue.Message)
			if issue.Suggestion != "" {
				fmt.Fprintf(b, "      %s-> %s%s\n", ansiDim, issue.Suggestion, ansiReset)
			}
		}
		b.WriteString("\n")
	}
}

// Text renders the result for terminal output with ANSI colors.
func (r *Result) Text() string {
	var b strings.Builder

	fmt.Fprintf(&b, "\n%s%s lm-review [%s] %s%s %s%s\n",
		ansiBold, verdictAnsiColor(r.Verdict), r.Model,
		verdictIcon(r.Verdict), strings.ToUpper(string(r.Verdict)),
		r.Summary, ansiReset)

	if r.Stats.Errors+r.Stats.Warnings+r.Stats.Infos > 0 {
		fmt.Fprintf(&b, "%s  %d errors · %d warnings · %d infos%s\n",
			ansiDim, r.Stats.Errors, r.Stats.Warnings, r.Stats.Infos, ansiReset)
	}

	r.writeTextIssues(&b)

	if len(r.Highlights) > 0 {
		fmt.Fprintf(&b, "  %s👍 Highlights%s\n", ansiBold, ansiReset)
		for _, h := range r.Highlights {
			fmt.Fprintf(&b, "  %s· %s%s\n", ansiDim, h, ansiReset)
		}
		b.WriteString("\n")
	}

	if r.TechDebt != "" {
		fmt.Fprintf(&b, "  %s🏗 Tech debt:%s %s\n\n", ansiDim, ansiReset, r.TechDebt)
	}

	return b.String()
}

// scopeLabelFor returns the human-readable label for a review scope.
func scopeLabelFor(scope string) string {
	switch scope {
	case "diff":
		return "Fast Review"
	case "pr":
		return "PR Review"
	case "repo":
		return "Repo Health"
	}
	return "Review"
}

// markdownSeverityLabel returns the emoji-prefixed severity label.
func markdownSeverityLabel(severity string) string {
	switch severity {
	case "error":
		return "🚫 error"
	case "warning":
		return "⚠️ warning"
	case "info":
		return "ℹ️ info"
	}
	return severity
}

// sortIssuesBySeverity sorts issues in-place: error > warning > info.
func sortIssuesBySeverity(issues []Issue) {
	sevOrd := map[string]int{"error": 0, "warning": 1, "info": 2}
	sort.Slice(issues, func(i, j int) bool {
		return sevOrd[issues[i].Severity] < sevOrd[issues[j].Severity]
	})
}

// writeMarkdownFile writes the per-file <details> block for Markdown.
func writeMarkdownFile(b *strings.Builder, file string, issues []Issue) {
	fmt.Fprintf(b, "<details><summary><code>%s</code> (%d issue", file, len(issues))
	if len(issues) != 1 {
		b.WriteString("s")
	}
	b.WriteString(")</summary>\n\n")
	b.WriteString("| Severity | Line | Rule | Message | Suggestion |\n")
	b.WriteString("|----------|------|------|---------|------------|\n")
	for _, issue := range issues {
		suggestion := issue.Suggestion
		if suggestion == "" {
			suggestion = "-"
		}
		fmt.Fprintf(b, "| %s | %s | `%s` | %s | %s |\n",
			markdownSeverityLabel(issue.Severity),
			formatLineRef(issue.Line, issue.EndLine),
			issue.Rule, issue.Message, suggestion)
	}
	b.WriteString("\n</details>\n\n")
}

// writeMarkdownIssues writes the Issues section to b.
func (r *Result) writeMarkdownIssues(b *strings.Builder) {
	if len(r.Issues) == 0 {
		return
	}
	byFile, order := r.groupByFile()
	for _, issues := range byFile {
		sortIssuesBySeverity(issues)
	}
	fmt.Fprintf(b, "### Issues\n\n")
	for _, file := range order {
		writeMarkdownFile(b, file, byFile[file])
	}
}

// Markdown renders the result as a GitHub PR comment body.
func (r *Result) Markdown() string {
	var b strings.Builder

	model := r.Model
	if r.LatencyMs > 0 {
		model = fmt.Sprintf("%s, %dms", r.Model, r.LatencyMs)
	}
	fmt.Fprintf(&b, "## 🤖 %s (%s)\n\n", scopeLabelFor(r.Scope), model)
	fmt.Fprintf(&b, "**Verdict:** %s %s. %s\n\n",
		verdictIcon(r.Verdict), strings.ToUpper(string(r.Verdict)), r.Summary)

	if r.Stats.Errors+r.Stats.Warnings+r.Stats.Infos > 0 {
		fmt.Fprintf(&b, "> %d errors · %d warnings · %d infos\n\n",
			r.Stats.Errors, r.Stats.Warnings, r.Stats.Infos)
	}

	r.writeMarkdownIssues(&b)

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

// sarifArtifactLocation is the URI half of a SARIF location.
type sarifArtifactLocation struct {
	URI string `json:"uri"`
}

// sarifRegion describes the line range a SARIF result points to.
type sarifRegion struct {
	StartLine int `json:"startLine"`
	EndLine   int `json:"endLine,omitempty"`
}

// sarifLocation pairs an artifact (file) with a region (line range).
type sarifLocation struct {
	ArtifactLocation sarifArtifactLocation `json:"artifactLocation"`
	Region           sarifRegion           `json:"region"`
}

// sarifMessage carries the human-readable description of a result.
type sarifMessage struct {
	Text string `json:"text"`
}

// sarifResult is a single finding in SARIF form.
type sarifResult struct {
	RuleID    string          `json:"ruleId"`
	Level     string          `json:"level"`
	Message   sarifMessage    `json:"message"`
	Locations []sarifLocation `json:"locations,omitempty"`
}

// sarifDriver identifies the tool that produced a SARIF run.
type sarifDriver struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// sarifTool wraps a driver, matching the SARIF schema.
type sarifTool struct {
	Driver sarifDriver `json:"driver"`
}

// sarifRun is a single tool run within a SARIF document.
type sarifRun struct {
	Tool    sarifTool     `json:"tool"`
	Results []sarifResult `json:"results"`
}

// sarifDoc is the SARIF 2.1.0 envelope.
type sarifDoc struct {
	Version string     `json:"version"`
	Schema  string     `json:"$schema"`
	Runs    []sarifRun `json:"runs"`
}

// sarifLevelFor maps internal severity strings to SARIF level names.
func sarifLevelFor(severity string) string {
	switch severity {
	case "error":
		return "error"
	case "warning":
		return "warning"
	case "info":
		return "note"
	default:
		return "none"
	}
}

// issueToSarifResult builds a SARIF result for a single Issue.
func issueToSarifResult(issue Issue) sarifResult {
	region := sarifRegion{StartLine: issue.Line, EndLine: 0}
	if issue.EndLine > issue.Line {
		region.EndLine = issue.EndLine
	}
	location := sarifLocation{
		ArtifactLocation: sarifArtifactLocation{URI: issue.File},
		Region:           region,
	}
	text := issue.Message
	if issue.Suggestion != "" {
		text += " Suggestion: " + issue.Suggestion
	}
	return sarifResult{
		RuleID:    issue.Rule,
		Level:     sarifLevelFor(issue.Severity),
		Message:   sarifMessage{Text: text},
		Locations: []sarifLocation{location},
	}
}

// SARIF returns a minimal SARIF 2.1.0 JSON string for tooling
// integration.
func (r *Result) SARIF() (string, error) {
	results := make([]sarifResult, 0, len(r.Issues))
	for _, issue := range r.Issues {
		results = append(results, issueToSarifResult(issue))
	}

	doc := sarifDoc{
		Version: "2.1.0",
		Schema:  "https://json.schemastore.org/sarif-2.1.0.json",
		Runs: []sarifRun{{
			Tool: sarifTool{Driver: sarifDriver{
				Name:    "lm-review",
				Version: "1.0.0",
			}},
			Results: results,
		}},
	}

	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal SARIF: %w", err)
	}
	return string(out), nil
}

// IssuesByCategory returns issues grouped by category, sorted by
// severity within each group.
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

// extractFirstJSONObject scans s for the first '{' that opens a
// balanced JSON object and returns that substring. Returns "" if none
// is found. This avoids the greedy-regex problem where reasoning prose
// containing bare '{' and '}' characters causes the extractor to
// return malformed input.
func extractFirstJSONObject(s string) string {
	for i := range len(s) {
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
			switch ch {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					return s[i : j+1]
				}
			}
		}
	}
	return ""
}

func extractReviewJSON(s string) string {
	// Using a greedy regex here would grab from the first { in reasoning text
	// to the last } in the document, producing invalid JSON. Instead we scan
	// forward from each { to find the one that yields a balanced object.
	if match := extractFirstJSONObject(s); match != "" {
		return match
	}

	// Last-ditch: strip any unclosed reasoning block and try once more.
	retry := strings.TrimSpace(reUnclosedThink.ReplaceAllString(s, ""))
	if match := extractFirstJSONObject(retry); match != "" {
		return match
	}

	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
