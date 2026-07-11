// Package config loads lm-review configuration from XDG TOML.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"goodkind.io/lm-review/internal/xdg"
)

const (
	maxReviewChunkBytes = 80 * 1024
	// DefaultInferenceModel leaves model selection to explicit configuration.
	DefaultInferenceModel = ""
	// DefaultInferenceListenAddress is the default inference gRPC listen address.
	DefaultInferenceListenAddress = "[::1]:5401"
)

// Config is the top-level configuration.
type Config struct {
	Provider     string       `toml:"provider,omitempty"` // "openai_compat" (default) or "claude"
	OpenAICompat OpenAICompat `toml:"openai_compat"`
	Claude       Claude       `toml:"claude"`
	StaticReview StaticReview `toml:"static_review"`
	Inference    Inference    `toml:"inference"`
	Rules        []Rule       `toml:"rules"`
}

// Inference holds settings for the standalone inference gRPC service.
type Inference struct {
	Model         string `toml:"model,omitempty"`
	ListenAddress string `toml:"listen_address,omitempty"`
	BaseURL       string `toml:"base_url,omitempty"`
	Token         string `toml:"token,omitempty"`
	TokenFile     string `toml:"token_file,omitempty"`
}

// ResolveModel returns the configured inference model id.
func (i Inference) ResolveModel() string {
	if i.Model != "" {
		return i.Model
	}
	return DefaultInferenceModel
}

// ResolveListenAddress returns the configured inference listen address or the default address.
func (i Inference) ResolveListenAddress() string {
	if i.ListenAddress != "" {
		return i.ListenAddress
	}
	return DefaultInferenceListenAddress
}

// ResolveBackend applies inference-specific connection overrides to the global backend.
func (i Inference) ResolveBackend(global OpenAICompat) OpenAICompat {
	backend := global
	if i.BaseURL != "" {
		backend.URL = i.BaseURL
		backend.Token = ""
	}
	if i.Token != "" {
		backend.Token = i.Token
	}
	return backend
}

