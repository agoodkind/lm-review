package lmstudio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"goodkind.io/gklog"
)

// LMDLoadRequest is the request body for the LMD load API.
type LMDLoadRequest struct {
	Model          string `json:"model"`
	ContextLength  int    `json:"context_length,omitempty"`
	EstimateOnly   bool   `json:"estimate_only,omitempty"`
	EchoLoadConfig bool   `json:"echo_load_config,omitempty"`
}

// LMDLoadResponse is the subset of the LMD load response used by lm-review.
type LMDLoadResponse struct {
	Type                   string   `json:"type"`
	InstanceID             string   `json:"instance_id"`
	LoadTimeSeconds        float64  `json:"load_time_seconds"`
	Status                 string   `json:"status"`
	CanLoad                *bool    `json:"can_load,omitempty"`
	EstimatedTotalMemoryGB *float64 `json:"estimated_total_memory_gb,omitempty"`
}

// PreflightLoad posts a load or estimate request to the LMD load API.
func PreflightLoad(ctx context.Context, baseURL string, token string, request LMDLoadRequest) (*LMDLoadResponse, error) {
	body, err := json.Marshal(request)
	if err != nil {
		gklog.LoggerFromContext(ctx).ErrorContext(ctx, "lmd.preflight.marshal_failed", "err", err)
		return nil, errors.New("marshal LMD load request failed: " + err.Error())
	}

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/api/v1/models/load", bytes.NewReader(body))
	if err != nil {
		gklog.LoggerFromContext(ctx).ErrorContext(ctx, "lmd.preflight.request_build_failed", "err", err)
		return nil, errors.New("build LMD load request failed: " + err.Error())
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	if token != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+token)
	}

	response, err := http.DefaultClient.Do(httpRequest)
	if err != nil {
		gklog.LoggerFromContext(ctx).ErrorContext(ctx, "lmd.preflight.send_failed", "err", err)
		return nil, errors.New("send LMD load request failed: " + err.Error())
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		gklog.LoggerFromContext(ctx).ErrorContext(ctx, "lmd.preflight.bad_status", "status_code", response.StatusCode)
		return nil, fmt.Errorf("LMD load API returned %d", response.StatusCode)
	}

	var decoded LMDLoadResponse
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		gklog.LoggerFromContext(ctx).ErrorContext(ctx, "lmd.preflight.decode_failed", "err", err)
		return nil, errors.New("decode LMD load response failed: " + err.Error())
	}
	return &decoded, nil
}
