// Package config loads lm-review configuration from XDG TOML.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"

	"goodkind.io/lm-review/internal/xdg"
)

// Config is the top-level configuration.
type Config struct {
	Provider string   `toml:"provider,omitempty"` // "lmstudio" (default) or "claude"
	LMStudio LMStudio `toml:"lmstudio"`
	Claude   Claude   `toml:"claude"`
	Rules    []Rule   `toml:"rules"`
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
	return "lmstudio"
}

// ModeModels holds per-mode model overrides.
// Falls back to the global fast_model/deep_model if not set.
type ModeModels struct {
	Model     string `toml:"model,omitempty"`
	DeepModel string `toml:"deep_model,omitempty"`
}

// LMStudio holds connection and model settings.
// Works with any OpenAI-compatible endpoint: LM Studio, ollama, OpenAI, etc.
type LMStudio struct {
	URL           string `toml:"url"`
	Token         string `toml:"token"`
	FastModel     string `toml:"fast_model"`
	DeepModel     string `toml:"deep_model"`
	ContextLength     int `toml:"context_length,omitempty"`      // tokens; passed to lms load -c (default 32768)
	MaxResponseTokens int `toml:"max_response_tokens,omitempty"` // max response tokens per request (default 8192)
	ChunkParallelism  int `toml:"chunk_parallelism,omitempty"`   // parallel chunk reviews for large repos (default 1)

	// Per-mode overrides. Falls back to FastModel/DeepModel if not set.
	Diff ModeModels `toml:"diff,omitempty"`
	PR   ModeModels `toml:"pr,omitempty"`
	Repo ModeModels `toml:"repo,omitempty"`
}

// ResolveContextLength returns the configured context length or the default.
func (l LMStudio) ResolveContextLength() int {
	if l.ContextLength > 0 {
		return l.ContextLength
	}
	return 32768
}

// ResolveMaxResponseTokens returns the configured max response tokens or the default.
func (l LMStudio) ResolveMaxResponseTokens() int {
	if l.MaxResponseTokens > 0 {
		return l.MaxResponseTokens
	}
	return 8192
}

// ResolveChunkParallelism returns the configured chunk parallelism or the default.
func (l LMStudio) ResolveChunkParallelism() int {
	if l.ChunkParallelism > 0 {
		return l.ChunkParallelism
	}
	return 1
}

// ResolveRepoMaxBytes returns the max bytes of source to send for a repo review.
// Derived from context_length: ~75% of context budget (in chars, ~4 chars/token)
// minus room for system prompt and response.
func (l LMStudio) ResolveRepoMaxBytes() int {
	ctx := l.ResolveContextLength()
	// Reserve 25% for system prompt + response tokens.
	// ~4 chars per token for code.
	return ctx * 3
}

// ResolveModel returns the model to use for a given scope and deep flag.
// Resolution: per-mode config → global fast/deep → empty string.
func (l LMStudio) ResolveModel(scope string, deep bool) string {
	var mode ModeModels
	switch scope {
	case "diff":
		mode = l.Diff
	case "pr":
		mode = l.PR
	case "repo":
		mode = l.Repo
	}

	if deep {
		if mode.DeepModel != "" {
			return mode.DeepModel
		}
		return l.DeepModel
	}

	if mode.Model != "" {
		return mode.Model
	}
	return l.FastModel
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

	var cfg Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return nil, fmt.Errorf("decode config %s: %w", path, err)
	}

	if cfg.LMStudio.URL == "" {
		cfg.LMStudio.URL = "http://localhost:1234"
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
