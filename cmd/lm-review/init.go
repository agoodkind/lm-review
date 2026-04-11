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
// Machine-readable via --json flag.
type InitResult struct {
	ConfigPath string `json:"config_path"`
	Backend    string `json:"backend"`
	URL        string `json:"url"`
	FastModel  string `json:"fast_model"`
	DeepModel  string `json:"deep_model"`
	Models     []string `json:"models_available"`
}

func newInitCmd() *cobra.Command {
	var jsonOut bool
	var force bool

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Auto-detect backend and write config",
		Long: `Probes localhost for a running LLM backend (LM Studio, ollama),
selects fast and deep models automatically, and writes ~/.config/lm-review/config.toml.

Safe to run repeatedly. Use --force to overwrite an existing config.
Use --json for machine-readable output.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			configPath := xdg.ConfigPath()

			if !force {
				if _, err := os.Stat(configPath); err == nil {
					fmt.Fprintf(os.Stderr, "config already exists at %s (use --force to overwrite)\n", configPath)
					return nil
				}
			}

			ctx := context.Background()

			backend, err := lmstudio.Detect(ctx)
			if err != nil {
				return fmt.Errorf("no LLM backend found: %w\n\nStart LM Studio or ollama, then re-run: lm-review init", err)
			}

			fast, deep := lmstudio.SelectModels(backend.Models)
			if fast == "" {
				return fmt.Errorf("no models available at %s - load a model first", backend.URL)
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

			fmt.Printf("✅ Config written: %s\n", configPath)
			fmt.Printf("   Backend:    %s (%s)\n", result.Backend, result.URL)
			fmt.Printf("   Fast model: %s\n", result.FastModel)
			fmt.Printf("   Deep model: %s\n", result.DeepModel)
			fmt.Printf("\nRun: lm-review diff\n")
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output result as JSON")
	cmd.Flags().BoolVar(&force, "force", false, "Overwrite existing config")
	return cmd
}

func writeConfig(path, url, token, fastModel, deepModel string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}

	content := fmt.Sprintf(`[lmstudio]
url        = %q
token      = %q
fast_model = %q
deep_model = %q
`, url, token, fastModel, deepModel)

	return os.WriteFile(path, []byte(content), 0o600)
}
