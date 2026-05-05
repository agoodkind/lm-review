package analyzer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
)

type semgrepSource struct{}

type semgrepResult struct {
	Results []struct {
		CheckID string `json:"check_id"`
		Path    string `json:"path"`
		Start   struct {
			Line int `json:"line"`
		} `json:"start"`
		End struct {
			Line int `json:"line"`
		} `json:"end"`
		Extra struct {
			Message  string `json:"message"`
			Severity string `json:"severity"`
		} `json:"extra"`
	} `json:"results"`
}

func (semgrepSource) Name() string { return "semgrep" }

// semgrepInstalled reports whether the semgrep binary is on PATH. The
// boolean result lets callers short-circuit cleanly without juggling
// an "absent is not an error" sentinel.
func semgrepInstalled(ctx context.Context) bool {
	_, err := exec.LookPath("semgrep")
	if err != nil {
		slog.DebugContext(ctx, "semgrep.run.skipped", "reason", "binary not found")
		return false
	}
	return true
}

func (semgrepSource) Run(ctx context.Context, opts RunOptions) ([]Finding, error) {
	if !semgrepInstalled(ctx) {
		return nil, nil
	}
	configPath, ok := findSemgrepConfig(opts.RepoRoot)
	if !ok {
		return nil, nil
	}

	args := []string{"--config", configPath, "--json", opts.RepoRoot}
	cmd := exec.CommandContext(ctx, "semgrep", args...)
	output, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			slog.ErrorContext(ctx, "semgrep.run.failed", "err", err, "stderr", string(exitErr.Stderr))
			return nil, fmt.Errorf("semgrep: %s", string(exitErr.Stderr))
		}
		slog.ErrorContext(ctx, "semgrep.run.failed", "err", err)
		return nil, fmt.Errorf("semgrep: %w", err)
	}

	var result semgrepResult
	if jsonErr := json.Unmarshal(output, &result); jsonErr != nil {
		slog.ErrorContext(ctx, "semgrep.decode_json.failed", "err", jsonErr)
		return nil, fmt.Errorf("decode semgrep json: %w", jsonErr)
	}

	allowedFiles := make(map[string]bool, len(opts.Files))
	for _, file := range opts.Files {
		allowedFiles[filepath.Clean(file)] = true
	}

	findings := make([]Finding, 0, len(result.Results))
	for _, item := range result.Results {
		rel, err := filepath.Rel(opts.RepoRoot, item.Path)
		if err != nil {
			continue
		}
		rel = filepath.Clean(rel)
		if len(allowedFiles) > 0 && !allowedFiles[rel] {
			continue
		}
		if len(opts.EnabledChecks) > 0 && !slices.Contains(opts.EnabledChecks, item.CheckID) {
			continue
		}
		findings = append(findings, Finding{
			Tool:      "semgrep",
			Check:     item.CheckID,
			Severity:  semgrepSeverity(item.Extra.Severity),
			File:      rel,
			Line:      item.Start.Line,
			EndLine:   item.End.Line,
			Message:   item.Extra.Message,
			Principle: "",
			Fix:       "",
		})
	}
	return findings, nil
}

func findSemgrepConfig(repoRoot string) (string, bool) {
	candidates := []string{
		filepath.Join(repoRoot, ".semgrep.yml"),
		filepath.Join(repoRoot, ".semgrep.yaml"),
		filepath.Join(repoRoot, ".semgrep", "config.yml"),
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate, true
		}
	}
	return "", false
}

func semgrepSeverity(input string) Severity {
	switch input {
	case "ERROR":
		return SeverityError
	case "INFO":
		return SeverityInfo
	default:
		return SeverityWarning
	}
}
