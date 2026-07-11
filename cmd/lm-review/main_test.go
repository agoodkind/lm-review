package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"goodkind.io/gklog"
	"goodkind.io/lm-review/api/inferencepb"
	"goodkind.io/lm-review/internal/config"
)

func TestRootCommandExposesInferenceWithoutJudgeAlias(t *testing.T) {
	root := newRootCmd()
	if command, _, err := root.Find([]string{"inference"}); err != nil || command.Name() != "inference" {
		t.Fatalf("find inference command=%v err=%v", command, err)
	}
	if command, _, err := root.Find([]string{"judge"}); err == nil && command.Name() == "judge" {
		t.Fatalf("deprecated judge alias remains registered: %v", command)
	}
}

func TestNewInferenceServerUsesEffectiveBackendAndRequestModel(t *testing.T) {
	tests := []struct {
		name              string
		inferenceToken    string
		wantAuthorization string
	}{
		{name: "base URL only sends no credential", wantAuthorization: ""},
		{
			name:              "explicit inference token is sent",
			inferenceToken:    "inference-token",
			wantAuthorization: "Bearer inference-token",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var globalCalls atomic.Int32
			globalBackend := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				globalCalls.Add(1)
			}))
			defer globalBackend.Close()

			var authorization string
			requestBody := make(map[string]any)
			inferenceBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				authorization = request.Header.Get("Authorization")
				if err := json.NewDecoder(request.Body).Decode(&requestBody); err != nil {
					t.Errorf("decode request: %v", err)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"chatcmpl-test","object":"chat.completion","created":0,"model":"actual-model","choices":[{"index":0,"message":{"role":"assistant","content":"{\"decision\":\"allow\"}"},"finish_reason":"stop"}]}`))
			}))
			defer inferenceBackend.Close()

			cfg := &config.Config{
				OpenAICompat: config.OpenAICompat{
					URL:               globalBackend.URL,
					Token:             "global-secret-canary",
					MaxResponseTokens: 256,
				},
				Inference: config.Inference{
					BaseURL: inferenceBackend.URL,
					Token:   test.inferenceToken,
				},
			}
			server := newInferenceServer(cfg, "configured-model")
			request := &inferencepb.InferRequest{
				Prompt:       "Classify",
				Input:        "sample",
				OutputSchema: `{"type":"object","additionalProperties":false,"properties":{"decision":{"type":"string"}},"required":["decision"]}`,
				Model:        "request-model",
			}

			if _, err := server.Infer(context.Background(), request); err != nil {
				t.Fatalf("Infer returned error: %v", err)
			}
			if globalCalls.Load() != 0 {
				t.Fatalf("global backend calls=%d, want 0", globalCalls.Load())
			}
			if authorization != test.wantAuthorization {
				t.Fatalf("authorization=%q, want %q", authorization, test.wantAuthorization)
			}
			if requestBody["model"] != "request-model" {
				t.Fatalf("model=%v, want request-model", requestBody["model"])
			}
		})
	}
}

func TestInferenceServerErrorsAndLogsDoNotExposeCredential(t *testing.T) {
	const credential = "credential-canary-5a2"
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"` + credential + `"}}`))
	}))
	defer backend.Close()

	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	previousLogger := slog.Default()
	slog.SetDefault(logger)
	defer slog.SetDefault(previousLogger)
	ctx := gklog.WithLogger(context.Background(), logger)
	cfg := &config.Config{
		OpenAICompat: config.OpenAICompat{URL: "http://global.invalid", Token: "global-token"},
		Inference:    config.Inference{BaseURL: backend.URL, Token: credential, Model: "test-model"},
	}
	server := newInferenceServer(cfg, cfg.Inference.ResolveModel())
	request := &inferencepb.InferRequest{
		Prompt:       "Classify",
		Input:        "sample",
		OutputSchema: `{"type":"object"}`,
	}

	_, err := server.Infer(ctx, request)
	if err == nil {
		t.Fatal("Infer returned nil error, want backend failure")
	}
	combined := logs.String() + "\n" + err.Error()
	if strings.Contains(combined, credential) {
		t.Fatalf("logs or error expose credential: %s", combined)
	}
}

func TestWriteConfigDocumentsOptionalInferenceBackendWithoutDuplicatingCredential(t *testing.T) {
	const credential = "global-credential-canary"
	path := filepath.Join(t.TempDir(), "config.toml")

	if err := writeConfig(path, "https://global.example.test", credential, "fast", "deep"); err != nil {
		t.Fatalf("writeConfig returned error: %v", err)
	}
	contentBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	content := string(contentBytes)
	for _, line := range []string{
		`# base_url = "https://inference.example.com"`,
		`# token = "replace-with-inference-token"`,
	} {
		if !strings.Contains(content, line) {
			t.Fatalf("config does not document %q:\n%s", line, content)
		}
	}
	if count := strings.Count(content, credential); count != 1 {
		t.Fatalf("global credential occurrences=%d, want 1", count)
	}
}
