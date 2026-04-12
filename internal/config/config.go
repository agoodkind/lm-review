// Package config loads lm-review configuration from XDG TOML.
package config

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"

	"goodkind.io/lm-review/internal/xdg"
)

// Config is the top-level configuration.
type Config struct {
	LMStudio LMStudio `toml:"lmstudio"`
	Rules    []Rule   `toml:"rules"`
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
	URL       string `toml:"url"`
	Token     string `toml:"token"`
	FastModel string `toml:"fast_model"`
	DeepModel string `toml:"deep_model"`

	// Per-mode overrides. Falls back to FastModel/DeepModel if not set.
	Diff ModeModels `toml:"diff,omitempty"`
	PR   ModeModels `toml:"pr,omitempty"`
	Repo ModeModels `toml:"repo,omitempty"`
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
