package daemon

import (
	"testing"

	"goodkind.io/lm-review/internal/analyzer"
	"goodkind.io/lm-review/internal/review"
)

func TestRawStaticResultMapsFindings(t *testing.T) {
	result := rawStaticResult([]analyzer.Finding{
		{Tool: "staticcheck", Check: "SA4006", Severity: analyzer.SeverityWarning, File: "a.go", Line: 3, Message: "value assigned but never used", Fix: "remove assignment"},
		{Tool: "custom", Check: "slog_error_without_err", Severity: analyzer.SeverityError, File: "b.go", Line: 5, Message: "missing err"},
	}, []error{nil})

	if result.Verdict != review.VerdictBlock {
		t.Fatalf("verdict=%q, want %q", result.Verdict, review.VerdictBlock)
	}
	if len(result.Issues) != 2 {
		t.Fatalf("issues=%d, want 2", len(result.Issues))
	}
	if result.Issues[0].Rule != "SA4006" {
		t.Fatalf("first rule=%q, want SA4006", result.Issues[0].Rule)
	}
	if result.Stats.Errors != 1 || result.Stats.Warnings != 1 {
		t.Fatalf("stats=%+v, want 1 error and 1 warning", result.Stats)
	}
}
