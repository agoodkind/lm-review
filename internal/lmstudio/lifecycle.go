package lmstudio

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// lmsPlatformDefaults lists known default lms CLI locations by OS.
// Users with non-standard installs should set lms_path in config.toml.
var lmsPlatformDefaults = map[string][]string{
	"darwin": {
		"/Applications/LM Studio.app/Contents/Resources/app/.webpack/lms",
	},
	"linux": {
		// AppImage installs typically add lms to PATH; these are fallbacks.
		"/opt/lmstudio/lms",
		"/usr/local/bin/lms",
	},
	"windows": {
		`%LOCALAPPDATA%\LM Studio\lms.exe`,
	},
}

// findLMS locates the lms CLI binary.
// Priority: configPath override → PATH → platform-specific defaults.
// Returns ("", false) if lms is not available - lifecycle management disabled.
func findLMS(configPath string) (string, bool) {
	if configPath == "none" {
		return "", false
	}

	if configPath != "" {
		return configPath, true
	}

	// Check PATH first - works for any platform where user installed properly.
	if p, err := exec.LookPath("lms"); err == nil {
		return p, true
	}

	// Fall back to platform-specific defaults.
	for _, candidate := range lmsPlatformDefaults[runtime.GOOS] {
		if p, err := exec.LookPath(candidate); err == nil {
			return p, true
		}
		// Try as literal path (not just name).
		if out, err := exec.Command(candidate, "--version").Output(); err == nil && len(out) > 0 {
			return candidate, true
		}
	}

	return "", false
}

// EnsureReady ensures the LM Studio server is running and the given model is loaded.
// If the lms CLI is not available, it only checks the HTTP API and returns an error
// if the server is not already running (user must start LM Studio manually).
func EnsureReady(ctx context.Context, baseURL, token, model, lmsPath string) error {
	lmsBin, hasLMS := findLMS(lmsPath)

	if pingServer(ctx, baseURL, token) {
		// Server already up - just ensure model is loaded.
		if hasLMS {
			return ensureModelLoaded(ctx, lmsBin, model)
		}
		return nil
	}

	if !hasLMS {
		return fmt.Errorf("LM Studio server is not running at %s and lms CLI was not found. "+
			"Start LM Studio manually, or set lms_path in ~/.config/lm-review/config.toml", baseURL)
	}

	// Start server via lms CLI.
	out, err := exec.CommandContext(ctx, lmsBin, "server", "start").CombinedOutput()
	if err != nil {
		return fmt.Errorf("lms server start: %w: %s", err, out)
	}

	if err := waitForServer(ctx, baseURL, token, 15*time.Second); err != nil {
		return fmt.Errorf("server did not start: %w", err)
	}

	return ensureModelLoaded(ctx, lmsBin, model)
}

// ensureModelLoaded loads the model if it is not already in memory.
func ensureModelLoaded(ctx context.Context, lmsBin, model string) error {
	loaded, err := loadedModels(ctx, lmsBin)
	if err != nil {
		return err
	}

	for _, m := range loaded {
		if strings.EqualFold(m, model) || strings.HasSuffix(strings.ToLower(m), strings.ToLower(model)) {
			return nil
		}
	}

	out, err := exec.CommandContext(ctx, lmsBin, "load", model, "--gpu=max").CombinedOutput()
	if err != nil {
		return fmt.Errorf("lms load %s: %w: %s", model, err, out)
	}

	return nil
}

// loadedModels returns currently loaded model identifiers via lms ps.
func loadedModels(ctx context.Context, lmsBin string) ([]string, error) {
	out, err := exec.CommandContext(ctx, lmsBin, "ps").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("lms ps: %w", err)
	}

	var models []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "IDENTIFIER") || strings.HasPrefix(line, "No models") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) > 0 {
			models = append(models, fields[0])
		}
	}

	return models, nil
}

// pingServer returns true if the LM Studio HTTP API is reachable.
func pingServer(ctx context.Context, baseURL, token string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v1/models", nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+token)

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode < 500
}

// waitForServer polls the HTTP API until the server responds or timeout.
func waitForServer(ctx context.Context, baseURL, token string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if pingServer(ctx, baseURL, token) {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("LM Studio server did not start within %s", timeout)
}

// UnloadModel unloads a model to free memory.
func UnloadModel(ctx context.Context, lmsBin, model string) error {
	if lmsBin == "" {
		return nil
	}
	out, err := exec.CommandContext(ctx, lmsBin, "unload", model).CombinedOutput()
	if err != nil {
		return fmt.Errorf("lms unload %s: %w: %s", model, err, out)
	}
	return nil
}
