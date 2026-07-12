package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"
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

func TestInferenceResolveBackendsRoutesByModel(t *testing.T) {
	var cfg struct {
		OpenAICompat OpenAICompat `toml:"openai_compat"`
		Inference    Inference    `toml:"inference"`
	}
	_, err := toml.Decode(`[openai_compat]
url = "http://global.example.test"
token = "global-token"
max_response_tokens = 4096

[[inference.backend]]
models = ["gpt-5.4-mini"]
base_url = "https://api.openai.com"
token = "openai-token"

[[inference.backend]]
models = ["agentgate/agent-gate-judge-v4"]
base_url = "http://localhost:5400"
`, &cfg)
	if err != nil {
		t.Fatalf("decode TOML: %v", err)
	}
	resolved, err := cfg.Inference.ResolveBackends(cfg.OpenAICompat)
	if err != nil {
		t.Fatalf("ResolveBackends returned error: %v", err)
	}
	if len(resolved) != 2 {
		t.Fatalf("resolved backends=%d, want 2", len(resolved))
	}
	byModel := make(map[string]OpenAICompat)
	for _, backend := range resolved {
		for _, model := range backend.Models {
			byModel[model] = backend.Backend
		}
	}
	mini, ok := byModel["gpt-5.4-mini"]
	if !ok {
		t.Fatal("gpt-5.4-mini has no resolved backend")
	}
	if mini.URL != "https://api.openai.com" || mini.Token != "openai-token" {
		t.Fatalf("mini backend URL=%q token=%q, want OpenAI URL + token", mini.URL, mini.Token)
	}
	if mini.ResolveMaxResponseTokens() != 4096 {
		t.Fatalf("mini max response tokens=%d, want inherited 4096", mini.ResolveMaxResponseTokens())
	}
	v4, ok := byModel["agentgate/agent-gate-judge-v4"]
	if !ok {
		t.Fatal("v4 model has no resolved backend")
	}
	if v4.URL != "http://localhost:5400" || v4.Token != "" {
		t.Fatalf("v4 backend URL=%q token=%q, want lmd URL + no token", v4.URL, v4.Token)
	}
}

func TestInferenceResolveBackendsRejectsDuplicateModel(t *testing.T) {
	var cfg struct {
		Inference Inference `toml:"inference"`
	}
	_, err := toml.Decode(`[[inference.backend]]
models = ["m"]
base_url = "http://a.example.test"

[[inference.backend]]
models = ["m"]
base_url = "http://b.example.test"
`, &cfg)
	if err != nil {
		t.Fatalf("decode TOML: %v", err)
	}
	if _, err := cfg.Inference.ResolveBackends(OpenAICompat{}); err == nil {
		t.Fatal("expected duplicate-model error, got nil")
	}
}

func TestInferenceResolveBackendsRejectsTokenAndTokenFile(t *testing.T) {
	var cfg struct {
		Inference Inference `toml:"inference"`
	}
	_, err := toml.Decode(`[[inference.backend]]
models = ["m"]
base_url = "http://a.example.test"
token = "t"
token_file = "f"
`, &cfg)
	if err != nil {
		t.Fatalf("decode TOML: %v", err)
	}
	if _, err := cfg.Inference.ResolveBackends(OpenAICompat{}); err == nil {
		t.Fatal("expected token/token_file mutual-exclusion error, got nil")
	}
}

