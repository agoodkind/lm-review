package analyzer

import (
	"context"

	customanalyzer "goodkind.io/lm-review/internal/analyzer/custom"
)

type customSource struct{}

func (customSource) Name() string { return "custom" }

func (customSource) Run(ctx context.Context, opts RunOptions) ([]Finding, error) {
	specs := []analysisSpec{
		{Tool: "custom", Analyzer: customanalyzer.SlogErrorWithoutErrAnalyzer},
		{Tool: "custom", Analyzer: customanalyzer.BannedDirectOutputAnalyzer},
		{Tool: "custom", Analyzer: customanalyzer.HotLoopInfoLogAnalyzer},
		{Tool: "custom", Analyzer: customanalyzer.MissingBoundaryLogAnalyzer},
	}
	findings, err := runAnalysisDriver(ctx, opts.RepoRoot, opts.Files, opts.EnabledChecks, specs)
	if err != nil {
		return nil, err
	}
	for i := range findings {
		switch findings[i].Check {
		case customanalyzer.SlogErrorWithoutErrAnalyzer.Name:
			findings[i].Principle = "Every slog.Error event should include an err field so the structured log remains actionable."
			findings[i].Fix = "Add an \"err\", err pair to the slog.Error call."
		case customanalyzer.BannedDirectOutputAnalyzer.Name:
			findings[i].Principle = "Production diagnostics should use structured slog output instead of direct fmt/log printing."
			findings[i].Fix = "Replace the direct output call with slog and structured fields."
		case customanalyzer.HotLoopInfoLogAnalyzer.Name:
			findings[i].Principle = "High-frequency loops should avoid info-level logs unless they are intentionally sampled."
			findings[i].Fix = "Lower the log level, aggregate the signal, or move the log outside the loop boundary."
		case customanalyzer.MissingBoundaryLogAnalyzer.Name:
			findings[i].Principle = "Side-effecting exported operations should emit boundary logs so failures are traceable in production."
			findings[i].Fix = "Add a structured log at the operation boundary with relevant IDs and error fields."
		}
	}
	return findings, nil
}
