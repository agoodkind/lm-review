package lmstudio

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const lmsCLI = "/Applications/LM Studio.app/Contents/Resources/app/.webpack/lms"

// EnsureReady ensures the LM Studio server is running and the given model is loaded.
// This enables fully headless operation - no LM Studio GUI required.
func EnsureReady(ctx context.Context, model string) error {
	if err := ensureServerRunning(ctx); err != nil {
		return fmt.Errorf("start LM Studio server: %w", err)
	}
	if err := ensureModelLoaded(ctx, model); err != nil {
		return fmt.Errorf("load model %s: %w", model, err)
	}
	return nil
}

// ensureServerRunning starts the LM Studio server if it isn't already running.
func ensureServerRunning(ctx context.Context) error {
	out, err := exec.CommandContext(ctx, lmsCLI, "server", "status").Output()
	if err == nil && strings.Contains(string(out), "running") {
		return nil
	}

	startOut, startErr := exec.CommandContext(ctx, lmsCLI, "server", "start").CombinedOutput()
	if startErr != nil {
		return fmt.Errorf("lms server start: %w: %s", startErr, startOut)
	}

	// Wait for server to accept connections.
	return waitForServer(ctx, 15*time.Second)
}

// ensureModelLoaded loads the model if it isn't already in memory.
func ensureModelLoaded(ctx context.Context, model string) error {
	loaded, err := loadedModels(ctx)
	if err != nil {
		return err
	}

	for _, m := range loaded {
		if strings.EqualFold(m, model) || strings.HasSuffix(strings.ToLower(m), strings.ToLower(model)) {
			return nil // already loaded
		}
	}

	out, err := exec.CommandContext(ctx, lmsCLI, "load", model, "--gpu=max").CombinedOutput()
	if err != nil {
		return fmt.Errorf("lms load %s: %w: %s", model, err, out)
	}

	return nil
}

// loadedModels returns the list of currently loaded model identifiers.
func loadedModels(ctx context.Context) ([]string, error) {
	out, err := exec.CommandContext(ctx, lmsCLI, "ps").Output()
	if err != nil {
		return nil, fmt.Errorf("lms ps: %w", err)
	}

	var models []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "LLM") || strings.HasPrefix(line, "EMBEDDING") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) > 0 {
			models = append(models, fields[0])
		}
	}

	return models, nil
}

// waitForServer polls localhost:1234 until the server responds or the timeout elapses.
func waitForServer(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		out, err := exec.CommandContext(ctx, lmsCLI, "server", "status").Output()
		if err == nil && strings.Contains(string(out), "running") {
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
