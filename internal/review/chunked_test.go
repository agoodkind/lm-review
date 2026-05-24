package review

import (
	"context"
	"strings"
	"testing"
)

type recordingChatClient struct {
	userMessages []string
}

func (c *recordingChatClient) Chat(_ context.Context, _ string, userMessage string) (string, error) {
	c.userMessages = append(c.userMessages, userMessage)
	return `{"verdict":"pass","summary":"ok","issues":[],"stats":{"errors":0,"warnings":0,"infos":0}}`, nil
}

func (c *recordingChatClient) ModelID() string {
	return "test-model"
}

func TestChunkedDiffReviewSplitsLargeDiff(t *testing.T) {
	diff := strings.Join([]string{
		"diff --git a/a.go b/a.go\n--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-old\n+new\n",
		"diff --git a/b.go b/b.go\n--- a/b.go\n+++ b/b.go\n@@ -1 +1 @@\n-old\n+new\n",
	}, "")
	client := &recordingChatClient{}

	result, err := ChunkedReview(
		context.Background(),
		client,
		diff,
		"diff",
		nil,
		BuildQuickSystemPrompt,
		DiffPrompt,
		DiffChunkPrompt,
		SplitDiffInput,
		len(diff)/2,
		1,
	)
	if err != nil {
		t.Fatalf("ChunkedReview returned error: %v", err)
	}

	if result.Verdict != VerdictPass {
		t.Fatalf("verdict=%q, want %q", result.Verdict, VerdictPass)
	}
	if len(client.userMessages) < 2 {
		t.Fatalf("chat calls=%d, want at least 2", len(client.userMessages))
	}
	for _, userMessage := range client.userMessages {
		if !strings.Contains(userMessage, "Review chunk") {
			t.Fatalf("chunk user message missing chunk prompt:\n%s", userMessage)
		}
	}
}

func TestSplitRepoAndDiffInputHardSplitOversizedParts(t *testing.T) {
	repoChunks := SplitRepoInput("// FILE: huge.go\n"+strings.Repeat("a", 40), 20)
	if len(repoChunks) < 2 {
		t.Fatalf("repo chunks=%d, want at least 2", len(repoChunks))
	}

	diffChunks := SplitDiffInput("diff --git a/a.go b/a.go\n"+strings.Repeat("b", 40), 20)
	if len(diffChunks) < 2 {
		t.Fatalf("diff chunks=%d, want at least 2", len(diffChunks))
	}
}
