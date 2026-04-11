package lmstudio

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

const lmsCLI = "/Applications/LM Studio.app/Contents/Resources/app/.webpack/lms"

// EnsureReady ensures the LM Studio server is running and the given model is loaded.
// This enables fully headless operation - no LM Studio GUI required.
func EnsureReady(ctx context.Context, baseURL, token, model string) error {
	if err := ensureServerRunning(ctx, baseURL, token); err != nil {
		return fmt.Errorf("start LM Studio server: %w", err)
	}
	if err := ensureModelLoaded(ctx, model); err != nil {
		return fmt.Errorf("load model %s: %w", model, err)
	}
	return nil
}

// ensureServerRunning checks the HTTP API directly - more reliable than lms CLI IPC.
func ensureServerRunning(ctx context.Context, baseURL, token string) error {
	if pingServer(ctx, baseURL, token) {
		return nil
	}

	// Server not responding - try to start it via lms CLI.
	out, err := exec.CommandContext(ctx, lmsCLI, "server", "start").CombinedOutput()
	if err != nil {
		return fmt.Errorf("lms server start: %w: %s", err, out)
	}

	// Wait for server to accept connections.
	return waitForServer(ctx, baseURL, token, 15*time.Second)
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

// ensureModelLoaded loads the model if it isn't already in memory.
func ensureModelLoaded(ctx context.Context, model string) error {
	loaded, err := loadedModels(ctx)
	if err != nil {
		return err
	}

	for _, m := range loaded {
		if strings.EqualFold(m, model) || strings.HasSuffix(strings.ToLower(m), strings.ToLower(model)) {
			return nil
		}
	}

	out, err := exec.CommandContext(ctx, lmsCLI, "load", model, "--gpu=max").CombinedOutput()
	if err != nil {
		return fmt.Errorf("lms load %s: %w: %s", model, err, out)
	}

	return nil
}

// loadedModels returns the list of currently loaded model identifiers via lms ps.
func loadedModels(ctx context.Context) ([]string, error) {
	out, err := exec.CommandContext(ctx, lmsCLI, "ps").CombinedOutput()
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
func UnloadModel(ctx context.Context, model string) error {
	out, err := exec.CommandContext(ctx, lmsCLI, "unload", model).CombinedOutput()
	if err != nil {
		return fmt.Errorf("lms unload %s: %w: %s", model, err, out)
	}
	return nil
}
