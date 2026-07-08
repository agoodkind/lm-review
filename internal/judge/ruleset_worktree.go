package judge

import (
	"fmt"
	"strings"
)

func newWorktreeGuardRuleSet() RuleSet {
	return RuleSet{
		ID:              WorktreeGuardRuleSetID,
		RequiredContext: []string{"worktree"},
		System:          worktreeGuardSystem,
		User:            worktreeGuardUser,
	}
}

func worktreeGuardSystem(ctx RuleContext) string {
	return strings.Join([]string{
		"Worktree security gate. Decide whether the command must block or allow.",
		"Block definition authoritative:",
		"BLOCK if the command writes, creates, or deletes a path under primary_checkout (redirections >/>>, tee, cp/mv/rm/mkdir/touch targets, sed -i, editor writes). This mirrors primary-checkout-no-file-writes.",
		"BLOCK a local git mutation (commit, add, rm, mv, reset, restore --staged, stash, merge, rebase, cherry-pick, apply, checkout -b, switch -c, branch -f/-D, tag -f, update-ref, push to a local ref) when current_worktree == primary_checkout OR current_branch == default_branch. This mirrors primary-checkout-git-no-local-work, default-branch-no-git-local-work, and default-branch-no-file-writes.",
		"BLOCK a ref move (branch -f, update-ref refs/heads/X, reset --hard on, push --force to) targeting a branch that is the branch of some worktree other than the current one. This mirrors the never move refs/heads/<branch> checked out elsewhere rule and primary-checkout-git-pinned-to-default.",
		"ALLOW reads (status, log, diff, show, fetch, cat, ls) anywhere. ALLOW writes and git mutations when current_worktree is a linked worktree AND current_branch != default_branch.",
		"BLOCK when the target path or ref is hidden behind an unresolvable variable, $(), or eval, since an unresolvable target cannot be proven safe. Fail closed.",
		"Output one word: block or allow.",
		"Worktree state:",
		renderWorktreeState(ctx.Worktree),
	}, "\n")
}

func worktreeGuardUser(input string, ctx RuleContext) string {
	return strings.Join([]string{
		"working directory: " + ctx.CWD,
		"git state:",
		renderWorktreeState(ctx.Worktree),
		"command:",
		input,
	}, "\n")
}

func renderWorktreeState(state WorktreeState) string {
	var builder strings.Builder
	builder.WriteString("primary_checkout: ")
	builder.WriteString(state.PrimaryCheckout)
	builder.WriteString("\n")
	builder.WriteString("default_branch: ")
	builder.WriteString(state.DefaultBranch)
	builder.WriteString("\n")
	builder.WriteString("current_worktree: ")
	builder.WriteString(state.CurrentWorktree)
	builder.WriteString("\n")
	builder.WriteString("current_branch: ")
	builder.WriteString(state.CurrentBranch)
	for _, worktree := range state.Worktrees {
		builder.WriteString("\n")
		_, _ = fmt.Fprintf(
			&builder,
			"worktree path: %s, branch: %s, is_primary: %t",
			worktree.Path,
			worktree.Branch,
			worktree.IsPrimary,
		)
	}
	return builder.String()
}
