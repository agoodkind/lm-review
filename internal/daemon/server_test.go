package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"

	"goodkind.io/lm-review/api/reviewpb"
	"goodkind.io/lm-review/internal/analyzer"
	"goodkind.io/lm-review/internal/config"
	"goodkind.io/lm-review/internal/review"
)

func TestRawStaticResultMapsFindings(t *testing.T) {
	result := rawStaticResult([]analyzer.Finding{
		{Tool: "staticcheck", Check: "SA4006", Severity: analyzer.SeverityWarning, File: "a.go", Line: 3, Message: "value assigned but never used", Fix: "remove assignment"},
		{Tool: "custom", Check: "slog_error_without_err", Severity: analyzer.SeverityError, File: "b.go", Line: 5, Message: "missing err"},
	}, []error{nil})

	if result.Verdict != review.VerdictBlock {
		t.Fatalf("verdict=%q, want %q", result.Verdict, review.VerdictBlock)
	}
	if len(result.Issues) != 2 {
		t.Fatalf("issues=%d, want 2", len(result.Issues))
	}
	if result.Issues[0].Rule != "SA4006" {
		t.Fatalf("first rule=%q, want SA4006", result.Issues[0].Rule)
	}
	if result.Stats.Errors != 1 || result.Stats.Warnings != 1 {
		t.Fatalf("stats=%+v, want 1 error and 1 warning", result.Stats)
	}
}

func TestReviewRequestHasNoPayloadFields(t *testing.T) {
	fields := reviewpb.File_review_proto.Messages().ByName("ReviewRequest").Fields()
	for _, fieldName := range []protoreflect.Name{"diff", "context"} {
		if fields.ByName(fieldName) != nil {
			t.Fatalf("ReviewRequest still has payload field %q", fieldName)
		}
	}
}

func TestOpenAICompatBuildClientUsesConfiguredEndpoint(t *testing.T) {
	server := &Server{cfg: &config.Config{
		OpenAICompat: config.OpenAICompat{
			URL:        "http://127.0.0.1:1",
			Token:      "test-token",
			QuickModel: "missing-test-model",
		},
	}}

	client, model := server.buildClient("diff", "quick", "")

	if client == nil {
		t.Fatal("client is nil, want configured OpenAI-compatible client")
	}
	if model != "missing-test-model" {
		t.Fatalf("model=%q, want missing-test-model", model)
	}
}

