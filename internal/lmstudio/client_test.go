package lmstudio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"goodkind.io/gklog"
	"goodkind.io/lm-review/internal/config"
	"goodkind.io/lm-review/internal/requestmeta"
)

func TestChatReviewSendsJSONSchemaResponseFormat(t *testing.T) {
	requestBody := captureChatRequest(t, func(client *Client) error {
		_, err := client.ChatReview(context.Background(), "system prompt", "user prompt")
		return err
	})

	responseFormat := requireMap(t, requestBody["response_format"], "response_format")
	if got := responseFormat["type"]; got != "json_schema" {
		t.Fatalf("response_format.type=%v, want json_schema", got)
	}

	jsonSchema := requireMap(t, responseFormat["json_schema"], "response_format.json_schema")
	if got := jsonSchema["name"]; got != "review_result" {
		t.Fatalf("response_format.json_schema.name=%v, want review_result", got)
	}
	if got, ok := jsonSchema["strict"].(bool); !ok || !got {
		t.Fatalf("response_format.json_schema.strict=%v, want true", jsonSchema["strict"])
	}

	schema := requireMap(t, jsonSchema["schema"], "response_format.json_schema.schema")
	requiredFields := stringSet(t, schema["required"], "response_format.json_schema.schema.required")
	for _, field := range []string{"verdict", "summary", "issues"} {
		if !requiredFields[field] {
			t.Fatalf("schema.required missing %q", field)
		}
	}
	if requiredFields["stats"] {
		t.Fatal("schema.required unexpectedly contains stats")
	}

	properties := requireMap(t, schema["properties"], "response_format.json_schema.schema.properties")
	issues := requireMap(t, properties["issues"], "response_format.json_schema.schema.properties.issues")
	issueItems := requireMap(t, issues["items"], "response_format.json_schema.schema.properties.issues.items")
	issueRequired := stringSet(t, issueItems["required"], "response_format.json_schema.schema.properties.issues.items.required")
	for _, field := range []string{"severity", "file", "line", "rule", "message"} {
		if !issueRequired[field] {
			t.Fatalf("issue schema required missing %q", field)
		}
	}
}

func TestChatSchemaSendsCallerSchemaInStrictMode(t *testing.T) {
	const schema = `{"type":"object","additionalProperties":false,"properties":{"classification":{"type":"object","additionalProperties":false,"properties":{"breed":{"type":"string","enum":["Labrador","Poodle"]},"traits":{"type":"array","items":{"type":"string","enum":["friendly","active"]}}},"required":["breed","traits"]}},"required":["classification"]}`
	requestBody := captureChatRequest(t, func(client *Client) error {
		_, err := client.ChatSchema(context.Background(), "prompt", "input", json.RawMessage(schema))
		return err
	})

	responseFormat := requireMap(t, requestBody["response_format"], "response_format")
	if got := responseFormat["type"]; got != "json_schema" {
		t.Fatalf("response_format.type=%v, want json_schema", got)
	}
	jsonSchema := requireMap(t, responseFormat["json_schema"], "response_format.json_schema")
	if got := jsonSchema["name"]; got != "inference_output" {
		t.Fatalf("response_format.json_schema.name=%v, want inference_output", got)
	}
	if got, ok := jsonSchema["strict"].(bool); !ok || !got {
		t.Fatalf("response_format.json_schema.strict=%v, want true", jsonSchema["strict"])
	}
	wantSchema := requireMap(t, mustDecodeJSON(t, schema), "want schema")
	gotSchema := requireMap(t, jsonSchema["schema"], "response_format.json_schema.schema")
	if !reflect.DeepEqual(gotSchema, wantSchema) {
		t.Fatalf("schema=%#v, want %#v", gotSchema, wantSchema)
	}
	if got := requireFloat64(t, requestBody["max_tokens"], "max_tokens"); got != 256 {
		t.Fatalf("max_tokens=%v, want 256", got)
	}
	if _, ok := requestBody["max_completion_tokens"]; ok {
		t.Fatalf(
			"max_completion_tokens=%v, want omitted",
			requestBody["max_completion_tokens"],
		)
	}
}

