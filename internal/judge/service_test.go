package judge

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"goodkind.io/lm-review/api/judgepb"
)

func TestEvaluateReturnsDeciderVerdict(t *testing.T) {
	var capturedModel string
	var capturedSystem string
	var capturedUser string
	server := NewServerWithDecider(
		"test-model",
		func(_ context.Context, model string, system string, user string) (judgepb.Verdict, error) {
			capturedModel = model
			capturedSystem = system
			capturedUser = user
			return judgepb.Verdict_VERDICT_BLOCK, nil
		},
	)

	reply, err := server.Evaluate(context.Background(), &judgepb.JudgeRequest{
		InputText: "rg TODO .",
		RuleSetId: SearchGuardRuleSetID,
		Context: &judgepb.RuleContext{
			IndexedRoots: []string{"/Users/test/Sites/app"},
			Cwd:          "/Users/test/Sites/app",
			CwdIndexed:   "yes",
		},
	})
	if err != nil {
		t.Fatalf("Evaluate returned error: %v", err)
	}
	if reply.GetVerdict() != judgepb.Verdict_VERDICT_BLOCK {
		t.Fatalf("verdict=%s, want %s", reply.GetVerdict(), judgepb.Verdict_VERDICT_BLOCK)
	}
	if capturedModel != "test-model" {
		t.Fatalf("model=%q, want test-model", capturedModel)
	}
	requireContains(t, capturedSystem, "/Users/test/Sites/app")
	requireContains(t, capturedUser, "command:\nrg TODO .")
}

func TestEvaluateRejectsUnknownRuleSet(t *testing.T) {
	server := NewServerWithDecider(
		"test-model",
		func(_ context.Context, _ string, _ string, _ string) (judgepb.Verdict, error) {
			t.Fatal("decider should not be called")
			return judgepb.Verdict_VERDICT_UNSPECIFIED, nil
		},
	)

	_, err := server.Evaluate(context.Background(), &judgepb.JudgeRequest{
		InputText: "rg TODO .",
		RuleSetId: "missing",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("code=%s, want %s", status.Code(err), codes.InvalidArgument)
	}
}

func TestListRuleSetsIncludesRequiredContext(t *testing.T) {
	server := NewServer("test-model", "", "")

	reply, err := server.ListRuleSets(context.Background(), &judgepb.ListRuleSetsRequest{})
	if err != nil {
		t.Fatalf("ListRuleSets returned error: %v", err)
	}

	ruleSets := map[string][]string{}
	for _, descriptor := range reply.GetRuleSets() {
		ruleSets[descriptor.GetId()] = descriptor.GetRequiredContext()
	}
	requireDescriptorContext(t, ruleSets, SearchGuardRuleSetID, "indexed_roots")
	requireDescriptorContext(t, ruleSets, WorktreeGuardRuleSetID, "worktree")
}

func requireDescriptorContext(t *testing.T, ruleSets map[string][]string, id string, required string) {
	t.Helper()
	values, ok := ruleSets[id]
	if !ok {
		keys := make([]string, 0, len(ruleSets))
		for key := range ruleSets {
			keys = append(keys, key)
		}
		t.Fatalf("rule set %q missing from %s", id, strings.Join(keys, ", "))
	}
	requireContainsValue(t, values, required)
}
