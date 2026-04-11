// Package config loads lm-review configuration from XDG TOML.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// Config is the top-level configuration.
type Config struct {
	LMStudio LMStudio `toml:"lmstudio"`
}

// LMStudio holds connection and model settings.
type LMStudio struct {
	URL       string `toml:"url"`
	Token     string `toml:"token"`
	FastModel string `toml:"fast_model"`
	DeepModel string `toml:"deep_model"`
}

// Load reads config from XDG_CONFIG_HOME/lm-review/config.toml.
func Load() (*Config, error) {
	path := ConfigPath()

	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, fmt.Errorf("no config found at %s — run 'lm-review init' to create one", path)
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

// WriteDefault writes a starter config to ConfigPath.
func WriteDefault(token string) error {
	path := ConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	content := fmt.Sprintf(`[lmstudio]
url        = "http://localhost:1234"
token      = %q
fast_model = "qwen3-coder-30b-a3b-instruct-dwq-lr9e8"
deep_model = "qwen3.5-122b-a10b-text-qx85-mlx"
`, token)

	return os.WriteFile(path, []byte(content), 0o600)
}

// ConfigPath returns the XDG config path for lm-review.
func ConfigPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "lm-review", "config.toml")
}