func TestChatSchemaDetailedSendsReasoningOptionsAndReturnsMetadata(t *testing.T) {
	maxCompletionTokens := int64(2048)
	requestBody, result := captureDetailedChatRequest(t, SchemaGenerationOptions{
		ReasoningEffort:     "high",
		MaxCompletionTokens: &maxCompletionTokens,
		Temperature:         nil,
	})

	if got := requestBody["model"]; got != "gpt-5.4-mini" {
		t.Fatalf("model=%v, want gpt-5.4-mini", got)
	}
	if got := requestBody["reasoning_effort"]; got != "high" {
		t.Fatalf("reasoning_effort=%v, want high", got)
	}
	if got := requireFloat64(t, requestBody["max_completion_tokens"], "max_completion_tokens"); got != 2048 {
		t.Fatalf("max_completion_tokens=%v, want 2048", got)
	}
	if _, ok := requestBody["max_tokens"]; ok {
		t.Fatalf("max_tokens=%v, want omitted", requestBody["max_tokens"])
	}
	if _, ok := requestBody["temperature"]; ok {
		t.Fatalf("temperature=%v, want omitted", requestBody["temperature"])
	}
	if _, ok := requestBody["top_k"]; ok {
		t.Fatalf("top_k=%v, want omitted", requestBody["top_k"])
	}
	if _, ok := requestBody["repeat_penalty"]; ok {
		t.Fatalf("repeat_penalty=%v, want omitted", requestBody["repeat_penalty"])
	}
	if result.RequestID != "inference-test-single" || result.ActualModel != "gpt-5.4-mini-2026-07-01" {
		t.Fatalf("result identity=%#v", result)
	}
	if result.BackendFingerprint != "fp_test" || result.BackendVersion != "backend-2026.07" {
		t.Fatalf("result backend metadata=%#v", result)
	}
	if result.PromptTokens != 41 || result.CompletionTokens != 7 || result.TotalTokens != 48 {
		t.Fatalf("result usage=%#v", result)
	}
	if !result.PromptTokensPresent || !result.CompletionTokensPresent || !result.TotalTokensPresent {
		t.Fatal("result usage fields are not marked present")
	}
	if result.FinishReason != "stop" || result.Latency < 0 {
		t.Fatalf("result completion metadata=%#v", result)
	}
}

func TestChatSchemaDetailedSendsExplicitTemperature(t *testing.T) {
	temperature := 0.0
	requestBody, _ := captureDetailedChatRequest(t, SchemaGenerationOptions{
		Temperature: &temperature,
	})

	if got := requireFloat64(t, requestBody["temperature"], "temperature"); got != 0 {
		t.Fatalf("temperature=%v, want 0", got)
	}
	if got := requireFloat64(t, requestBody["max_completion_tokens"], "max_completion_tokens"); got != 8192 {
		t.Fatalf("max_completion_tokens=%v, want 8192", got)
	}
	if _, ok := requestBody["max_tokens"]; ok {
		t.Fatalf("max_tokens=%v, want omitted", requestBody["max_tokens"])
	}
}

func TestChatSchemaDetailedLeavesUnknownBackendMetadataAbsent(t *testing.T) {
	response := `{"id":"chatcmpl-inference","object":"chat.completion","created":0,"choices":[{"index":0,"message":{"role":"assistant","content":"{\"decision\":\"allow\"}"},"finish_reason":"stop"}]}`
	_, result := captureDetailedChatResponse(t, response, SchemaGenerationOptions{})

	if result.ActualModel != "" {
		t.Fatalf("actual_model=%q, want empty", result.ActualModel)
	}
	if result.PromptTokensPresent || result.CompletionTokensPresent || result.TotalTokensPresent {
		t.Fatal("usage fields are marked present when backend omitted them")
	}
}

func TestChatSchemaDetailedMarksExplicitZeroUsagePresent(t *testing.T) {
	response := `{"id":"chatcmpl-inference","object":"chat.completion","created":0,"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0},"choices":[{"index":0,"message":{"role":"assistant","content":"{\"decision\":\"allow\"}"},"finish_reason":"stop"}]}`
	_, result := captureDetailedChatResponse(t, response, SchemaGenerationOptions{})

	if !result.PromptTokensPresent || !result.CompletionTokensPresent || !result.TotalTokensPresent {
		t.Fatal("explicit zero usage fields are not marked present")
	}
	if result.PromptTokens != 0 || result.CompletionTokens != 0 || result.TotalTokens != 0 {
		t.Fatalf("result usage=%#v, want explicit zero values", result)
	}
}

func TestParseTokenUsagePresenceTreatsNullFieldsAsAbsent(t *testing.T) {
	tests := []struct {
		name           string
		response       string
		wantPrompt     bool
		wantCompletion bool
		wantTotal      bool
	}{
		{
			name:           "null prompt tokens",
			response:       `{"usage":{"prompt_tokens":null,"completion_tokens":0,"total_tokens":0}}`,
			wantPrompt:     false,
			wantCompletion: true,
			wantTotal:      true,
		},
		{
			name:           "null completion tokens",
			response:       `{"usage":{"prompt_tokens":0,"completion_tokens":null,"total_tokens":0}}`,
			wantPrompt:     true,
			wantCompletion: false,
			wantTotal:      true,
		},
		{
			name:           "null total tokens",
			response:       `{"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":null}}`,
			wantPrompt:     true,
			wantCompletion: true,
			wantTotal:      false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			presence := parseTokenUsagePresence(test.response)

			if presence.prompt != test.wantPrompt ||
				presence.completion != test.wantCompletion ||
				presence.total != test.wantTotal {
				t.Fatalf("presence=%+v, want prompt=%v completion=%v total=%v",
					presence,
					test.wantPrompt,
					test.wantCompletion,
					test.wantTotal,
				)
			}
		})
	}
}

