package analyzer

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/tools/go/analysis"
	"golang.org/x/tools/go/analysis/checker"
	"golang.org/x/tools/go/packages"
)

type analysisSpec struct {
	Tool     string
	Analyzer *analysis.Analyzer
}

// runAnalysisDriver loads packages from repoRoot, runs the selected
// analyzers, and converts diagnostics to project [Finding] values.
func runAnalysisDriver(ctx context.Context, repoRoot string, files []string, enabledChecks []string, specs []analysisSpec) ([]Finding, error) {
	analyzers, toolByName := selectAnalyzers(specs, enabledChecks)
	if len(analyzers) == 0 {
		return nil, nil
	}

	graph, err := loadAndAnalyze(ctx, repoRoot, analyzers)
	if err != nil || graph == nil {
		return nil, err
	}

	allowedFiles := make(map[string]struct{}, len(files))
	for _, file := range files {
		allowedFiles[filepath.Clean(file)] = struct{}{}
	}
	return collectFindings(graph, toolByName, repoRoot, allowedFiles), nil
}

// selectAnalyzers filters specs by the optional enabledChecks list and
// builds a name-to-tool map for diagnostic attribution.
func selectAnalyzers(specs []analysisSpec, enabledChecks []string) ([]*analysis.Analyzer, map[string]string) {
	analyzers := make([]*analysis.Analyzer, 0, len(specs))
	toolByName := make(map[string]string, len(specs))
	for _, spec := range specs {
		if spec.Analyzer == nil {
			continue
		}
		if len(enabledChecks) > 0 && !slices.Contains(enabledChecks, spec.Analyzer.Name) {
			continue
		}
		analyzers = append(analyzers, spec.Analyzer)
		toolByName[spec.Analyzer.Name] = spec.Tool
	}
	return analyzers, toolByName
}

// loadAndAnalyze loads packages under repoRoot and runs the analyzer
// graph. Returns nil graph when no packages were loaded.
func loadAndAnalyze(ctx context.Context, repoRoot string, analyzers []*analysis.Analyzer) (*checker.Graph, error) {
	cfg := &packages.Config{
		Context: ctx,
		Dir:     repoRoot,
		Mode:    packages.LoadAllSyntax,
	}
	initial, err := packages.Load(cfg, "./...")
	if err != nil {
		slog.ErrorContext(ctx, "analyzer.load_packages.failed", "repo_root", repoRoot, "err", err)
		return nil, fmt.Errorf("load packages: %w", err)
	}
	if len(initial) == 0 {
		return nil, nil
	}
	graph, err := checker.Analyze(analyzers, initial, nil)
	if err != nil {
		slog.ErrorContext(ctx, "analyzer.checker.failed", "repo_root", repoRoot, "err", err)
		return nil, fmt.Errorf("analyze packages: %w", err)
	}
	return graph, nil
}

// collectFindings walks every action in the graph and emits a Finding
// for every diagnostic whose file passes the allowedFiles filter.
func collectFindings(graph *checker.Graph, toolByName map[string]string, repoRoot string, allowedFiles map[string]struct{}) []Finding {
	var findings []Finding
	for action := range graph.All() {
		tool := toolByName[action.Analyzer.Name]
		for _, diagnostic := range action.Diagnostics {
			finding, ok := buildFinding(action, diagnostic, tool, repoRoot, allowedFiles)
			if !ok {
				continue
			}
			findings = append(findings, finding)
		}
	}
	return findings
}

// buildFinding converts one diagnostic to a [Finding] when the file is
// in scope. Returns ok=false when the diagnostic should be skipped.
func buildFinding(action *checker.Action, diagnostic analysis.Diagnostic, tool, repoRoot string, allowedFiles map[string]struct{}) (Finding, bool) {
	pos := action.Package.Fset.PositionFor(diagnostic.Pos, false)
	if pos.Filename == "" {
		return zeroFinding(), false
	}
	rel, relErr := filepath.Rel(repoRoot, pos.Filename)
	if relErr != nil {
		return zeroFinding(), false
	}
	rel = filepath.Clean(rel)
	if strings.HasPrefix(rel, "..") {
		return zeroFinding(), false
	}
	if len(allowedFiles) > 0 {
		if _, ok := allowedFiles[rel]; !ok {
			return zeroFinding(), false
		}
	}
	endLine := 0
	if diagnostic.End.IsValid() {
		endPos := action.Package.Fset.PositionFor(diagnostic.End, false)
		endLine = endPos.Line
	}
	return Finding{
		Tool:      tool,
		Check:     checkName(action.Analyzer, diagnostic),
		Severity:  severityFor(action.Analyzer.Name),
		File:      rel,
		Line:      pos.Line,
		EndLine:   endLine,
		Message:   diagnostic.Message,
		Principle: "",
		Fix:       "",
	}, true
}

// zeroFinding returns a fully-initialized empty Finding for "skip"
// returns from buildFinding. Centralized so exhaustruct stays happy.
func zeroFinding() Finding {
	return Finding{
		Tool: "", Check: "", Severity: "",
		File: "", Line: 0, EndLine: 0,
		Message: "", Principle: "", Fix: "",
	}
}

func checkName(analyzer *analysis.Analyzer, diagnostic analysis.Diagnostic) string {
	if diagnostic.Category != "" {
		return diagnostic.Category
	}
	return analyzer.Name
}

// severityFor maps an analyzer to its default severity. Today every
// analyzer reports SeverityWarning; the function exists so per-check
// overrides can be added without touching every call site.
func severityFor(_ string) Severity {
	return SeverityWarning
}
