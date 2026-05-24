package review

import (
	"strings"
	"testing"
)

func TestParseRejectsBareReviewObjectMissingOpeningBrace(t *testing.T) {
	raw := "\"verdict\": \"pass\",\n" +
		"  \"summary\": \"No issues found in this minimal diff.\",\n" +
		"  \"issues\": []\n" +
		"}\n\n" +
		"```"

	result, err := Parse(raw)
	if err == nil {
		t.Fatal("Parse unexpectedly succeeded")
	}
	if result != nil {
		t.Fatalf("Parse returned result=%#v, want nil", result)
	}
	if !strings.Contains(err.Error(), "no JSON found") {
		t.Fatalf("Parse error=%q, want missing JSON signal", err)
	}
}