func TestChatSchemaSanitizesBackendErrorLogsAndReturn(t *testing.T) {
	secrets := []string{
		"prompt-secret-8e31",
		"input-secret-a7c2",
		"context-secret-420d",
		"schema-secret-9b14",
		"output-secret-f651",
	}
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":{"message":"` + strings.Join(secrets, " ") + `"}}`))
	}))
	defer backend.Close()

	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	ctx := gklog.WithLogger(context.Background(), logger)
	client := New(backend.URL, "test-token", "test-model", 256, time.Second, config.ChatSettings{})
	schema := json.RawMessage(`{"type":"object","description":"schema-secret-9b14"}`)
	_, err := client.ChatSchema(
		ctx,
		"prompt-secret-8e31",
		"input-secret-a7c2\ncontext-secret-420d",
		schema,
	)
	if err == nil {
		t.Fatal("ChatSchema returned nil error, want failure")
	}

	combined := logs.String() + "\n" + err.Error()
	for _, secret := range secrets {
		if strings.Contains(combined, secret) {
			t.Fatalf("logs or error contain %q: %s", secret, combined)
		}
	}
}

func TestChatPreservesContextCancellationAndDeadline(t *testing.T) {
	tests := []struct {
		name string
		ctx  func() context.Context
		want error
	}{
		{name: "canceled", ctx: canceledChatContext, want: context.Canceled},
		{name: "deadline", ctx: expiredChatContext, want: context.DeadlineExceeded},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := New("http://[::1]:1", "test-token", "test-model", 256, time.Second, config.ChatSettings{})
			_, err := client.Chat(test.ctx(), "prompt", "input")
			if !errors.Is(err, test.want) {
				t.Fatalf("error=%v, want errors.Is(%v)", err, test.want)
			}
		})
	}
}

func TestChatOmitsResponseFormatByDefault(t *testing.T) {
	requestBody := captureChatRequest(t, func(client *Client) error {
		_, err := client.Chat(context.Background(), "system prompt", "user prompt")
		return err
	})

	if _, ok := requestBody["response_format"]; ok {
		t.Fatalf("response_format=%v, want omitted", requestBody["response_format"])
	}
}

func TestChatSendsConfiguredInferenceKnobs(t *testing.T) {
	topP := 0.9
	topK := 40
	presencePenalty := 0.2
	frequencyPenalty := 0.3
	repeatPenalty := 1.05
	seed := int64(99)
	settings := config.ChatSettings{
		Temperature:      0,
		TopP:             &topP,
		TopK:             &topK,
		PresencePenalty:  &presencePenalty,
		FrequencyPenalty: &frequencyPenalty,
		RepeatPenalty:    &repeatPenalty,
		Seed:             &seed,
		Stop:             []string{"DONE", "STOP"},
	}

	requestBody := captureChatRequestWithSettings(t, settings, func(client *Client) error {
		_, err := client.Chat(context.Background(), "system prompt", "user prompt")
		return err
	})

	if got := requireFloat64(t, requestBody["temperature"], "temperature"); got != 0 {
		t.Fatalf("temperature=%v, want 0", got)
	}
	if got := requireFloat64(t, requestBody["top_p"], "top_p"); got != topP {
		t.Fatalf("top_p=%v, want %v", got, topP)
	}
	if got := requireFloat64(t, requestBody["presence_penalty"], "presence_penalty"); got != presencePenalty {
		t.Fatalf("presence_penalty=%v, want %v", got, presencePenalty)
	}
	if got := requireFloat64(t, requestBody["frequency_penalty"], "frequency_penalty"); got != frequencyPenalty {
		t.Fatalf("frequency_penalty=%v, want %v", got, frequencyPenalty)
	}
	if got := requireFloat64(t, requestBody["repeat_penalty"], "repeat_penalty"); got != repeatPenalty {
		t.Fatalf("repeat_penalty=%v, want %v", got, repeatPenalty)
	}
	if got := requireFloat64(t, requestBody["seed"], "seed"); got != float64(seed) {
		t.Fatalf("seed=%v, want %d", got, seed)
	}
	if got := requireFloat64(t, requestBody["top_k"], "top_k"); got != float64(topK) {
		t.Fatalf("top_k=%v, want %d", got, topK)
	}
	stopValues, ok := requestBody["stop"].([]any)
	if !ok || len(stopValues) != 2 {
		t.Fatalf("stop=%T %#v, want 2 stop values", requestBody["stop"], requestBody["stop"])
	}
}

