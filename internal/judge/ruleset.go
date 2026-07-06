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

type ConversationTurn struct {
	Role string
	Text string
}

type WorktreeState struct {
	PrimaryCheckout string
	DefaultBranch   string
	Worktrees       []Worktree
	CurrentWorktree string
	CurrentBranch   string
}

type Worktree struct {
	Path      string
	Branch    string
	IsPrimary bool
}

const (
	SearchGuardRuleSetID   = "search-guard"
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
