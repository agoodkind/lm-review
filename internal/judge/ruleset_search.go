package judge

import "strings"

const maxRootsInPrompt = 80

func newSearchGuardRuleSet() RuleSet {
	return RuleSet{
		ID:              SearchGuardRuleSetID,
		RequiredContext: []string{"indexed_roots"},
		System:          searchGuardSystem,
		User:            searchGuardUser,
	}
}

func searchGuardSystem(ctx RuleContext) string {
	roots := ctx.IndexedRoots
	if len(roots) > maxRootsInPrompt {
		roots = roots[:maxRootsInPrompt]
	}
	rootBlock := strings.Join(roots, "\n")
	return "Security gate. Indexed codebases:\n" + rootBlock + "\n" +
		"Block if the command searches file CONTENTS inside an indexed dir with a code search tool " +
		"(grep/rg/ag/ack/git grep/sed -n), incl. via $var, $(...), backticks, xargs, find -exec, eval, " +
		"or cd into one. Allow if it searches only outside, reads a stdin pipe, inspects output, or is " +
		"not a content search. Unknown target + indexed cwd -> block. Output one word: block or allow."
}

func searchGuardUser(input string, ctx RuleContext) string {
	return "working directory: " + ctx.CWD + "\n" +
		"working directory is inside an indexed codebase: " + ctx.CWDIndexed + "\n" +
		"command:\n" + input
}