func captureChatRequest(t *testing.T, invoke func(*Client) error) map[string]any {
	t.Helper()
	return captureChatRequestWithSettings(t, config.ChatSettings{Temperature: 0.1}, invoke)
}

func captureChatRequestWithSettings(t *testing.T, settings config.ChatSettings, invoke func(*Client) error) map[string]any {
	t.Helper()

	type capturedRequest struct {
		path string
		body map[string]any
		err  error
	}

	captured := capturedRequest{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured.path = r.URL.Path
		payload, err := io.ReadAll(r.Body)
		if err != nil {
			captured.err = err
			http.Error(w, "read failed", http.StatusInternalServerError)
			return
		}
		if err := json.Unmarshal(payload, &captured.body); err != nil {
			captured.err = err
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-test","object":"chat.completion","created":0,"model":"test-model","choices":[{"index":0,"message":{"role":"assistant","content":"{\"verdict\":\"pass\",\"summary\":\"ok\",\"issues\":[]}"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	client := New(server.URL, "test-token", "test-model", 256, time.Second, settings)
	if err := invoke(client); err != nil {
		t.Fatalf("chat call failed: %v", err)
	}
	if captured.err != nil {
		t.Fatalf("request capture failed: %v", captured.err)
	}
	if captured.path != "/v1/chat/completions" {
		t.Fatalf("path=%q, want %q", captured.path, "/v1/chat/completions")
	}
	return captured.body
}

func captureDetailedChatRequest(t *testing.T, options SchemaGenerationOptions) (map[string]any, ChatResult) {
	t.Helper()
	response := `{"id":"chatcmpl-inference","object":"chat.completion","created":0,"model":"gpt-5.4-mini-2026-07-01","system_fingerprint":"fp_test","usage":{"prompt_tokens":41,"completion_tokens":7,"total_tokens":48},"choices":[{"index":0,"message":{"role":"assistant","content":"{\"decision\":\"allow\"}"},"finish_reason":"stop"}]}`
	return captureDetailedChatResponse(t, response, options)
}

func captureDetailedChatResponse(
	t *testing.T,
	response string,
	options SchemaGenerationOptions,
) (map[string]any, ChatResult) {
	t.Helper()

	requestBody := make(map[string]any)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		payload, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
			return
		}
		if err := json.Unmarshal(payload, &requestBody); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Backend-Version", "backend-2026.07")
		_, _ = w.Write([]byte(response))
	}))
	defer server.Close()

	topK := 40
	repeatPenalty := 1.05
	client := New(server.URL, "test-token", "gpt-5.4-mini", 8192, time.Second, config.ChatSettings{
		Temperature:   0.7,
		TopK:          &topK,
		RepeatPenalty: &repeatPenalty,
	})
	ctx := requestmeta.With(context.Background(), requestmeta.Metadata{ReviewID: "inference-test"})
	result, err := client.ChatSchemaDetailed(
		ctx,
		"prompt",
		"input",
		json.RawMessage(`{"type":"object"}`),
		options,
	)
	if err != nil {
		t.Fatalf("ChatSchemaDetailed: %v", err)
	}
	return requestBody, result
}

func requireFloat64(t *testing.T, value any, field string) float64 {
	t.Helper()

	number, ok := value.(float64)
	if !ok {
		t.Fatalf("%s=%T, want float64", field, value)
	}
	return number
}

func requireMap(t *testing.T, value any, field string) map[string]any {
	t.Helper()

	mapped, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s=%T, want map[string]any", field, value)
	}
	return mapped
}

func stringSet(t *testing.T, value any, field string) map[string]bool {
	t.Helper()

	items, ok := value.([]any)
	if !ok {
		t.Fatalf("%s=%T, want []any", field, value)
	}

	values := make(map[string]bool, len(items))
	for index, item := range items {
		text, ok := item.(string)
		if !ok {
			t.Fatalf("%s[%d]=%T, want string", field, index, item)
		}
		values[text] = true
	}
	return values
}

func mustDecodeJSON(t *testing.T, value string) any {
	t.Helper()
	var decoded any
	if err := json.Unmarshal([]byte(value), &decoded); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	return decoded
}

func canceledChatContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

func expiredChatContext() context.Context {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	cancel()
	return ctx
}
