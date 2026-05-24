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

// PRDiff returns the diff between the current branch and the requested base.
func PRDiff(repoRoot string, baseRef string) (string, error) {
	slog.Debug("gitutil.pr_diff.begin", "repo_root", repoRoot, "base_ref", baseRef)
	fetchOutput, err := exec.Command("git", "-C", repoRoot, "fetch", "--all", "--prune").CombinedOutput()
	if err != nil {
		slog.Warn("gitutil.pr_diff.fetch_failed", "repo_root", repoRoot, "err", err)
		return "", fmt.Errorf("git fetch --all --prune: %w: %s", err, strings.TrimSpace(string(fetchOutput)))
	}

	base := strings.TrimSpace(baseRef)
	if base == "" {
		base, err = defaultPRBase(repoRoot)
		if err != nil {
			slog.Warn("gitutil.pr_diff.base_failed", "repo_root", repoRoot, "err", err)
			return "", err
		}
	}

	out, err := exec.Command("git", "-C", repoRoot, "diff", base+"...HEAD").Output()
	if err != nil {
		slog.Warn("gitutil.pr_diff.failed", "repo_root", repoRoot, "base_ref", base, "err", err)
		return "", fmt.Errorf("git diff %s...HEAD: %w", base, err)
	}
	diff := string(out)
	slog.Debug("gitutil.pr_diff.completed", "repo_root", repoRoot, "base_ref", base, "bytes", len(diff))
	return diff, nil
}

// WorktreeDiff returns tracked unstaged changes plus untracked non-ignored Go files.
func WorktreeDiff(repoRoot string) (string, error) {
	slog.Debug("gitutil.worktree_diff.begin", "repo_root", repoRoot)
	out, err := exec.Command("git", "-C", repoRoot, "diff").Output()
	if err != nil {
		slog.Warn("gitutil.worktree_diff.failed", "repo_root", repoRoot, "err", err)
		return "", fmt.Errorf("git diff: %w", err)
	}

	var sb strings.Builder
	diff := string(out)
	sb.WriteString(diff)
	if diff != "" && !strings.HasSuffix(diff, "\n") {
		sb.WriteString("\n")
	}

	untrackedFiles, err := gitGoFiles(repoRoot, "--others", "--exclude-standard")
	if err != nil {
		slog.Warn("gitutil.worktree_diff.untracked_failed", "repo_root", repoRoot, "err", err)
		return "", err
	}
	for _, file := range untrackedFiles {
		content, readErr := os.ReadFile(filepath.Join(repoRoot, file))
		if readErr != nil {
			return "", fmt.Errorf("read untracked file %s: %w", file, readErr)
		}
		sb.WriteString(syntheticNewFileDiff(file, content))
	}

	worktreeDiff := sb.String()
	slog.Debug("gitutil.worktree_diff.completed", "repo_root", repoRoot, "bytes", len(worktreeDiff))
	return worktreeDiff, nil
}

// RepoSnapshot returns a concatenated string of all Go files in the repo,
// truncated at maxBytes to fit in LLM context. Pass 0 for no limit.
func RepoSnapshot(repoRoot string, maxBytes int) (string, error) {
	slog.Debug("gitutil.repo_snapshot.begin", "repo_root", repoRoot, "max_bytes", maxBytes)
	trackedFiles, err := gitGoFiles(repoRoot)
	if err != nil {
		slog.Warn("gitutil.repo_snapshot.failed", "repo_root", repoRoot, "err", err)
		return "", err
	}
	untrackedFiles, err := gitGoFiles(repoRoot, "--others", "--exclude-standard")
	if err != nil {
		slog.Warn("gitutil.repo_snapshot.untracked_failed", "repo_root", repoRoot, "err", err)
		return "", err
	}

	files := appendUniqueFiles(trackedFiles, untrackedFiles)
	var sb strings.Builder

	for _, f := range files {
		if f == "" {
			continue
		}
		content, readErr := os.ReadFile(filepath.Join(repoRoot, f))
		if readErr != nil {
			if !os.IsNotExist(readErr) {
				return "", fmt.Errorf("read repo file %s: %w", f, readErr)
			}
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

func defaultPRBase(repoRoot string) (string, error) {
	slog.Debug("gitutil.default_pr_base.begin", "repo_root", repoRoot)
	for _, ref := range []string{"@{upstream}", "origin/main", "origin/master"} {
		if gitRefExists(repoRoot, ref) {
			slog.Debug("gitutil.default_pr_base.completed", "repo_root", repoRoot, "base_ref", ref)
			return ref, nil
		}
	}
	slog.Warn("gitutil.default_pr_base.failed", "repo_root", repoRoot)
	return "", fmt.Errorf("no PR base ref found; tried @{upstream}, origin/main, origin/master")
}

func gitRefExists(repoRoot string, ref string) bool {
	slog.Debug("gitutil.ref_exists.begin", "repo_root", repoRoot, "ref", ref)
	err := exec.Command("git", "-C", repoRoot, "rev-parse", "--verify", "--quiet", ref+"^{commit}").Run()
	exists := err == nil
	slog.Debug("gitutil.ref_exists.completed", "repo_root", repoRoot, "ref", ref, "exists", exists)
	return exists
}

func gitGoFiles(repoRoot string, args ...string) ([]string, error) {
	slog.Debug("gitutil.go_files.begin", "repo_root", repoRoot, "arg_count", len(args))
	gitArgs := []string{"-C", repoRoot, "ls-files", "-z"}
	gitArgs = append(gitArgs, args...)
	gitArgs = append(gitArgs, "--", "*.go")

	out, err := exec.Command("git", gitArgs...).Output()
	if err != nil {
		slog.Warn("gitutil.go_files.failed", "repo_root", repoRoot, "err", err)
		return nil, fmt.Errorf("git ls-files: %w", err)
	}
	files := splitNullOutput(out)
	slog.Debug("gitutil.go_files.completed", "repo_root", repoRoot, "file_count", len(files))
	return files, nil
}

func splitNullOutput(out []byte) []string {
	parts := strings.Split(string(out), "\x00")
	files := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			continue
		}
		files = append(files, part)
	}
	return files
}

func appendUniqueFiles(fileLists ...[]string) []string {
	seen := make(map[string]struct{})
	var files []string
	for _, fileList := range fileLists {
		for _, file := range fileList {
			if _, ok := seen[file]; ok {
				continue
			}
			seen[file] = struct{}{}
			files = append(files, file)
		}
	}
	return files
}

func syntheticNewFileDiff(file string, content []byte) string {
	text := string(content)
	lineCount := strings.Count(text, "\n")
	if text != "" && !strings.HasSuffix(text, "\n") {
		lineCount++
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "diff --git a/%s b/%s\n", file, file)
	sb.WriteString("new file mode 100644\n")
	sb.WriteString("index 0000000..0000000\n")
	sb.WriteString("--- /dev/null\n")
	fmt.Fprintf(&sb, "+++ b/%s\n", file)
	fmt.Fprintf(&sb, "@@ -0,0 +1,%d @@\n", lineCount)

	lines := strings.SplitAfter(text, "\n")
	for _, line := range lines {
		if line == "" {
			continue
		}
		sb.WriteString("+")
		sb.WriteString(line)
		if !strings.HasSuffix(line, "\n") {
			sb.WriteString("\n\\ No newline at end of file\n")
		}
	}
	return sb.String()
}
