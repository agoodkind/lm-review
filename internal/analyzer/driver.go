package analyzer

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/checker"
	"golang.org/x/tools/go/packages"
)

type analysisSpec struct {
	Tool     string
	Analyzer *analysis.Analyzer
}

func runAnalysisDriver(ctx context.Context, repoRoot string, files []string, enabledChecks []string, specs []analysisSpec) ([]Finding, error) {
	if len(specs) == 0 {
		return nil, nil
	}

	analyzers := make([]*analysis.Analyzer, 0, len(specs))
	toolByName := make(map[string]string, len(specs))
	for _, spec := range specs {
		if spec.Analyzer == nil {
			continue
		}
		if len(enabledChecks) > 0 && !contains(enabledChecks, spec.Analyzer.Name) {
			continue
		}
		analyzers = append(analyzers, spec.Analyzer)
		toolByName[spec.Analyzer.Name] = spec.Tool
	}
	if len(analyzers) == 0 {
		return nil, nil
	}

	cfg := &packages.Config{
		Context: ctx,
		Dir:     repoRoot,
		Mode:    packages.LoadAllSyntax,
	}
	initial, err := packages.Load(cfg, "./...")
	if err != nil {
		return nil, fmt.Errorf("load packages: %w", err)
	}
	if len(initial) == 0 {
		return nil, nil
	}

	graph, err := checker.Analyze(analyzers, initial, nil)
	if err != nil {
		return nil, fmt.Errorf("analyze packages: %w", err)
	}

	allowedFiles := make(map[string]bool, len(files))
	for _, file := range files {
		allowedFiles[filepath.Clean(file)] = true
	}

	var findings []Finding
	for action := range graph.All() {
		tool := toolByName[action.Analyzer.Name]
		for _, diagnostic := range action.Diagnostics {
			pos := action.Package.Fset.PositionFor(diagnostic.Pos, false)
			if pos.Filename == "" {
				continue
			}
			rel, relErr := filepath.Rel(repoRoot, pos.Filename)
			if relErr != nil {
				continue
			}
			rel = filepath.Clean(rel)
			if strings.HasPrefix(rel, "..") {
				continue
			}
			if len(allowedFiles) > 0 && !allowedFiles[rel] {
				continue
			}
			endLine := 0
			if diagnostic.End.IsValid() {
				endPos := action.Package.Fset.PositionFor(diagnostic.End, false)
				endLine = endPos.Line
			}
			findings = append(findings, Finding{
				Tool:     tool,
				Check:    checkName(action.Analyzer, diagnostic),
				Severity: severityFor(tool, action.Analyzer.Name),
				File:     rel,
				Line:     pos.Line,
				EndLine:  endLine,
				Message:  diagnostic.Message,
			})
		}
	}
	return findings, nil
}

func checkName(analyzer *analysis.Analyzer, diagnostic analysis.Diagnostic) string {
	if diagnostic.Category != "" {
		return diagnostic.Category
	}
	return analyzer.Name
}

func severityFor(tool string, check string) Severity {
	if tool == "staticcheck" && strings.HasPrefix(check, "SA") {
		return SeverityWarning
	}
	return SeverityWarning
}
