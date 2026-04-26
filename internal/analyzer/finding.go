package analyzer

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
	SeverityInfo    Severity = "info"
)

type Finding struct {
	Tool      string   `json:"tool"`
	Check     string   `json:"check"`
	Severity  Severity `json:"severity"`
	File      string   `json:"file"`
	Line      int      `json:"line"`
	EndLine   int      `json:"end_line,omitempty"`
	Message   string   `json:"message"`
	Principle string   `json:"principle,omitempty"`
	Fix       string   `json:"fix,omitempty"`
}

type Source interface {
	Name() string
	Run(context.Context, RunOptions) ([]Finding, error)
}

type RunOptions struct {
	RepoRoot      string
	Files         []string
	EnabledChecks []string
}

type Config struct {
	DisabledSources []string
	EnabledChecks   []string
}

func Run(ctx context.Context, cfg Config, opts RunOptions) ([]Finding, []error) {
	var all []Finding
	var errs []error
	for _, source := range DefaultSources() {
		if contains(cfg.DisabledSources, source.Name()) {
			continue
		}
		sourceOpts := opts
		if len(sourceOpts.EnabledChecks) == 0 {
			sourceOpts.EnabledChecks = cfg.EnabledChecks
		}
		findings, err := source.Run(ctx, sourceOpts)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", source.Name(), err))
		}
		all = append(all, findings...)
	}
	return dedupeAndSort(all), errs
}

func FormatForPrompt(findings []Finding) string {
	if len(findings) == 0 {
		return ""
	}

	groups := map[string][]Finding{}
	for _, finding := range findings {
		groups[finding.Tool] = append(groups[finding.Tool], finding)
	}

	orderedTools := []string{"vet", "staticcheck", "custom", "semgrep"}
	var builder strings.Builder
	builder.WriteString("## Static analyzer findings\n\n")
	builder.WriteString("These findings were computed before the LLM review. Confirm, prioritize, or reject them using code context.\n\n")
	for _, tool := range orderedTools {
		items := groups[tool]
		if len(items) == 0 {
			continue
		}
		fmt.Fprintf(&builder, "### %s\n\n", tool)
		for _, finding := range items {
			fmt.Fprintf(&builder, "- %s:%d [%s] %s\n", finding.File, finding.Line, finding.Check, finding.Message)
			if finding.Principle != "" {
				fmt.Fprintf(&builder, "  principle: %s\n", finding.Principle)
			}
			if finding.Fix != "" {
				fmt.Fprintf(&builder, "  fix: %s\n", finding.Fix)
			}
		}
		builder.WriteString("\n")
	}
	return builder.String()
}

func dedupeAndSort(findings []Finding) []Finding {
	type key struct {
		File    string
		Line    int
		Tool    string
		Check   string
		Message string
	}
	seen := make(map[key]bool, len(findings))
	out := make([]Finding, 0, len(findings))
	for _, finding := range findings {
		findingKey := key{
			File:    finding.File,
			Line:    finding.Line,
			Tool:    finding.Tool,
			Check:   finding.Check,
			Message: finding.Message,
		}
		if seen[findingKey] {
			continue
		}
		seen[findingKey] = true
		out = append(out, finding)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		if out[i].Line != out[j].Line {
			return out[i].Line < out[j].Line
		}
		if out[i].Tool != out[j].Tool {
			return out[i].Tool < out[j].Tool
		}
		return out[i].Check < out[j].Check
	})
	return out
}

func contains(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}