// ReadBackendCredential resolves the backend and reads an optional
// owner-only token file without storing the credential in TOML.
func (i Inference) ReadBackendCredential(global OpenAICompat) (OpenAICompat, error) {
	backend := i.ResolveBackend(global)
	if i.TokenFile == "" {
		return backend, nil
	}
	if i.Token != "" {
		return OpenAICompat{}, errors.New("inference token and token_file are mutually exclusive")
	}
	if !filepath.IsAbs(i.TokenFile) {
		return OpenAICompat{}, errors.New("inference token_file must be an absolute path")
	}
	info, err := os.Stat(i.TokenFile)
	if err != nil {
		slog.Error("stat inference token_file failed", "err", err)
		return OpenAICompat{}, fmt.Errorf("stat inference token_file: %w", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return OpenAICompat{}, errors.New("inference token_file must not be accessible by group or other users")
	}
	contents, err := os.ReadFile(i.TokenFile)
	if err != nil {
		slog.Error("read inference token_file failed", "err", err)
		return OpenAICompat{}, fmt.Errorf("read inference token_file: %w", err)
	}
	backend.Token = strings.TrimSpace(string(contents))
	if backend.Token == "" {
		return OpenAICompat{}, errors.New("inference token_file is empty")
	}
	return backend, nil
}

// StaticReview configures the deterministic static-analysis pipeline that backs
// the review_static MCP tool.
type StaticReview struct {
	Enabled         *bool    `toml:"enabled,omitempty"`
	DisabledSources []string `toml:"disabled_sources,omitempty"`
	EnabledChecks   []string `toml:"enabled_checks,omitempty"`
	Synthesize      *bool    `toml:"synthesize,omitempty"`
}

// IsEnabled reports whether the static review pipeline is enabled.
func (s StaticReview) IsEnabled() bool {
	if s.Enabled == nil {
		return true
	}
	return *s.Enabled
}

// SynthesizeByDefault reports whether review_static should use the LLM when
// the caller does not choose a mode explicitly.
func (s StaticReview) SynthesizeByDefault() bool {
	if s.Synthesize == nil {
		return true
	}
	return *s.Synthesize
}

// Claude holds settings for the claude CLI provider.
type Claude struct {
	Model string `toml:"model,omitempty"` // e.g. "opus", "sonnet", "haiku"
}

// ResolveProvider returns the configured provider or the default.
func (c Config) ResolveProvider() string {
	if c.Provider != "" {
		return c.Provider
	}
	return "openai_compat"
}

// ModeModels holds per-mode model overrides.
// Falls back to the global quick/fast/deep/ultra_model if not set.
type ModeModels struct {
	QuickModel string `toml:"quick_model,omitempty"`
	Model      string `toml:"model,omitempty"`
	DeepModel  string `toml:"deep_model,omitempty"`
	UltraModel string `toml:"ultra_model,omitempty"`
}

// LMDPreflight controls the optional LMD-specific load estimate/preload step.
type LMDPreflight struct {
	Enabled *bool `toml:"enabled,omitempty"`
	Preload *bool `toml:"preload,omitempty"`
}

// IsEnabled reports whether LMD preflight is explicitly enabled.
func (p LMDPreflight) IsEnabled() bool {
	return p.Enabled != nil && *p.Enabled
}

// ShouldPreload reports whether the preflight should also preload the model.
func (p LMDPreflight) ShouldPreload() bool {
	return p.Preload != nil && *p.Preload
}

// ChatSettings holds standard OpenAI-compatible inference settings.
type ChatSettings struct {
	Temperature      float64
	TopP             *float64
	TopK             *int
	PresencePenalty  *float64
	FrequencyPenalty *float64
	RepeatPenalty    *float64
	Seed             *int64
	Stop             []string
}

// OpenAICompat holds connection and model settings.
// Works with any OpenAI-compatible endpoint: lmd, LM Studio, ollama, OpenAI, etc.
type OpenAICompat struct {
	URL               string       `toml:"url"`
	Token             string       `toml:"token"`
	QuickModel        string       `toml:"quick_model"`
	FastModel         string       `toml:"fast_model"`
	DeepModel         string       `toml:"deep_model"`
	UltraModel        string       `toml:"ultra_model"`
	ContextLength     int          `toml:"context_length,omitempty"`      // tokens used to size review snapshots and chunks
	MaxResponseTokens int          `toml:"max_response_tokens,omitempty"` // max response tokens per request (default 8192)
	ChunkParallelism  int          `toml:"chunk_parallelism,omitempty"`   // parallel chunk reviews for large repos (default 1)
	RequestTimeoutSec int          `toml:"request_timeout_seconds,omitempty"`
	Temperature       *float64     `toml:"temperature,omitempty"`
	TopP              *float64     `toml:"top_p,omitempty"`
	TopK              *int         `toml:"top_k,omitempty"`
	PresencePenalty   *float64     `toml:"presence_penalty,omitempty"`
	FrequencyPenalty  *float64     `toml:"frequency_penalty,omitempty"`
	RepeatPenalty     *float64     `toml:"repeat_penalty,omitempty"`
	Seed              *int64       `toml:"seed,omitempty"`
	Stop              []string     `toml:"stop,omitempty"`
	LMDPreflight      LMDPreflight `toml:"lmd_preflight,omitempty"`

	// Per-mode overrides. Falls back to FastModel/DeepModel if not set.
	Diff ModeModels `toml:"diff,omitempty"`
	PR   ModeModels `toml:"pr,omitempty"`
	Repo ModeModels `toml:"repo,omitempty"`
}

// ResolveContextLength returns the configured context length or the default.
func (l OpenAICompat) ResolveContextLength() int {
	if l.ContextLength > 0 {
		return l.ContextLength
	}
	return 32768
}

// ResolveMaxResponseTokens returns the configured max response tokens or the default.
func (l OpenAICompat) ResolveMaxResponseTokens() int {
	if l.MaxResponseTokens > 0 {
		return l.MaxResponseTokens
	}
	return 8192
}

// ResolveChunkParallelism returns the configured chunk parallelism or the default.
func (l OpenAICompat) ResolveChunkParallelism() int {
	if l.ChunkParallelism > 0 {
		return l.ChunkParallelism
	}
	return 1
}

// ResolveRequestTimeout returns the per-chat OpenAI-compatible request timeout.
func (l OpenAICompat) ResolveRequestTimeout() time.Duration {
	if l.RequestTimeoutSec > 0 {
		return time.Duration(l.RequestTimeoutSec) * time.Second
	}
	return 5 * time.Minute
}

// ResolveChatSettings returns the configured chat generation settings.
func (l OpenAICompat) ResolveChatSettings() ChatSettings {
	settings := ChatSettings{
		Temperature:      0.1,
		TopP:             nil,
		TopK:             nil,
		PresencePenalty:  nil,
		FrequencyPenalty: nil,
		RepeatPenalty:    nil,
		Seed:             nil,
		Stop:             nil,
	}
	if l.Temperature != nil {
		settings.Temperature = *l.Temperature
	}
	if l.TopP != nil {
		value := *l.TopP
		settings.TopP = &value
	}
	if l.TopK != nil {
		value := *l.TopK
		settings.TopK = &value
	}
	if l.PresencePenalty != nil {
		value := *l.PresencePenalty
		settings.PresencePenalty = &value
	}
	if l.FrequencyPenalty != nil {
		value := *l.FrequencyPenalty
		settings.FrequencyPenalty = &value
	}
	if l.RepeatPenalty != nil {
		value := *l.RepeatPenalty
		settings.RepeatPenalty = &value
	}
	if l.Seed != nil {
		value := *l.Seed
		settings.Seed = &value
	}
	if len(l.Stop) > 0 {
		settings.Stop = append([]string{}, l.Stop...)
	}
	return settings
}

// ResolveRepoMaxBytes returns the max bytes of source to send for a repo review.
// Derived from context_length: ~75% of context budget (in chars, ~4 chars/token)
// minus room for system prompt and response.
func (l OpenAICompat) ResolveRepoMaxBytes() int {
	ctx := l.ResolveContextLength()
	// Reserve 25% for system prompt + response tokens.
	// ~4 chars per token for code.
	return ctx * 3
}

// ResolveReviewChunkBytes returns the max bytes to send in one model request.
func (l OpenAICompat) ResolveReviewChunkBytes() int {
	chunkBytes := l.ResolveRepoMaxBytes()
	if chunkBytes <= 0 || chunkBytes > maxReviewChunkBytes {
		return maxReviewChunkBytes
	}
	return chunkBytes
}

// ResolveModel returns the model to use for a given scope and depth.
// Depth values: "quick", "normal" (or ""), "deep", "ultra".
// Resolution: per-mode config → global tier model → fallback to next lower tier.
func (l OpenAICompat) ResolveModel(scope string, depth string) string {
	var mode ModeModels
	switch scope {
	case "diff":
		mode = l.Diff
	case "pr":
		mode = l.PR
	case "repo":
		mode = l.Repo
	}

	switch depth {
	case "quick":
		if mode.QuickModel != "" {
			return mode.QuickModel
		}
		if l.QuickModel != "" {
			return l.QuickModel
		}
		return l.FastModel // fall back to fast
	case "deep":
		if mode.DeepModel != "" {
			return mode.DeepModel
		}
		return l.DeepModel
	case "ultra":
		if mode.UltraModel != "" {
			return mode.UltraModel
		}
		if l.UltraModel != "" {
			return l.UltraModel
		}
		return l.DeepModel // fall back to deep
	default: // "normal" or ""
		if mode.Model != "" {
			return mode.Model
		}
		return l.FastModel
	}
}

// Rule is a single review instruction sent to the LLM.
// If Globs is set, the rule is only included when the diff or repo contains
// files matching at least one glob. Rules with no Globs always apply.
// Always = true forces the rule to apply even when Globs is also set.
type Rule struct {
	Text   string   `toml:"text"`
	Globs  []string `toml:"globs,omitempty"`
	Always bool     `toml:"always,omitempty"`
}

// Load reads config from the XDG config path.
// Returns a helpful error if the config does not exist yet.
func Load() (*Config, error) {
	path := xdg.ConfigPath()

	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, fmt.Errorf("no config found at %s\n\nRun: lm-review init", path)
	}

	var raw struct {
		Provider     string       `toml:"provider,omitempty"`
		OpenAICompat OpenAICompat `toml:"openai_compat"`
		LMStudio     OpenAICompat `toml:"lmstudio"`
		Claude       Claude       `toml:"claude"`
		StaticReview StaticReview `toml:"static_review"`
		Inference    Inference    `toml:"inference"`
		Rules        []Rule       `toml:"rules"`
	}
	if _, err := toml.DecodeFile(path, &raw); err != nil {
		return nil, fmt.Errorf("decode config %s: %w", path, err)
	}

	cfg := Config{
		Provider:     raw.Provider,
		OpenAICompat: raw.OpenAICompat,
		Claude:       raw.Claude,
		StaticReview: raw.StaticReview,
		Inference:    raw.Inference,
		Rules:        raw.Rules,
	}
	if cfg.OpenAICompat.URL == "" {
		cfg.OpenAICompat = raw.LMStudio
	}
	if cfg.OpenAICompat.URL == "" {
		cfg.OpenAICompat.URL = "http://localhost:5400"
	}
	if cfg.Provider == "lmstudio" {
		cfg.Provider = "openai_compat"
	}

	return &cfg, nil
}

// projectConfig holds only the fields allowed in a project-local .lm-review.toml.
type projectConfig struct {
	Rules []Rule `toml:"rules"`
}

// MergeProjectRules loads <repoPath>/.lm-review.toml if it exists and appends
// its rules to cfg. The project-local file may only contain [[rules]] entries;
// model and connection settings are ignored. Returns cfg unchanged if the file
// is absent.
func MergeProjectRules(cfg *Config, repoPath string) (*Config, error) {
	localPath := filepath.Join(repoPath, ".lm-review.toml")
	if _, err := os.Stat(localPath); os.IsNotExist(err) {
		return cfg, nil
	}

	var local projectConfig
	if _, err := toml.DecodeFile(localPath, &local); err != nil {
		return cfg, fmt.Errorf("decode project config %s: %w", localPath, err)
	}

	merged := *cfg
	merged.Rules = append(append([]Rule{}, cfg.Rules...), local.Rules...)
	return &merged, nil
}
