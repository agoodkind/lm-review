package analyzer

import (
	"context"

	"golang.org/x/tools/go/analysis/passes/assign"
	"golang.org/x/tools/go/analysis/passes/atomic"
	"golang.org/x/tools/go/analysis/passes/bools"
	"golang.org/x/tools/go/analysis/passes/copylock"
	"golang.org/x/tools/go/analysis/passes/errorsas"
	"golang.org/x/tools/go/analysis/passes/httpresponse"
	"golang.org/x/tools/go/analysis/passes/lostcancel"
	"golang.org/x/tools/go/analysis/passes/nilfunc"
	"golang.org/x/tools/go/analysis/passes/printf"
	"golang.org/x/tools/go/analysis/passes/shift"
	"golang.org/x/tools/go/analysis/passes/stdmethods"
	"golang.org/x/tools/go/analysis/passes/stringintconv"
	"golang.org/x/tools/go/analysis/passes/structtag"
	"golang.org/x/tools/go/analysis/passes/tests"
	"golang.org/x/tools/go/analysis/passes/unmarshal"
	"golang.org/x/tools/go/analysis/passes/unreachable"
	"golang.org/x/tools/go/analysis/passes/unusedresult"
)

type vetSource struct{}

func (vetSource) Name() string { return "vet" }

func (vetSource) Run(ctx context.Context, opts RunOptions) ([]Finding, error) {
	specs := []analysisSpec{
		{Tool: "vet", Analyzer: assign.Analyzer},
		{Tool: "vet", Analyzer: atomic.Analyzer},
		{Tool: "vet", Analyzer: bools.Analyzer},
		{Tool: "vet", Analyzer: copylock.Analyzer},
		{Tool: "vet", Analyzer: errorsas.Analyzer},
		{Tool: "vet", Analyzer: httpresponse.Analyzer},
		{Tool: "vet", Analyzer: lostcancel.Analyzer},
		{Tool: "vet", Analyzer: nilfunc.Analyzer},
		{Tool: "vet", Analyzer: printf.Analyzer},
		{Tool: "vet", Analyzer: shift.Analyzer},
		{Tool: "vet", Analyzer: stdmethods.Analyzer},
		{Tool: "vet", Analyzer: stringintconv.Analyzer},
		{Tool: "vet", Analyzer: structtag.Analyzer},
		{Tool: "vet", Analyzer: tests.Analyzer},
		{Tool: "vet", Analyzer: unmarshal.Analyzer},
		{Tool: "vet", Analyzer: unreachable.Analyzer},
		{Tool: "vet", Analyzer: unusedresult.Analyzer},
	}
	return runAnalysisDriver(ctx, opts.RepoRoot, opts.Files, opts.EnabledChecks, specs)
}
