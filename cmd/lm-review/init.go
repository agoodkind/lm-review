package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"goodkind.io/lm-review/internal/config"
	"goodkind.io/lm-review/internal/lmstudio"
	"goodkind.io/lm-review/internal/xdg"
)

// InitResult is the structured output of `lm-review init`.
type InitResult struct {
	ConfigPath string   `json:"config_path"`
	Backend    string   `json:"backend"`
	URL        string   `json:"url"`
	FastModel  string   `json:"fast_model"`
	DeepModel  string   `json:"deep_model"`
	Models     []string `json:"models_available"`
}

func newInitCmd() *cobra.Command {
	var (
		jsonOut bool
		force   bool
		token   string
		url     string
	)

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Detect backend, select models, write config",
		Long: `Probes localhost for a running LLM (lmd at :5400, LM Studio at :1234, ollama at :11434),
selects fast and deep models automatically, and writes config.toml.

If the endpoint requires authentication, pass --token.
To target a specific URL (e.g. OpenAI, remote ollama), pass --url.

Safe to re-run. Use --force to overwrite an existing config.
Use --json for machine-readable output (useful when called by an agent).`,
		Example: `  lm-review init
  lm-review init --token sk-lm-abc:xyz
  lm-review init --url https://api.openai.com --token sk-...
  lm-review init --json`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runInit(cmd, force, jsonOut, token, url)
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output result as JSON")
	cmd.Flags().BoolVar(&force, "force", false, "Overwrite existing config")
	cmd.Flags().StringVar(&token, "token", "", "API token for the LLM backend")
	cmd.Flags().StringVar(&url, "url", "", "Explicit backend URL (skips auto-detection)")
	return cmd
}

// runInit performs the init flow: detect backend, choose models,
// write config, render output.
func runInit(cmd *cobra.Command, force, jsonOut bool, token, url string) error {
	configPath := xdg.ConfigPath()

	if !force {
		_, statErr := os.Stat(configPath)
		if statErr == nil {
			cmd.PrintErrf("config already exists at %s\nUse --force to overwrite, or edit directly.\n", configPath)
			return nil
		}
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	backend, err := resolveInitBackend(ctx, url, token)
	if err != nil {
		return err
	}

	fast, deep := lmstudio.SelectModels(backend.Models)
	if fast == "" {
		slog.ErrorContext(ctx, "lm-review.init.no_models", "url", backend.URL)
		return fmt.Errorf("no models found at %s, load a model first", backend.URL)
	}

	writeErr := writeConfig(configPath, backend.URL, backend.Token, fast, deep)
	if writeErr != nil {
		slog.ErrorContext(ctx, "lm-review.init.write_failed", "path", configPath, "err", writeErr)
		return fmt.Errorf("write config: %w", writeErr)
	}

	result := InitResult{
		ConfigPath: configPath,
		Backend:    backend.Name,
		URL:        backend.URL,
		FastModel:  fast,
		DeepModel:  deep,
		Models:     backend.Models,
	}

	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		encErr := enc.Encode(result)
		if encErr != nil {
			return fmt.Errorf("encode json: %w", encErr)
		}
		return nil
	}

	cmd.Printf("config written: %s\n", configPath)
	cmd.Printf("backend:    %s (%s)\n", result.Backend, result.URL)
	cmd.Printf("fast model: %s\n", result.FastModel)
	cmd.Printf("deep model: %s\n", result.DeepModel)
	return nil
}

// resolveInitBackend either lists models at an explicit URL or runs
// auto-detection across known local endpoints.
func resolveInitBackend(ctx context.Context, url, token string) (*lmstudio.Backend, error) {
	if url != "" {
		models, lerr := lmstudio.ListModels(ctx, url, token)
		if lerr != nil {
			slog.ErrorContext(ctx, "lm-review.init.list_models_failed", "url", url, "err", lerr)
			return nil, fmt.Errorf("cannot reach %s: %w", url, lerr)
		}
		return &lmstudio.Backend{Name: "custom", URL: url, Token: token, Models: models}, nil
	}
	backend, err := lmstudio.Detect(ctx, token)
	if err != nil {
		slog.ErrorContext(ctx, "lm-review.init.detect_failed", "err", err)
		return nil, fmt.Errorf("%w\n\nStart your LLM backend, then re-run.\nOr specify explicitly: lm-review init --url <url> --token <token>", err)
	}
	return backend, nil
}

func writeConfig(path, url, token, fastModel, deepModel string) error {
	mkErr := os.MkdirAll(filepath.Dir(path), 0o700)
	if mkErr != nil {
		return fmt.Errorf("mkdir config dir: %w", mkErr)
	}
	content := fmt.Sprintf(`[openai_compat]
url        = %q
token      = %q
fast_model = %q
deep_model = %q

[judge]
model = %q
listen_address = %q
`, url, token, fastModel, deepModel, config.DefaultJudgeModel, config.DefaultJudgeListenAddress)
	writeErr := os.WriteFile(path, []byte(content), 0o600)
	if writeErr != nil {
		return fmt.Errorf("write file: %w", writeErr)
	}
	return nil
}
