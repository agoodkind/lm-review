package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"goodkind.io/lm-review/internal/lmstudio"
	"goodkind.io/lm-review/internal/xdg"
)

// InitResult is the structured output of lm-review init.
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
		RunE: func(cmd *cobra.Command, args []string) error {
			configPath := xdg.ConfigPath()

			if !force {
				if _, err := os.Stat(configPath); err == nil {
					cmd.PrintErrf("config already exists at %s\nUse --force to overwrite, or edit directly.\n", configPath)
					return nil
				}
			}

			ctx := context.Background()

			var backend *lmstudio.Backend
			var err error

			if url != "" {
				// Explicit URL - just list models there.
				models, lerr := lmstudio.ListModels(ctx, url, token)
				if lerr != nil {
					return fmt.Errorf("cannot reach %s: %w", url, lerr)
				}
				backend = &lmstudio.Backend{Name: "custom", URL: url, Token: token, Models: models}
			} else {
				backend, err = lmstudio.Detect(ctx, token)
				if err != nil {
					return fmt.Errorf("%w\n\nStart your LLM backend, then re-run.\nOr specify explicitly: lm-review init --url <url> --token <token>", err)
				}
			}

			fast, deep := lmstudio.SelectModels(backend.Models)
			if fast == "" {
				return fmt.Errorf("no models found at %s - load a model first", backend.URL)
			}

			if err := writeConfig(configPath, backend.URL, backend.Token, fast, deep); err != nil {
				return fmt.Errorf("write config: %w", err)
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
				return enc.Encode(result)
			}

			cmd.Printf("config written: %s\n", configPath)
			cmd.Printf("backend:    %s (%s)\n", result.Backend, result.URL)
			cmd.Printf("fast model: %s\n", result.FastModel)
			cmd.Printf("deep model: %s\n", result.DeepModel)
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output result as JSON")
	cmd.Flags().BoolVar(&force, "force", false, "Overwrite existing config")
	cmd.Flags().StringVar(&token, "token", "", "API token for the LLM backend")
	cmd.Flags().StringVar(&url, "url", "", "Explicit backend URL (skips auto-detection)")
	return cmd
}

func writeConfig(path, url, token, fastModel, deepModel string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	content := fmt.Sprintf(`[openai_compat]
url        = %q
token      = %q
fast_model = %q
deep_model = %q
`, url, token, fastModel, deepModel)
	return os.WriteFile(path, []byte(content), 0o600)
}
