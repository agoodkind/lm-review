package lmstudio

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Backend represents a detected LLM backend.
type Backend struct {
	Name   string // "LM Studio" | "ollama" | "custom"
	URL    string
	Token  string
	Models []string
}

// Detect probes known local endpoints and returns the first reachable one.
// Order: LM Studio (1234) → ollama (11434).
func Detect(ctx context.Context) (*Backend, error) {
	candidates := []struct {
		name     string
		url      string
		tokenFn  func() string
	}{
		{"LM Studio", "http://localhost:1234", lmStudioToken},
		{"ollama", "http://localhost:11434", func() string { return "ollama" }},
	}

	for _, c := range candidates {
		token := c.tokenFn()
		models, err := listModels(ctx, c.url, token)
		if err == nil {
			return &Backend{Name: c.name, URL: c.url, Token: token, Models: models}, nil
		}
	}

	return nil, fmt.Errorf("no LLM backend detected on localhost:1234 or localhost:11434")
}

// ListModels returns available model IDs for the given endpoint.
func ListModels(ctx context.Context, url, token string) ([]string, error) {
	return listModels(ctx, url, token)
}

func listModels(ctx context.Context, baseURL, token string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/v1/models", nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}

	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}

	ids := make([]string, 0, len(body.Data))
	for _, m := range body.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	return ids, nil
}

// lmStudioToken reads the LM Studio API token from its internal credentials file.
func lmStudioToken() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	type permissionsStore struct {
		JSON struct {
			Tokens []struct {
				ClientIdentifier         string `json:"clientIdentifier"`
				ClientPasskeySHA512Base64 string `json:"clientPasskeySHA512Base64"`
			} `json:"tokens"`
		} `json:"json"`
	}

	// The lms-key-2 file holds the raw passkey for the built-in client.
	keyPath := filepath.Join(home, ".lmstudio", ".internal", "lms-key-2")
	key, err := os.ReadFile(keyPath)
	if err != nil {
		return ""
	}

	// Read the matching clientIdentifier from permissions-store.json.
	storePath := filepath.Join(home, ".lmstudio", ".internal", "permissions-store.json")
	data, err := os.ReadFile(storePath)
	if err != nil {
		return ""
	}

	var store permissionsStore
	if err := json.Unmarshal(data, &store); err != nil {
		return ""
	}

	keyStr := strings.TrimSpace(string(key))
	// The built-in key's identifier matches the first 8 chars of the key.
	for _, tok := range store.JSON.Tokens {
		if strings.HasPrefix(keyStr, tok.ClientIdentifier) {
			return fmt.Sprintf("sk-lm-%s:%s", tok.ClientIdentifier, keyStr[len(tok.ClientIdentifier):])
		}
	}

	return ""
}

// reParamCount matches parameter counts in model names like 30B, 122B, 7b, 3B.
var reParamCount = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)b`)

// reActiveParams matches MoE active params like A3B, A10B.
var reActiveParams = regexp.MustCompile(`(?i)-a(\d+(?:\.\d+)?)b`)

// SelectModels picks the best fast and deep models from a list.
// Fast: smallest effective size with "coder" preference.
// Deep: largest effective size.
func SelectModels(models []string) (fast, deep string) {
	type scored struct {
		id          string
		effectiveB  float64 // effective parameter count (active params for MoE)
		totalB      float64
		coderBonus  float64
	}

	var candidates []scored
	for _, id := range models {
		lower := strings.ToLower(id)

		// Skip embedding models.
		if strings.Contains(lower, "embed") || strings.Contains(lower, "nomic") {
			continue
		}

		var totalB, activeB float64

		// Extract total param count.
		if m := reParamCount.FindStringSubmatch(id); m != nil {
			v, _ := strconv.ParseFloat(m[1], 64)
			totalB = v
		}

		// Extract active param count (MoE).
		if m := reActiveParams.FindStringSubmatch(id); m != nil {
			v, _ := strconv.ParseFloat(m[1], 64)
			activeB = v
		}

		effective := totalB
		if activeB > 0 {
			effective = activeB
		}
		if effective == 0 {
			effective = 1 // unknown size - treat as tiny
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

	// Fast: smallest effective size, coder preferred.
	bestFast := candidates[0]
	for _, c := range candidates[1:] {
		fastScore := c.effectiveB - c.coderBonus*5
		bestScore := bestFast.effectiveB - bestFast.coderBonus*5
		if fastScore < bestScore {
			bestFast = c
		}
	}

	// Deep: largest total size.
	bestDeep := candidates[0]
	for _, c := range candidates[1:] {
		if c.totalB > bestDeep.totalB {
			bestDeep = c
		}
	}

	// If only one model, use it for both.
	if len(candidates) == 1 {
		return bestFast.id, bestFast.id
	}

	return bestFast.id, bestDeep.id
}
