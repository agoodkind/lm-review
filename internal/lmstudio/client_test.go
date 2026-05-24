package lmstudio

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"goodkind.io/lm-review/internal/config"
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
