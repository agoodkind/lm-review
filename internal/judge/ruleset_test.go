package judge

import (
	"strings"
	"testing"
)

func TestRegistryIncludesSearchGuard(t *testing.T) {
	ruleSet, ok := Registry()["search-guard"]
	if !ok {
		t.Fatal("search-guard missing from registry")
	}
	requireContainsValue(t, ruleSet.RequiredContext, "indexed_roots")

	ctx := RuleContext{
		IndexedRoots: []string{"/Users/test/Sites/app", "/Users/test/Sites/lib"},
		CWD:          "/Users/test/Sites/app/pkg",
		CWDIndexed:   "yes",
	}
	system := ruleSet.System(ctx)
	requireContains(t, system, "Security gate. Indexed codebases:")
	requireContains(t, system, "/Users/test/Sites/app\n/Users/test/Sites/lib")
	requireContains(t, system, "Unknown target + indexed cwd -> block")
	requireContains(t, system, "Output one word: block or allow.")

	user := ruleSet.User("rg TODO .", ctx)
	requireContains(t, user, "working directory: /Users/test/Sites/app/pkg")
	requireContains(t, user, "working directory is inside an indexed codebase: yes")
	requireContains(t, user, "command:\nrg TODO .")
}

func TestRegistryIncludesWorktreeGuard(t *testing.T) {
	ruleSet, ok := Registry()["worktree-guard"]
	if !ok {
		t.Fatal("worktree-guard missing from registry")
	}
	requireContainsValue(t, ruleSet.RequiredContext, "worktree")

	ctx := RuleContext{
		CWD: "/Users/test/Sites/app",
		Worktree: WorktreeState{
			PrimaryCheckout: "/Users/test/Sites/app",
			DefaultBranch:   "main",
			CurrentWorktree: "/Users/test/Sites/app",
			CurrentBranch:   "main",
			Worktrees: []Worktree{
				{Path: "/Users/test/Sites/app", Branch: "main", IsPrimary: true},
				{Path: "/Users/test/worktrees/app-feature", Branch: "feature", IsPrimary: false},
			},
		},
	}
	system := ruleSet.System(ctx)
	requireContains(t, system, "Block definition authoritative:")
	requireContains(t, system, "BLOCK if the command writes, creates, or deletes a path under primary_checkout")
	requireContains(t, system, "primary_checkout: /Users/test/Sites/app")
	requireContains(t, system, "worktree path: /Users/test/worktrees/app-feature")

	user := ruleSet.User("git commit -m test", ctx)
	requireContains(t, user, "working directory: /Users/test/Sites/app")
	requireContains(t, user, "git state:")
	requireContains(t, user, "current_branch: main")
	requireContains(t, user, "command:\ngit commit -m test")
}

func requireContains(t *testing.T, text string, want string) {
	t.Helper()
	if !strings.Contains(text, want) {
		t.Fatalf("text missing %q:\n%s", want, text)
	}
}

func requireContainsValue(t *testing.T, values []string, want string) {
	t.Helper()
	for _, value := range values {
		if value == want {
			return
		}
	}
	t.Fatalf("values=%v, want %q", values, want)
}