func TestInferenceResolveBackendsEmptyFallsBackToShorthand(t *testing.T) {
	resolved, err := Inference{}.ResolveBackends(OpenAICompat{})
	if err != nil {
		t.Fatalf("ResolveBackends returned error: %v", err)
	}
	if resolved != nil {
		t.Fatalf("resolved=%v, want nil so the single-backend shorthand is used", resolved)
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
			name:      "overrides base URL without inheriting token",
			inference: Inference{BaseURL: "https://inference.example.test"},
			wantURL:   "https://inference.example.test",
			wantToken: "",
		},
		{
			name:      "overrides token only",
			inference: Inference{Token: "inference-token"},
			wantURL:   global.URL,
			wantToken: "inference-token",
		},
		{
			name: "overrides base URL and token",
			inference: Inference{
				BaseURL: "https://inference.example.test",
				Token:   "inference-token",
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

func TestInferenceTOMLDecodesBaseURL(t *testing.T) {
	var cfg struct {
		Inference Inference `toml:"inference"`
	}
	_, err := toml.Decode(`[inference]
base_url = "https://inference.example.test"
token = "inference-token"
`, &cfg)
	if err != nil {
		t.Fatalf("decode TOML: %v", err)
	}
	if cfg.Inference.BaseURL != "https://inference.example.test" {
		t.Fatalf("base_url=%q, want configured URL", cfg.Inference.BaseURL)
	}
	if cfg.Inference.Token != "inference-token" {
		t.Fatalf("token=%q, want configured token", cfg.Inference.Token)
	}
}

func TestInferenceResolveBackendCredentialReadsRelativeTokenFile(t *testing.T) {
	configPath := writeTestConfig(t, `[openai_compat]
url = "https://global.example.test"
token = "global-token"

[inference]
base_url = "https://inference.example.test"
token_file = "secrets/inference-token"
`)
	tokenPath := filepath.Join(filepath.Dir(configPath), "secrets", "inference-token")
	if err := os.MkdirAll(filepath.Dir(tokenPath), 0o700); err != nil {
		t.Fatalf("create token directory: %v", err)
	}
	if err := os.WriteFile(tokenPath, []byte("  inference-token\n"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Inference.Token != "" {
		t.Fatalf("inference token=%q before resolution, want empty", cfg.Inference.Token)
	}
	if cfg.OpenAICompat.Token != "global-token" {
		t.Fatalf("global token=%q, want unchanged global token", cfg.OpenAICompat.Token)
	}
	backend, err := cfg.Inference.ResolveBackendCredential(cfg.OpenAICompat)
	if err != nil {
		t.Fatalf("ResolveBackendCredential returned error: %v", err)
	}
	if backend.URL != "https://inference.example.test" {
		t.Fatalf("backend URL=%q, want inference URL", backend.URL)
	}
	if backend.Token != "inference-token" {
		t.Fatalf("backend token=%q, want resolved inference token", backend.Token)
	}
}

func TestInferenceResolveBackendCredentialReadsHomeRelativeTokenFile(t *testing.T) {
	homeDirectory := t.TempDir()
	t.Setenv("HOME", homeDirectory)
	tokenPath := filepath.Join(homeDirectory, ".config", "lm-review", "inference.token")
	if err := os.MkdirAll(filepath.Dir(tokenPath), 0o700); err != nil {
		t.Fatalf("create token directory: %v", err)
	}
	if err := os.WriteFile(tokenPath, []byte("home-token\n"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	writeTestConfig(t, `[inference]
token_file = "~/.config/lm-review/inference.token"
`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	backend, err := cfg.Inference.ResolveBackendCredential(cfg.OpenAICompat)
	if err != nil {
		t.Fatalf("ResolveBackendCredential returned error: %v", err)
	}
	if backend.Token != "home-token" {
		t.Fatalf("backend token=%q, want resolved home token", backend.Token)
	}
}

func TestLoadDoesNotResolveInvalidInferenceTokenFile(t *testing.T) {
	writeTestConfig(t, `[openai_compat]
url = "https://global.example.test"
token = "global-token"

[inference]
base_url = "https://inference.example.test"
token_file = "missing.token"
`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load returned error for unused inference credential: %v", err)
	}
	if cfg.OpenAICompat.Token != "global-token" {
		t.Fatalf("global token=%q, want unchanged ordinary review token", cfg.OpenAICompat.Token)
	}
	if cfg.Inference.TokenFile != "missing.token" {
		t.Fatalf("inference token_file=%q, want unresolved reference", cfg.Inference.TokenFile)
	}
}

func TestInferenceResolveBackendCredentialRejectsInvalidTokenFile(t *testing.T) {
	tests := []struct {
		name         string
		config       string
		prepare      func(*testing.T, string)
		wantError    string
		secretCanary string
	}{
		{
			name: "inline token and token file",
			config: `[inference]
token = "inline-secret-canary"
token_file = "inference.token"
`,
			wantError:    "token and token_file are mutually exclusive",
			secretCanary: "inline-secret-canary",
		},
		{
			name: "missing token file",
			config: `[inference]
token_file = "missing.token"
`,
			wantError: "read inference token_file",
		},
		{
			name: "non-regular token file",
			config: `[inference]
token_file = "inference.token"
`,
			prepare: func(t *testing.T, tokenPath string) {
				t.Helper()
				if err := os.Mkdir(tokenPath, 0o700); err != nil {
					t.Fatalf("create token directory: %v", err)
				}
			},
			wantError: "inference token_file must be a regular file",
		},
		{
			name: "token file without exact permissions",
			config: `[inference]
token_file = "inference.token"
`,
			prepare: func(t *testing.T, tokenPath string) {
				t.Helper()
				if err := os.WriteFile(tokenPath, []byte("permission-secret-canary"), 0o644); err != nil {
					t.Fatalf("write group-readable token file: %v", err)
				}
			},
			wantError:    "inference token_file must have permissions 0600",
			secretCanary: "permission-secret-canary",
		},
		{
			name: "unreadable token file",
			config: `[inference]
token_file = "inference.token"
`,
			prepare: func(t *testing.T, tokenPath string) {
				t.Helper()
				if err := os.WriteFile(tokenPath, []byte("unreadable-secret-canary"), 0o000); err != nil {
					t.Fatalf("write unreadable token file: %v", err)
				}
			},
			wantError:    "read inference token_file",
			secretCanary: "unreadable-secret-canary",
		},
		{
			name: "symlink token file",
			config: `[inference]
token_file = "inference.token"
`,
			prepare: func(t *testing.T, tokenPath string) {
				t.Helper()
				targetPath := filepath.Join(filepath.Dir(tokenPath), "target.token")
				if err := os.WriteFile(targetPath, []byte("symlink-secret-canary"), 0o600); err != nil {
					t.Fatalf("write symlink target: %v", err)
				}
				if err := os.Symlink(targetPath, tokenPath); err != nil {
					t.Fatalf("create token symlink: %v", err)
				}
			},
			wantError:    "inference token_file must not be a symlink",
			secretCanary: "symlink-secret-canary",
		},
		{
			name: "empty token file",
			config: `[inference]
token_file = "inference.token"
`,
			prepare: func(t *testing.T, tokenPath string) {
				t.Helper()
				if err := os.WriteFile(tokenPath, nil, 0o600); err != nil {
					t.Fatalf("write empty token file: %v", err)
				}
			},
			wantError: "inference token_file is empty",
		},
		{
			name: "whitespace-only token file",
			config: `[inference]
token_file = "inference.token"
`,
			prepare: func(t *testing.T, tokenPath string) {
				t.Helper()
				if err := os.WriteFile(tokenPath, []byte(" \n\t"), 0o600); err != nil {
					t.Fatalf("write empty token file: %v", err)
				}
			},
			wantError: "inference token_file is empty",
		},
		{
			name: "oversized token file",
			config: `[inference]
token_file = "inference.token"
`,
			prepare: func(t *testing.T, tokenPath string) {
				t.Helper()
				contents := bytes.Repeat([]byte("x"), maxInferenceTokenBytes+1)
				if err := os.WriteFile(tokenPath, contents, 0o600); err != nil {
					t.Fatalf("write oversized token file: %v", err)
				}
			},
			wantError: "inference token_file exceeds maximum size",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			configPath := writeTestConfig(t, test.config)
			if test.prepare != nil {
				test.prepare(t, filepath.Join(filepath.Dir(configPath), "inference.token"))
			}

			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load returned error before inference resolution: %v", err)
			}
			_, err = cfg.Inference.ResolveBackendCredential(cfg.OpenAICompat)
			if err == nil {
				t.Fatal("ResolveBackendCredential returned nil error")
			}
			if !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error=%q, want substring %q", err, test.wantError)
			}
			if test.secretCanary != "" && strings.Contains(err.Error(), test.secretCanary) {
				t.Fatalf("error exposes secret content: %q", err)
			}
		})
	}
}

func TestReadOpenedInferenceTokenUsesOpenedDescriptor(t *testing.T) {
	directory := t.TempDir()
	tokenPath := filepath.Join(directory, "inference.token")
	replacementPath := filepath.Join(directory, "replacement.token")
	if err := os.WriteFile(tokenPath, []byte("original-token\n"), 0o600); err != nil {
		t.Fatalf("write original token: %v", err)
	}
	if err := os.WriteFile(replacementPath, []byte("replacement-token\n"), 0o600); err != nil {
		t.Fatalf("write replacement token: %v", err)
	}

	tokenFile, err := openInferenceTokenFile(tokenPath)
	if err != nil {
		t.Fatalf("openInferenceTokenFile returned error: %v", err)
	}
	defer tokenFile.Close()
	if err := os.Rename(replacementPath, tokenPath); err != nil {
		t.Fatalf("replace token pathname: %v", err)
	}

	token, err := readOpenedInferenceTokenFile(tokenFile, tokenPath)
	if err != nil {
		t.Fatalf("readOpenedInferenceTokenFile returned error: %v", err)
	}
	if token != "original-token" {
		t.Fatalf("token=%q, want content from opened descriptor", token)
	}
}

func TestInferenceTOMLEncodesTokenFileWithoutInlineToken(t *testing.T) {
	var encoded bytes.Buffer
	cfg := struct {
		Inference Inference `toml:"inference"`
	}{
		Inference: Inference{TokenFile: "secrets/inference.token"},
	}
	if err := toml.NewEncoder(&encoded).Encode(cfg); err != nil {
		t.Fatalf("encode TOML: %v", err)
	}
	if !strings.Contains(encoded.String(), `token_file = "secrets/inference.token"`) {
		t.Fatalf("encoded config does not contain token_file: %s", encoded.String())
	}
	if strings.Contains(encoded.String(), "token =") {
		t.Fatalf("encoded config contains inline token: %s", encoded.String())
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

func writeTestConfig(t *testing.T, content string) string {
	t.Helper()
	configRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	configPath := filepath.Join(configRoot, "lm-review", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatalf("create config directory: %v", err)
	}
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return configPath
}
