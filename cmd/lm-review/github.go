package main

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"goodkind.io/lm-review/api/reviewpb"
)

const markerPrefix = "<!-- lm-review:"

// postToGitHub upserts a living PR comment for the given scope.
// Silently skips if no PR is open.
func postToGitHub(scope string, resp *reviewpb.ReviewResponse) error {
	prNumber, err := currentPRNumber()
	if err != nil {
		return nil // no open PR, skip silently
	}

	body := renderMarkdown(scope, resp)
	marker := markerPrefix + scope + " -->"

	commentID, err := findComment(prNumber, marker)
	if err != nil || commentID == 0 {
		return createComment(prNumber, body)
	}

	return updateComment(commentID, body)
}

func renderMarkdown(scope string, resp *reviewpb.ReviewResponse) string {
	icon := map[string]string{"pass": "✅", "warn": "⚠️", "block": "🚫"}[resp.Verdict]
	label := map[string]string{"diff": "Fast Review", "pr": "PR Review", "repo": "Repo Health"}[scope]

	var sb strings.Builder
	fmt.Fprintf(&sb, "## 🤖 %s (%s, %dms)\n\n", label, resp.Model, resp.LatencyMs)
	fmt.Fprintf(&sb, "**Verdict:** %s %s\n\n%s\n", icon, strings.ToUpper(resp.Verdict), resp.Summary)

	if len(resp.Issues) > 0 {
		sb.WriteString("\n### Issues\n\n| Severity | File | Line | Rule | Message |\n|---|---|---|---|---|\n")
		for _, issue := range resp.Issues {
			sevIcon := map[string]string{"error": "🚫", "warning": "⚠️", "info": "ℹ️"}[issue.Severity]
			fmt.Fprintf(&sb, "| %s | `%s` | %d | %s | %s |\n",
				sevIcon, issue.File, issue.Line, issue.Rule, issue.Message)
		}
	}

	fmt.Fprintf(&sb, "\n%s%s -->\n", markerPrefix, scope)
	return sb.String()
}

func currentPRNumber() (int, error) {
	out, err := exec.Command("gh", "pr", "view", "--json", "number", "--jq", ".number").Output()
	if err != nil {
		return 0, fmt.Errorf("no open PR")
	}
	var n int
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(out))), &n); err != nil {
		return 0, err
	}
	return n, nil
}

func findComment(prNumber int, marker string) (int, error) {
	out, err := exec.Command("gh", "api",
		fmt.Sprintf("/repos/{owner}/{repo}/issues/%d/comments", prNumber),
		"--jq", fmt.Sprintf("[.[] | select(.body | contains(%q)) | .id] | first // 0", marker),
	).Output()
	if err != nil {
		return 0, err
	}
	var id int
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(out))), &id); err != nil {
		return 0, nil
	}
	return id, nil
}

func updateComment(commentID int, body string) error {
	out, err := exec.Command("gh", "api",
		fmt.Sprintf("/repos/{owner}/{repo}/issues/comments/%d", commentID),
		"--method", "PATCH", "--field", "body="+body,
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("update comment: %w: %s", err, out)
	}
	return nil
}

func createComment(prNumber int, body string) error {
	out, err := exec.Command("gh", "pr", "comment",
		fmt.Sprintf("%d", prNumber), "--body", body,
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("create comment: %w: %s", err, out)
	}
	return nil
}
