package lmstudio

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPreflightLoadUsesLMDLoadAPI(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/models/load" {
			t.Fatalf("path=%q, want /api/v1/models/load", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"type":"llm","instance_id":"test-model","load_time_seconds":0,"status":"estimated","can_load":true}`))
	}))
	defer server.Close()

	response, err := PreflightLoad(context.Background(), server.URL, "token", LMDLoadRequest{
		Model:          "test-model",
		ContextLength:  4096,
		EstimateOnly:   true,
		EchoLoadConfig: true,
	})
	if err != nil {
		t.Fatalf("PreflightLoad returned error: %v", err)
	}
	if response.Status != "estimated" {
		t.Fatalf("status=%q, want estimated", response.Status)
	}
	if captured["model"] != "test-model" {
		t.Fatalf("model=%v, want test-model", captured["model"])
	}
	if captured["context_length"] != float64(4096) {
		t.Fatalf("context_length=%v, want 4096", captured["context_length"])
	}
	if captured["estimate_only"] != true {
		t.Fatalf("estimate_only=%v, want true", captured["estimate_only"])
	}
	if captured["echo_load_config"] != true {
		t.Fatalf("echo_load_config=%v, want true", captured["echo_load_config"])
	}
}
