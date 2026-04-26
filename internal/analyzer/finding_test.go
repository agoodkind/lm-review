package analyzer

import (
	"strings"
	"testing"
)

func TestDedupeAndSort(t *testing.T) {
	input := []Finding{
		{Tool: "custom", Check: "b", File: "b.go", Line: 2, Message: "second"},
		{Tool: "custom", Check: "b", File: "b.go", Line: 2, Message: "second"},
		{Tool: "vet", Check: "a", File: "a.go", Line: 1, Message: "first"},
	}

	got := dedupeAndSort(input)
	if len(got) != 2 {
		t.Fatalf("len(got)=%d, want 2", len(got))
	}
	if got[0].File != "a.go" || got[1].File != "b.go" {
		t.Fatalf("unexpected sort order: %#v", got)
	}
}

func TestFormatForPrompt(t *testing.T) {
	findings := []Finding{
		{Tool: "vet", Check: "printf", File: "a.go", Line: 10, Message: "bad printf"},
		{Tool: "custom", Check: "slog_error_without_err", File: "b.go", Line: 12, Message: "missing err", Fix: "add err"},
	}

	got := FormatForPrompt(findings)
	for _, want := range []string{"## Static analyzer findings", "### vet", "### custom", "a.go:10 [printf]", "fix: add err"} {
		if !strings.Contains(got, want) {
			t.Fatalf("prompt missing %q:\n%s", want, got)
		}
	}
}
