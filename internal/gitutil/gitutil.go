// Package gitutil provides git operations used by the CLI and MCP server.
package gitutil

import (
	"fmt"
	"os/exec"
	"strings"
)

// Root returns the git repo root for the given directory.
// If dir is empty, uses the current working directory.
func Root(dir string) (string, error) {
	args := []string{"rev-parse", "--show-toplevel"}
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		return "", fmt.Errorf("not in a git repo and no path provided. Pass path='/path/to/repo' to specify one")
	}
	return strings.TrimSpace(string(out)), nil
}

// StagedDiff returns the staged diff for the given repo root.
func StagedDiff(repoRoot string) (string, error) {
	out, err := exec.Command("git", "-C", repoRoot, "diff", "--cached").Output()
	if err != nil {
		return "", fmt.Errorf("git diff --cached: %w", err)
	}
	return string(out), nil
}

// PRDiff returns the diff between the current branch and main for the given repo root.
func PRDiff(repoRoot string) (string, error) {
	out, err := exec.Command("git", "-C", repoRoot, "diff", "main...HEAD").Output()
	if err != nil {
		out, err = exec.Command("git", "-C", repoRoot, "diff", "origin/main...HEAD").Output()
		if err != nil {
			return "", fmt.Errorf("git diff vs main: %w", err)
		}
	}
	return string(out), nil
}

// RepoSnapshot returns a concatenated string of all Go files in the repo,
// truncated at maxBytes to fit in LLM context. Pass 0 for no limit.
func RepoSnapshot(repoRoot string, maxBytes int) (string, error) {
	out, err := exec.Command("git", "-C", repoRoot, "ls-files", "*.go").Output()
	if err != nil {
		return "", fmt.Errorf("git ls-files: %w", err)
	}

	files := strings.Split(strings.TrimSpace(string(out)), "\n")
	var sb strings.Builder

	for _, f := range files {
		if f == "" {
			continue
		}
		content, readErr := exec.Command("git", "-C", repoRoot, "show", "HEAD:"+f).Output()
		if readErr != nil {
			continue
		}
		entry := fmt.Sprintf("// FILE: %s\n%s\n\n", f, content)
		if maxBytes > 0 && sb.Len()+len(entry) > maxBytes {
			fmt.Fprintf(&sb, "// ... truncated at %d bytes\n", maxBytes)
			break
		}
		sb.WriteString(entry)
	}

	return sb.String(), nil
}
