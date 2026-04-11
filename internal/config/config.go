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
}

// LMStudio holds connection and model settings.
// Works with any OpenAI-compatible endpoint: LM Studio, ollama, OpenAI, etc.
type LMStudio struct {
	URL       string `toml:"url"`
	Token     string `toml:"token"`
	FastModel string `toml:"fast_model"`
	DeepModel string `toml:"deep_model"`
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
