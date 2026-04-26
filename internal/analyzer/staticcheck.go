package analyzer

import (
	"context"

	"honnef.co/go/tools/staticcheck"
)

type staticcheckSource struct{}

func (staticcheckSource) Name() string { return "staticcheck" }

func (staticcheckSource) Run(ctx context.Context, opts RunOptions) ([]Finding, error) {
	specs := make([]analysisSpec, 0, len(staticcheck.Analyzers))
	for _, analyzer := range staticcheck.Analyzers {
		if analyzer == nil || analyzer.Analyzer == nil {
			continue
		}
		specs = append(specs, analysisSpec{Tool: "staticcheck", Analyzer: analyzer.Analyzer})
	}
	return runAnalysisDriver(ctx, opts.RepoRoot, opts.Files, opts.EnabledChecks, specs)
}
