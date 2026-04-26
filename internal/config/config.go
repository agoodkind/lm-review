// Package config loads lm-review configuration from XDG TOML.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"goodkind.io/lm-review/internal/xdg"
)

// Config is the top-level configuration.
type Config struct {
	Provider     string       `toml:"provider,omitempty"` // "openai_compat" (default) or "claude"
	OpenAICompat OpenAICompat `toml:"openai_compat"`
	Claude       Claude       `toml:"claude"`
	StaticReview StaticReview `toml:"static_review"`
	Rules        []Rule       `toml:"rules"`
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

// OpenAICompat holds connection and model settings.
// Works with any OpenAI-compatible endpoint: lmd, LM Studio, ollama, OpenAI, etc.
type OpenAICompat struct {
	URL               string `toml:"url"`
	Token             string `toml:"token"`
	QuickModel        string `toml:"quick_model"`
	FastModel         string `toml:"fast_model"`
	DeepModel         string `toml:"deep_model"`
	UltraModel        string `toml:"ultra_model"`
	ContextLength     int    `toml:"context_length,omitempty"`      // tokens; passed to lms load -c (default 32768)
	MaxResponseTokens int    `toml:"max_response_tokens,omitempty"` // max response tokens per request (default 8192)
	ChunkParallelism  int    `toml:"chunk_parallelism,omitempty"`   // parallel chunk reviews for large repos (default 1)
	MaxMemoryGB       int    `toml:"max_memory_gb,omitempty"`       // max GB of models to keep loaded (default: 75% of system RAM)

	// ModelPriority is an ordered list of models from weakest to strongest.
	// When a tier requests a model and a higher-priority model is already
	// loaded and warm, the loaded model is used instead of swapping.
	// Empty list disables substitution (always load the exact model requested).
	ModelPriority []string `toml:"model_priority,omitempty"`

	// AllowEviction controls whether lm-review may load/unload models.
	// When false, only already-loaded models are used. If no suitable model
	// is loaded, the review is skipped. Prevents disrupting active coding
	// sessions. Defaults to true if not set.
	AllowEviction *bool `toml:"allow_eviction,omitempty"`

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

// ResolveMaxMemoryBytes returns the max bytes of model memory to keep loaded.
func (l OpenAICompat) ResolveMaxMemoryBytes() int64 {
	if l.MaxMemoryGB > 0 {
		return int64(l.MaxMemoryGB) * 1024 * 1024 * 1024
	}
	return 0 // 0 means auto-detect in the caller
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

// CanEvict returns whether lm-review is allowed to load/unload models.
// Defaults to true if not configured.
func (l OpenAICompat) CanEvict() bool {
	if l.AllowEviction == nil {
		return true
	}
	return *l.AllowEviction
}

// PreferLoaded checks whether a loaded model should be used instead of the
// requested model, based on model_priority. Returns the substitute model name
// if a higher-priority model is loaded, or the original model if not.
// loadedModels should come from lmctl.ListLoaded().
func (l OpenAICompat) PreferLoaded(requested string, loadedModels []string) string {
	if len(l.ModelPriority) == 0 {
		return requested
	}

	reqRank := l.modelRank(requested)
	if reqRank < 0 {
		return requested // requested model not in priority list, no substitution
	}

	bestRank := reqRank
	bestModel := requested
	for _, loaded := range loadedModels {
		rank := l.modelRank(loaded)
		if rank > bestRank {
			bestRank = rank
			bestModel = loaded
		}
	}
	return bestModel
}

// modelRank returns the index of a model in ModelPriority, matching on base
// name (strips publisher prefix). Returns -1 if not found.
func (l OpenAICompat) modelRank(model string) int {
	base := baseModelName(model)
	for i, m := range l.ModelPriority {
		if baseModelName(m) == base {
			return i
		}
	}
	return -1
}

// baseModelName strips the publisher prefix (e.g. "qwen/qwen3-coder-next"
// becomes "qwen3-coder-next").
func baseModelName(model string) string {
	if i := strings.LastIndex(model, "/"); i >= 0 {
		return model[i+1:]
	}
	return model
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
