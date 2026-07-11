package config

import (
	"testing"
	"time"
)

func TestResolveReviewChunkBytesCapsLargeContext(t *testing.T) {
	cfg := OpenAICompat{ContextLength: 262144}

	got := cfg.ResolveReviewChunkBytes()

	if got != maxReviewChunkBytes {
		t.Fatalf("chunk bytes=%d, want %d", got, maxReviewChunkBytes)
	}
}

func TestResolveReviewChunkBytesKeepsSmallContext(t *testing.T) {
	cfg := OpenAICompat{ContextLength: 8192}

	got := cfg.ResolveReviewChunkBytes()
	want := cfg.ResolveRepoMaxBytes()

	if got != want {
		t.Fatalf("chunk bytes=%d, want %d", got, want)
	}
}

func TestResolveRequestTimeout(t *testing.T) {
	if got := (OpenAICompat{}).ResolveRequestTimeout(); got != 5*time.Minute {
		t.Fatalf("default timeout=%s, want 5m0s", got)
	}

	cfg := OpenAICompat{RequestTimeoutSec: 12}
	if got := cfg.ResolveRequestTimeout(); got != 12*time.Second {
		t.Fatalf("configured timeout=%s, want 12s", got)
	}
}

func TestInferenceDefaults(t *testing.T) {
	cfg := Inference{}

	if got := cfg.ResolveModel(); got != DefaultInferenceModel {
		t.Fatalf("model=%q, want %q", got, DefaultInferenceModel)
	}
	if got := cfg.ResolveListenAddress(); got != DefaultInferenceListenAddress {
		t.Fatalf("listen_address=%q, want %q", got, DefaultInferenceListenAddress)
	}
}

func TestInferenceResolveBackend(t *testing.T) {
	global := OpenAICompat{
		URL:               "https://global.example.test",
		Token:             "global-token",
		FastModel:         "global-model",
		MaxResponseTokens: 4096,
	}
	tests := []struct {
		name      string
		inference Inference
		wantURL   string
		wantToken string
	}{
		{
			name:      "inherits global backend",
			inference: Inference{},
			wantURL:   global.URL,
			wantToken: global.Token,
		},
		{
			name:      "overrides URL only",
			inference: Inference{URL: "https://inference.example.test"},
			wantURL:   "https://inference.example.test",
			wantToken: global.Token,
		},
		{
			name:      "overrides token only",
			inference: Inference{Token: "inference-token"},
			wantURL:   global.URL,
			wantToken: "inference-token",
		},
		{
			name: "overrides URL and token",
			inference: Inference{
				URL:   "https://inference.example.test",
				Token: "inference-token",
			},
			wantURL:   "https://inference.example.test",
			wantToken: "inference-token",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			backend := test.inference.ResolveBackend(global)

			if backend.URL != test.wantURL {
				t.Fatalf("url=%q, want %q", backend.URL, test.wantURL)
			}
			if backend.Token != test.wantToken {
				t.Fatalf("token=%q, want configured token", backend.Token)
			}
			if backend.FastModel != global.FastModel {
				t.Fatalf("fast_model=%q, want %q", backend.FastModel, global.FastModel)
			}
			if backend.MaxResponseTokens != global.MaxResponseTokens {
				t.Fatalf(
					"max_response_tokens=%d, want %d",
					backend.MaxResponseTokens,
					global.MaxResponseTokens,
				)
			}
		})
	}
}

func TestResolveChatSettings(t *testing.T) {
	topP := 0.8
	topK := 20
	presencePenalty := 0.4
	frequencyPenalty := 0.5
	repeatPenalty := 1.1
	seed := int64(7)

	cfg := OpenAICompat{
		Temperature:      floatPtr(0),
		TopP:             floatPtr(topP),
		TopK:             intPtr(topK),
		PresencePenalty:  floatPtr(presencePenalty),
		FrequencyPenalty: floatPtr(frequencyPenalty),
		RepeatPenalty:    floatPtr(repeatPenalty),
		Seed:             &seed,
		Stop:             []string{"DONE"},
	}
	settings := cfg.ResolveChatSettings()

	if settings.Temperature != 0 {
		t.Fatalf("temperature=%v, want 0", settings.Temperature)
	}
	if settings.TopP == nil || *settings.TopP != topP {
		t.Fatalf("top_p=%v, want %v", settings.TopP, topP)
	}
	if settings.TopK == nil || *settings.TopK != topK {
		t.Fatalf("top_k=%v, want %v", settings.TopK, topK)
	}
	if settings.PresencePenalty == nil || *settings.PresencePenalty != presencePenalty {
		t.Fatalf("presence_penalty=%v, want %v", settings.PresencePenalty, presencePenalty)
	}
	if settings.FrequencyPenalty == nil || *settings.FrequencyPenalty != frequencyPenalty {
		t.Fatalf("frequency_penalty=%v, want %v", settings.FrequencyPenalty, frequencyPenalty)
	}
	if settings.RepeatPenalty == nil || *settings.RepeatPenalty != repeatPenalty {
		t.Fatalf("repeat_penalty=%v, want %v", settings.RepeatPenalty, repeatPenalty)
	}
	if settings.Seed == nil || *settings.Seed != seed {
		t.Fatalf("seed=%v, want %v", settings.Seed, seed)
	}
	if len(settings.Stop) != 1 || settings.Stop[0] != "DONE" {
		t.Fatalf("stop=%v, want [DONE]", settings.Stop)
	}
}

func floatPtr(value float64) *float64 { return &value }
func intPtr(value int) *int           { return &value }
