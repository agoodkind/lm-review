// Package gitutil provides git operations used by the CLI and MCP server.
package gitutil

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Root returns the git repo root for the given directory.
// If dir is empty, uses the current working directory.
func Root(dir string) (string, error) {
	slog.Debug("gitutil.root.begin", "dir", dir)
	args := []string{"rev-parse", "--show-toplevel"}
	if dir != "" {
		args = append([]string{"-C", dir}, args...)
	}
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		slog.Warn("gitutil.root.failed", "dir", dir, "err", err)
		return "", fmt.Errorf("not in a git repo and no path provided. Pass path='/path/to/repo' to specify one")
	}
	root := strings.TrimSpace(string(out))
	slog.Debug("gitutil.root.completed", "dir", dir, "root", root)
	return root, nil
}

// StagedDiff returns the staged diff for the given repo root.
func StagedDiff(repoRoot string) (string, error) {
	slog.Debug("gitutil.staged_diff.begin", "repo_root", repoRoot)
	out, err := exec.Command("git", "-C", repoRoot, "diff", "--cached").Output()
	if err != nil {
		slog.Warn("gitutil.staged_diff.failed", "repo_root", repoRoot, "err", err)
		return "", fmt.Errorf("git diff --cached: %w", err)
	}
	diff := string(out)
	slog.Debug("gitutil.staged_diff.completed", "repo_root", repoRoot, "bytes", len(diff))
	return diff, nil
}

// PRDiff returns the diff between the current branch and main for the given repo root.
func PRDiff(repoRoot string) (string, error) {
	slog.Debug("gitutil.pr_diff.begin", "repo_root", repoRoot)
	out, err := exec.Command("git", "-C", repoRoot, "diff", "main...HEAD").Output()
	if err != nil {
		out, err = exec.Command("git", "-C", repoRoot, "diff", "origin/main...HEAD").Output()
		if err != nil {
			slog.Warn("gitutil.pr_diff.failed", "repo_root", repoRoot, "err", err)
			return "", fmt.Errorf("git diff vs main: %w", err)
		}
	}
	diff := string(out)
	slog.Debug("gitutil.pr_diff.completed", "repo_root", repoRoot, "bytes", len(diff))
	return diff, nil
}

// RepoSnapshot returns a concatenated string of all Go files in the repo,
// truncated at maxBytes to fit in LLM context. Pass 0 for no limit.
func RepoSnapshot(repoRoot string, maxBytes int) (string, error) {
	slog.Debug("gitutil.repo_snapshot.begin", "repo_root", repoRoot, "max_bytes", maxBytes)
	out, err := exec.Command("git", "-C", repoRoot, "ls-files", "*.go").Output()
	if err != nil {
		slog.Warn("gitutil.repo_snapshot.failed", "repo_root", repoRoot, "err", err)
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

	snapshot := sb.String()
	slog.Debug("gitutil.repo_snapshot.completed", "repo_root", repoRoot, "bytes", len(snapshot), "file_count", len(files))
	return snapshot, nil
}

// FilesSnapshot returns a concatenated string of the selected Go files from the
// repo, using git object content when possible and the working tree as a
// fallback for uncommitted files.
func FilesSnapshot(repoRoot string, files []string, maxBytes int) (string, error) {
	slog.Debug("gitutil.files_snapshot.begin", "repo_root", repoRoot, "file_count", len(files), "max_bytes", maxBytes)
	var sb strings.Builder

	for _, file := range files {
		file = filepath.Clean(file)
		if file == "" || file == "." || strings.HasPrefix(file, "..") || !strings.HasSuffix(file, ".go") {
			continue
		}

		content, readErr := exec.Command("git", "-C", repoRoot, "show", "HEAD:"+file).Output()
		if readErr != nil {
			content, readErr = exec.Command("git", "-C", repoRoot, "show", ":"+file).Output()
			if readErr != nil {
				content, readErr = os.ReadFile(filepath.Join(repoRoot, file))
				if readErr != nil {
					continue
				}
			}
		}

		entry := fmt.Sprintf("// FILE: %s\n%s\n\n", file, content)
		if maxBytes > 0 && sb.Len()+len(entry) > maxBytes {
			fmt.Fprintf(&sb, "// ... truncated at %d bytes\n", maxBytes)
			break
		}
		sb.WriteString(entry)
	}

	snapshot := sb.String()
	slog.Debug("gitutil.files_snapshot.completed", "repo_root", repoRoot, "bytes", len(snapshot))
	return snapshot, nil
}
