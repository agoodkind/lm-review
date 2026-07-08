package judge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"goodkind.io/lm-review/api/judgepb"
)

func TestDecideParsesBlock(t *testing.T) {
	server := newChatCompletionServer(t, "block", http.StatusOK)
	defer server.Close()
	withModelBaseURL(t, server.URL)

	got, err := Decide(context.Background(), "test-model", "", "", "system", "user")
	if err != nil {
		t.Fatalf("Decide returned error: %v", err)
	}
	if got != judgepb.Verdict_VERDICT_BLOCK {
		t.Fatalf("verdict=%s, want %s", got, judgepb.Verdict_VERDICT_BLOCK)
	}
}

func TestDecideParsesAllowAtStartOfProse(t *testing.T) {
	server := newChatCompletionServer(t, "allow because this is a read", http.StatusOK)
	defer server.Close()
	withModelBaseURL(t, server.URL)

	got, err := Decide(context.Background(), "test-model", "", "", "system", "user")
	if err != nil {
		t.Fatalf("Decide returned error: %v", err)
	}
	if got != judgepb.Verdict_VERDICT_ALLOW {
		t.Fatalf("verdict=%s, want %s", got, judgepb.Verdict_VERDICT_ALLOW)
	}
}

func TestDecideReturnsErrorOnHTTPFailure(t *testing.T) {
	server := newChatCompletionServer(t, "", http.StatusInternalServerError)
	defer server.Close()
	withModelBaseURL(t, server.URL)

	_, err := Decide(context.Background(), "test-model", "", "", "system", "user")
	if err == nil {
		t.Fatal("Decide returned nil error, want non-nil error")
	}
}

func withModelBaseURL(t *testing.T, url string) {
	t.Helper()
	previous := lmdBaseURL
	lmdBaseURL = url
	t.Cleanup(func() {
		lmdBaseURL = previous
	})
}

type chatCompletionRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
	MaxTokens   int           `json:"max_tokens"`
}

type chatCompletionResponse struct {
	ID      string       `json:"id"`
	Object  string       `json:"object"`
	Created int64        `json:"created"`
	Model   string       `json:"model"`
	Choices []chatChoice `json:"choices"`
}

type chatChoice struct {
	Index        int         `json:"index"`
	Message      chatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func newChatCompletionServer(t *testing.T, content string, statusCode int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		if statusCode != http.StatusOK {
			http.Error(w, "model failed", statusCode)
			return
		}
		var request chatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		if request.Temperature != 0 {
			t.Fatalf("temperature=%v, want 0", request.Temperature)
		}
		if request.MaxTokens != decisionMaxTokens {
			t.Fatalf("max_tokens=%d, want %d", request.MaxTokens, decisionMaxTokens)
		}
		response := chatCompletionResponse{
			ID:      "chatcmpl-test",
			Object:  "chat.completion",
			Created: 0,
			Model:   request.Model,
			Choices: []chatChoice{
				{
					Index:        0,
					Message:      chatMessage{Role: "assistant", Content: content},
					FinishReason: "stop",
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(response); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
}
