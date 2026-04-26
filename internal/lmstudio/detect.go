package lmstudio

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Backend represents a detected LLM backend.
type Backend struct {
	Name   string
	URL    string
	Token  string
	Models []string
}

// Detect probes known local endpoints and returns the first reachable one.
// Pass token if the endpoint requires authentication.
func Detect(ctx context.Context, token string) (*Backend, error) {
	slog.Debug("lmstudio.detect.begin")
	candidates := []struct {
		name  string
		url   string
		token string
	}{
		{"lmd", "http://localhost:5400", token},
		{"LM Studio", "http://localhost:1234", token},
		{"ollama", "http://localhost:11434", "ollama"},
	}

	var detected *Backend
	for _, c := range candidates {
		models, err := ListModels(ctx, c.url, c.token)
		if err == nil {
			detected = &Backend{Name: c.name, URL: c.url, Token: c.token, Models: models}
			break
		}
	}
	if detected != nil {
		slog.Info("lmstudio.detect.completed", "backend", detected.Name, "url", detected.URL, "model_count", len(detected.Models))
		return detected, nil
	}

	slog.Warn("lmstudio.detect.failed")
	return nil, fmt.Errorf("no LLM backend found at localhost:5400, localhost:1234, or localhost:11434")
}

// ListModels returns available model IDs from an OpenAI-compatible endpoint.
func ListModels(ctx context.Context, baseURL, token string) ([]string, error) {
	slog.Debug("lmstudio.list_models.begin", "base_url", baseURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v1/models", nil)
	if err != nil {
		slog.Warn("lmstudio.list_models.request_failed", "base_url", baseURL, "err", err)
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		slog.Warn("lmstudio.list_models.http_failed", "base_url", baseURL, "err", err)
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusUnauthorized {
		slog.Warn("lmstudio.list_models.unauthorized", "base_url", baseURL)
		return nil, fmt.Errorf("authentication required - pass --token")
	}
	if resp.StatusCode != http.StatusOK {
		slog.Warn("lmstudio.list_models.bad_status", "base_url", baseURL, "status", resp.StatusCode)
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}

	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		slog.Warn("lmstudio.list_models.decode_failed", "base_url", baseURL, "err", err)
		return nil, err
	}

	ids := make([]string, 0, len(body.Data))
	for _, m := range body.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	slog.Debug("lmstudio.list_models.completed", "base_url", baseURL, "model_count", len(ids))
	return ids, nil
}

var reParamCount = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)b`)
var reActiveParams = regexp.MustCompile(`(?i)-a(\d+(?:\.\d+)?)b`)

// SelectModels picks the best fast and deep models from a list.
// Fast: smallest effective size, coder-named preferred.
// Deep: largest total size.
func SelectModels(models []string) (fast, deep string) {
	type scored struct {
		id         string
		effectiveB float64
		totalB     float64
		coderBonus float64
	}

	var candidates []scored
	for _, id := range models {
		lower := strings.ToLower(id)
		if strings.Contains(lower, "embed") {
			continue
		}

		var totalB, activeB float64
		if m := reParamCount.FindStringSubmatch(id); m != nil {
			totalB, _ = strconv.ParseFloat(m[1], 64)
		}
		if m := reActiveParams.FindStringSubmatch(id); m != nil {
			activeB, _ = strconv.ParseFloat(m[1], 64)
		}

		effective := totalB
		if activeB > 0 {
			effective = activeB
		}
		if effective == 0 {
			effective = 1
		}

		coderBonus := 0.0
		if strings.Contains(lower, "coder") || strings.Contains(lower, "devstral") {
			coderBonus = 1.0
		}

		candidates = append(candidates, scored{id: id, effectiveB: effective, totalB: totalB, coderBonus: coderBonus})
	}

	if len(candidates) == 0 {
		if len(models) > 0 {
			return models[0], models[0]
		}
		return "", ""
	}

	bestFast := candidates[0]
	for _, c := range candidates[1:] {
		if c.effectiveB-c.coderBonus*5 < bestFast.effectiveB-bestFast.coderBonus*5 {
			bestFast = c
		}
	}

	bestDeep := candidates[0]
	for _, c := range candidates[1:] {
		if c.totalB > bestDeep.totalB {
			bestDeep = c
		}
	}

	if len(candidates) == 1 {
		return bestFast.id, bestFast.id
	}

	return bestFast.id, bestDeep.id
}
