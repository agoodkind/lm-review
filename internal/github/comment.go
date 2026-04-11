// Package github posts review results as living PR comments via the gh CLI.
package github

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

const markerPrefix = "<!-- lm-review:"

// UpsertComment finds the existing lm-review comment for the given scope
// and updates it, or creates a new one if none exists.
func UpsertComment(scope, body string) error {
	prNumber, err := currentPRNumber()
	if err != nil {
		// No open PR - silently skip.
		return nil
	}

	commentID, err := findComment(prNumber, scope)
	if err != nil {
		return fmt.Errorf("find comment: %w", err)
	}

	if commentID != 0 {
		return updateComment(commentID, body)
	}

	return createComment(prNumber, body)
}

func currentPRNumber() (int, error) {
	out, err := exec.Command("gh", "pr", "view", "--json", "number", "--jq", ".number").Output()
	if err != nil {
		return 0, fmt.Errorf("no open PR")
	}

	var number int
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(out))), &number); err != nil {
		return 0, fmt.Errorf("parse PR number: %w", err)
	}

	return number, nil
}

func findComment(prNumber int, scope string) (int, error) {
	marker := markerPrefix + scope + " -->"

	out, err := exec.Command("gh", "api",
		fmt.Sprintf("/repos/{owner}/{repo}/issues/%d/comments", prNumber),
		"--jq", fmt.Sprintf("[.[] | select(.body | contains(%q)) | .id] | first // 0", marker),
	).Output()
	if err != nil {
		return 0, fmt.Errorf("list comments: %w", err)
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
		"--method", "PATCH",
		"--field", "body="+body,
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("update comment: %w: %s", err, out)
	}

	return nil
}

func createComment(prNumber int, body string) error {
	out, err := exec.Command("gh", "pr", "comment",
		fmt.Sprintf("%d", prNumber),
		"--body", body,
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("create comment: %w: %s", err, out)
	}

	return nil
}
