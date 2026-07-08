package judge

// RuleSet describes one judge prompt contract and the context it needs.
type RuleSet struct {
	ID              string
	RequiredContext []string
	System          func(RuleContext) string
	User            func(input string, ctx RuleContext) string
}

// RuleContext mirrors judgepb.RuleContext without coupling prompt rendering to
// generated protobuf types.
type RuleContext struct {
	Conversation []ConversationTurn
	IndexedRoots []string
	Worktree     WorktreeState
	CWD          string
	CWDIndexed   string
}

// ConversationTurn is one recent conversation message passed to a judge rule set.
type ConversationTurn struct {
	Role string
	Text string
}

// WorktreeState describes the current checkout and all known linked worktrees.
type WorktreeState struct {
	PrimaryCheckout string
	DefaultBranch   string
	Worktrees       []Worktree
	CurrentWorktree string
	CurrentBranch   string
}

// Worktree describes one entry from the current git worktree state.
type Worktree struct {
	Path      string
	Branch    string
	IsPrimary bool
}

const (
	// SearchGuardRuleSetID is the rule-set id for indexed code search decisions.
	SearchGuardRuleSetID = "search-guard"
	// WorktreeGuardRuleSetID is the rule-set id for primary and default-branch guard decisions.
	WorktreeGuardRuleSetID = "worktree-guard"
)

// Registry returns the supported judge rule sets keyed by rule set id.
func Registry() map[string]RuleSet {
	searchGuard := newSearchGuardRuleSet()
	worktreeGuard := newWorktreeGuardRuleSet()
	return map[string]RuleSet{
		searchGuard.ID:   searchGuard,
		worktreeGuard.ID: worktreeGuard,
	}
}
