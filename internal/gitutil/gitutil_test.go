package gitutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepoSnapshotReadsModifiedTrackedFile(t *testing.T) {
	repoRoot := initTestRepo(t)
	writeFile(t, repoRoot, "review.go", "package main\n\nfunc value() string { return \"old\" }\n")
	runGit(t, repoRoot, "add", "review.go")
	runGit(t, repoRoot, "commit", "-m", "Add review file")
	writeFile(t, repoRoot, "review.go", "package main\n\nfunc value() string { return \"new\" }\n")

	snapshot, err := RepoSnapshot(repoRoot, 0)
	if err != nil {
		t.Fatalf("RepoSnapshot returned error: %v", err)
	}

	if !strings.Contains(snapshot, "func value() string { return \"new\" }") {
		t.Fatalf("snapshot does not include working tree content:\n%s", snapshot)
	}
	if strings.Contains(snapshot, "func value() string { return \"old\" }") {
		t.Fatalf("snapshot includes stale HEAD content:\n%s", snapshot)
	}
}

func TestRepoSnapshotIncludesUntrackedGoFile(t *testing.T) {
	repoRoot := initTestRepo(t)
	runGit(t, repoRoot, "commit", "--allow-empty", "-m", "Initial commit")
	writeFile(t, repoRoot, "untracked.go", "package main\n\nfunc untracked() {}\n")

	snapshot, err := RepoSnapshot(repoRoot, 0)
	if err != nil {
		t.Fatalf("RepoSnapshot returned error: %v", err)
	}

	if !strings.Contains(snapshot, "// FILE: untracked.go") {
		t.Fatalf("snapshot does not include untracked file name:\n%s", snapshot)
	}
	if !strings.Contains(snapshot, "func untracked() {}") {
		t.Fatalf("snapshot does not include untracked file content:\n%s", snapshot)
	}
}

func TestWorktreeDiffIncludesUntrackedGoFile(t *testing.T) {
	repoRoot := initTestRepo(t)
	runGit(t, repoRoot, "commit", "--allow-empty", "-m", "Initial commit")
	writeFile(t, repoRoot, "untracked.go", "package main\n\nfunc untracked() {}\n")

	diff, err := WorktreeDiff(repoRoot)
	if err != nil {
		t.Fatalf("WorktreeDiff returned error: %v", err)
	}

	for _, want := range []string{
		"diff --git a/untracked.go b/untracked.go",
		"--- /dev/null",
		"+++ b/untracked.go",
		"+package main",
		"+func untracked() {}",
	} {
		if !strings.Contains(diff, want) {
			t.Fatalf("diff does not contain %q:\n%s", want, diff)
		}
	}
}

func TestIgnoredUntrackedGoFileExcluded(t *testing.T) {
	repoRoot := initTestRepo(t)
	writeFile(t, repoRoot, ".gitignore", "ignored.go\n")
	runGit(t, repoRoot, "add", ".gitignore")
	runGit(t, repoRoot, "commit", "-m", "Add gitignore")
	writeFile(t, repoRoot, "ignored.go", "package main\n\nfunc ignored() {}\n")

	snapshot, err := RepoSnapshot(repoRoot, 0)
	if err != nil {
		t.Fatalf("RepoSnapshot returned error: %v", err)
	}
	if strings.Contains(snapshot, "ignored.go") {
		t.Fatalf("snapshot includes ignored file:\n%s", snapshot)
	}

	diff, err := WorktreeDiff(repoRoot)
	if err != nil {
		t.Fatalf("WorktreeDiff returned error: %v", err)
	}
	if strings.Contains(diff, "ignored.go") {
		t.Fatalf("diff includes ignored file:\n%s", diff)
	}
}

func TestPRDiffUsesExplicitBaseRef(t *testing.T) {
	repoRoot := initTestRepo(t)
	writeFile(t, repoRoot, "review.go", "package main\n\nfunc value() string { return \"base\" }\n")
	runGit(t, repoRoot, "add", "review.go")
	runGit(t, repoRoot, "commit", "-m", "Add base file")
	runGit(t, repoRoot, "checkout", "-b", "feature")
	writeFile(t, repoRoot, "review.go", "package main\n\nfunc value() string { return \"feature\" }\n")
	runGit(t, repoRoot, "add", "review.go")
	runGit(t, repoRoot, "commit", "-m", "Update review file")

	diff, err := PRDiff(repoRoot, "main")
	if err != nil {
		t.Fatalf("PRDiff returned error: %v", err)
	}

	if !strings.Contains(diff, "+func value() string { return \"feature\" }") {
		t.Fatalf("diff does not include feature branch change:\n%s", diff)
	}
	if !strings.Contains(diff, "-func value() string { return \"base\" }") {
		t.Fatalf("diff does not include base branch content:\n%s", diff)
	}
}

func initTestRepo(t *testing.T) string {
	t.Helper()

	repoRoot := t.TempDir()
	runGit(t, repoRoot, "init")
	runGit(t, repoRoot, "branch", "-M", "main")
	runGit(t, repoRoot, "config", "user.email", "alex@goodkind.io")
	runGit(t, repoRoot, "config", "user.name", "Alexander Goodkind")
	return repoRoot
}

func writeFile(t *testing.T, repoRoot string, name string, content string) {
	t.Helper()

	path := filepath.Join(repoRoot, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create parent directory for %s: %v", name, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func runGit(t *testing.T, repoRoot string, args ...string) string {
	t.Helper()

	gitArgs := append([]string{"-C", repoRoot}, args...)
	out, err := exec.Command("git", gitArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