func TestOpenAICompatBuildClientIgnoresInferenceBackendOverride(t *testing.T) {
	var authorization string
	var requestBody map[string]any
	globalBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		authorization = request.Header.Get("Authorization")
		if err := json.NewDecoder(request.Body).Decode(&requestBody); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-review","object":"chat.completion","created":0,"model":"global-model","choices":[{"index":0,"message":{"role":"assistant","content":"{\"verdict\":\"pass\",\"summary\":\"ok\",\"issues\":[]}"},"finish_reason":"stop"}]}`))
	}))
	defer globalBackend.Close()

	var inferenceCalls int
	inferenceBackend := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		inferenceCalls++
	}))
	defer inferenceBackend.Close()

	server := &Server{cfg: &config.Config{
		OpenAICompat: config.OpenAICompat{
			URL:       globalBackend.URL,
			Token:     "global-token",
			FastModel: "global-model",
		},
		Inference: config.Inference{
			URL:   inferenceBackend.URL,
			Token: "inference-token",
		},
	}}
	client, _ := server.buildClient("diff", "normal", "")

	if _, err := client.Chat(context.Background(), "system", "input"); err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}
	if inferenceCalls != 0 {
		t.Fatalf("inference backend calls=%d, want 0", inferenceCalls)
	}
	if authorization != "Bearer global-token" {
		t.Fatalf("authorization=%q, want global credential", authorization)
	}
	if requestBody["model"] != "global-model" {
		t.Fatalf("model=%v, want global-model", requestBody["model"])
	}
}

func TestPrepareReviewInputModes(t *testing.T) {
	t.Run("staged", func(t *testing.T) {
		repoRoot := initDaemonTestRepo(t)
		writeDaemonTestFile(t, repoRoot, "review.go", "package main\n\nfunc value() string { return \"old\" }\n")
		runDaemonGit(t, repoRoot, "add", "review.go")
		runDaemonGit(t, repoRoot, "commit", "-m", "Add review file")
		writeDaemonTestFile(t, repoRoot, "review.go", "package main\n\nfunc value() string { return \"new\" }\n")
		runDaemonGit(t, repoRoot, "add", "review.go")

		prepared := prepareDaemonReviewInput(t, repoRoot, reviewpb.ReviewMode_REVIEW_MODE_STAGED_DIFF, "")

		if prepared.scope != "diff" || prepared.selectedModeLog != "staged" {
			t.Fatalf("prepared=%+v, want staged diff scope", prepared)
		}
		if !strings.Contains(prepared.input, "+func value() string { return \"new\" }") {
			t.Fatalf("staged input missing new value:\n%s", prepared.input)
		}
	})

	t.Run("worktree", func(t *testing.T) {
		repoRoot := initDaemonTestRepo(t)
		runDaemonGit(t, repoRoot, "commit", "--allow-empty", "-m", "Initial commit")
		writeDaemonTestFile(t, repoRoot, "worktree.go", "package main\n\nfunc worktree() {}\n")

		prepared := prepareDaemonReviewInput(t, repoRoot, reviewpb.ReviewMode_REVIEW_MODE_WORKTREE_DIFF, "")

		if prepared.scope != "diff" || prepared.selectedModeLog != "worktree" {
			t.Fatalf("prepared=%+v, want worktree diff scope", prepared)
		}
		if !strings.Contains(prepared.input, "diff --git a/worktree.go b/worktree.go") {
			t.Fatalf("worktree input missing untracked file diff:\n%s", prepared.input)
		}
	})

	t.Run("pr", func(t *testing.T) {
		repoRoot := initDaemonTestRepo(t)
		writeDaemonTestFile(t, repoRoot, "review.go", "package main\n\nfunc value() string { return \"base\" }\n")
		runDaemonGit(t, repoRoot, "add", "review.go")
		runDaemonGit(t, repoRoot, "commit", "-m", "Add base file")
		runDaemonGit(t, repoRoot, "checkout", "-b", "feature")
		writeDaemonTestFile(t, repoRoot, "review.go", "package main\n\nfunc value() string { return \"feature\" }\n")
		runDaemonGit(t, repoRoot, "add", "review.go")
		runDaemonGit(t, repoRoot, "commit", "-m", "Update review file")

		prepared := prepareDaemonReviewInput(t, repoRoot, reviewpb.ReviewMode_REVIEW_MODE_PR, "main")

		if prepared.scope != "diff" || prepared.selectedModeLog != "pr" {
			t.Fatalf("prepared=%+v, want pr diff scope", prepared)
		}
		if !strings.Contains(prepared.input, "+func value() string { return \"feature\" }") {
			t.Fatalf("pr input missing feature branch change:\n%s", prepared.input)
		}
	})

	t.Run("repo", func(t *testing.T) {
		repoRoot := initDaemonTestRepo(t)
		writeDaemonTestFile(t, repoRoot, "review.go", "package main\n\nfunc value() string { return \"repo\" }\n")
		runDaemonGit(t, repoRoot, "add", "review.go")
		runDaemonGit(t, repoRoot, "commit", "-m", "Add review file")

		prepared := prepareDaemonReviewInput(t, repoRoot, reviewpb.ReviewMode_REVIEW_MODE_REPO, "")

		if prepared.scope != "repo" || prepared.selectedModeLog != "repo" {
			t.Fatalf("prepared=%+v, want repo scope", prepared)
		}
		if !strings.Contains(prepared.input, "// FILE: review.go") {
			t.Fatalf("repo input missing file marker:\n%s", prepared.input)
		}
	})
}

func TestPrepareReviewInputAutoSelection(t *testing.T) {
	t.Run("prefers staged", func(t *testing.T) {
		repoRoot := initDaemonTestRepo(t)
		writeDaemonTestFile(t, repoRoot, "review.go", "package main\n\nfunc value() string { return \"old\" }\n")
		runDaemonGit(t, repoRoot, "add", "review.go")
		runDaemonGit(t, repoRoot, "commit", "-m", "Add review file")
		writeDaemonTestFile(t, repoRoot, "review.go", "package main\n\nfunc value() string { return \"staged\" }\n")
		runDaemonGit(t, repoRoot, "add", "review.go")
		writeDaemonTestFile(t, repoRoot, "worktree.go", "package main\n\nfunc worktree() {}\n")

		prepared := prepareDaemonReviewInput(t, repoRoot, reviewpb.ReviewMode_REVIEW_MODE_AUTO, "")

		if prepared.selectedModeLog != "auto-staged" {
			t.Fatalf("mode=%q, want auto-staged", prepared.selectedModeLog)
		}
		if strings.Contains(prepared.input, "worktree.go") {
			t.Fatalf("auto staged input should not include worktree fallback:\n%s", prepared.input)
		}
	})

	t.Run("falls back to worktree", func(t *testing.T) {
		repoRoot := initDaemonTestRepo(t)
		runDaemonGit(t, repoRoot, "commit", "--allow-empty", "-m", "Initial commit")
		writeDaemonTestFile(t, repoRoot, "worktree.go", "package main\n\nfunc worktree() {}\n")

		prepared := prepareDaemonReviewInput(t, repoRoot, reviewpb.ReviewMode_REVIEW_MODE_AUTO, "")

		if prepared.selectedModeLog != "auto-worktree" {
			t.Fatalf("mode=%q, want auto-worktree", prepared.selectedModeLog)
		}
	})

	t.Run("falls back to pr", func(t *testing.T) {
		repoRoot := initDaemonTestRepo(t)
		writeDaemonTestFile(t, repoRoot, "review.go", "package main\n\nfunc value() string { return \"base\" }\n")
		runDaemonGit(t, repoRoot, "add", "review.go")
		runDaemonGit(t, repoRoot, "commit", "-m", "Add base file")
		runDaemonGit(t, repoRoot, "checkout", "-b", "feature")
		writeDaemonTestFile(t, repoRoot, "review.go", "package main\n\nfunc value() string { return \"feature\" }\n")
		runDaemonGit(t, repoRoot, "add", "review.go")
		runDaemonGit(t, repoRoot, "commit", "-m", "Update review file")

		server := &Server{}
		prepared, err := server.prepareReviewInput(context.Background(), &reviewpb.ReviewRequest{
			Mode:    reviewpb.ReviewMode_REVIEW_MODE_AUTO,
			Path:    repoRoot,
			BaseRef: "main",
		})
		if err != nil {
			t.Fatalf("prepareReviewInput returned error: %v", err)
		}

		if prepared.selectedModeLog != "auto-pr" {
			t.Fatalf("mode=%q, want auto-pr", prepared.selectedModeLog)
		}
	})

	t.Run("falls back to repo", func(t *testing.T) {
		repoRoot := initDaemonTestRepo(t)
		writeDaemonTestFile(t, repoRoot, "review.go", "package main\n\nfunc value() string { return \"repo\" }\n")
		runDaemonGit(t, repoRoot, "add", "review.go")
		runDaemonGit(t, repoRoot, "commit", "-m", "Add review file")

		server := &Server{}
		prepared, err := server.prepareReviewInput(context.Background(), &reviewpb.ReviewRequest{
			Mode:    reviewpb.ReviewMode_REVIEW_MODE_AUTO,
			Path:    repoRoot,
			BaseRef: "main",
		})
		if err != nil {
			t.Fatalf("prepareReviewInput returned error: %v", err)
		}

		if prepared.selectedModeLog != "auto-repo" {
			t.Fatalf("mode=%q, want auto-repo", prepared.selectedModeLog)
		}
	})
}

func prepareDaemonReviewInput(t *testing.T, repoRoot string, mode reviewpb.ReviewMode, baseRef string) *preparedReviewInput {
	t.Helper()

	server := &Server{}
	prepared, err := server.prepareReviewInput(context.Background(), &reviewpb.ReviewRequest{
		Mode:    mode,
		Path:    repoRoot,
		BaseRef: baseRef,
	})
	if err != nil {
		t.Fatalf("prepareReviewInput returned error: %v", err)
	}
	return prepared
}

func initDaemonTestRepo(t *testing.T) string {
	t.Helper()

	repoRoot := t.TempDir()
	runDaemonGit(t, repoRoot, "init")
	runDaemonGit(t, repoRoot, "branch", "-M", "main")
	runDaemonGit(t, repoRoot, "config", "user.email", "alex@goodkind.io")
	runDaemonGit(t, repoRoot, "config", "user.name", "Alexander Goodkind")
	return repoRoot
}

func writeDaemonTestFile(t *testing.T, repoRoot string, name string, content string) {
	t.Helper()

	path := filepath.Join(repoRoot, name)
	err := os.MkdirAll(filepath.Dir(path), 0o755)
	if err != nil {
		t.Fatalf("create parent directory for %s: %v", name, err)
	}
	err = os.WriteFile(path, []byte(content), 0o644)
	if err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func runDaemonGit(t *testing.T, repoRoot string, args ...string) string {
	t.Helper()

	gitArgs := append([]string{"-C", repoRoot}, args...)
	cmd := exec.CommandContext(context.Background(), "git", gitArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}
