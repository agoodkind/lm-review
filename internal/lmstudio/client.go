// Package lmstudio wraps the official openai-go/v3 SDK pointed at an OpenAI-compatible endpoint.
package lmstudio

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/shared"

	"goodkind.io/gklog"
	"goodkind.io/lm-review/internal/clock"
	"goodkind.io/lm-review/internal/config"
	"goodkind.io/lm-review/internal/requestmeta"
)

const (
	// repWindowSize is the minimum char length of a repeated block to flag.
	repWindowSize = 80

	// repThreshold is the number of times a block must repeat to abort.
	repThreshold = 4
)

// Client wraps openai-go/v3 configured for the selected OpenAI-compatible API.
type Client struct {
	inner          openai.Client
	baseURL        string
	model          string
	maxTokens      int64
	requestTimeout time.Duration
	settings       config.ChatSettings
}

// SchemaGenerationOptions contains caller-declared structured inference controls.
type SchemaGenerationOptions struct {
	ReasoningEffort     string
	MaxCompletionTokens *int64
	Temperature         *float64
}

// ChatResult contains the structured output and backend invocation metadata.
type ChatResult struct {
	Content            string
	RequestID          string
	ActualModel        string
	BackendFingerprint string
	BackendVersion     string
	PromptTokens       int64
	CompletionTokens   int64
	TotalTokens        int64
	FinishReason       string
	Latency            time.Duration
}

// New creates a Client targeting the given OpenAI-compatible base URL with the provided token.
// maxTokens caps the response length to prevent infinite generation loops.
func New(baseURL, token, model string, maxTokens int, requestTimeout time.Duration, settings config.ChatSettings) *Client {
	c := openai.NewClient(
		option.WithBaseURL(baseURL+"/v1"),
		option.WithAPIKey(token),
		option.WithHTTPClient(&http.Client{}),
	)
	return &Client{
		inner:          c,
		baseURL:        baseURL,
		model:          model,
		maxTokens:      int64(maxTokens),
		requestTimeout: requestTimeout,
		settings:       settings,
	}
}

// Chat sends a system prompt and user message, returning the assistant reply.
func (c *Client) Chat(ctx context.Context, systemPrompt, userMessage string) (string, error) {
	return c.chat(ctx, systemPrompt, userMessage, nil)
}

// ChatReview sends a review prompt and requests the structured review schema.
func (c *Client) ChatReview(ctx context.Context, systemPrompt, userMessage string) (string, error) {
	responseFormat := reviewResultResponseFormat()
	return c.chat(ctx, systemPrompt, userMessage, &responseFormat)
}

// ChatSchema sends a prompt and input while requiring the supplied JSON Schema.
func (c *Client) ChatSchema(ctx context.Context, prompt, input string, schema json.RawMessage) (string, error) {
	result, err := c.ChatSchemaDetailed(ctx, prompt, input, schema, SchemaGenerationOptions{
		ReasoningEffort:     "",
		MaxCompletionTokens: nil,
		Temperature:         nil,
	})
	return result.Content, err
}

// ChatSchemaDetailed sends schema-driven inference and returns backend metadata.
func (c *Client) ChatSchemaDetailed(
	ctx context.Context,
	prompt string,
	input string,
	schema json.RawMessage,
	options SchemaGenerationOptions,
) (ChatResult, error) {
	responseFormat := jsonSchemaResponseFormat("inference_output", schema)
	return c.chatDetailed(ctx, prompt, input, &responseFormat, &options)
}

func (c *Client) chat(ctx context.Context, systemPrompt, userMessage string, responseFormat *openai.ChatCompletionNewParamsResponseFormatUnion) (string, error) {
	result, err := c.chatDetailed(ctx, systemPrompt, userMessage, responseFormat, nil)
	return result.Content, err
}

func (c *Client) chatDetailed(
	ctx context.Context,
	systemPrompt string,
	userMessage string,
	responseFormat *openai.ChatCompletionNewParamsResponseFormatUnion,
	schemaOptions *SchemaGenerationOptions,
) (ChatResult, error) {
	metadata := requestmeta.From(ctx)
	requestID := metadata.RequestID()
	if requestID == "" {
		requestID = newChatRequestID()
	}

	log := c.requestLogger(ctx, metadata, requestID, systemPrompt, userMessage)
	var httpResp *http.Response
	opts := c.requestOptions(metadata, requestID, &httpResp, schemaOptions == nil)

	start := clock.Now()
	log.InfoContext(ctx, "openai.chat.begin")
	params := c.chatParams(systemPrompt, userMessage, responseFormat, schemaOptions)
	resp, err := c.inner.Chat.Completions.New(ctx, params, opts...)
	latency := clock.Since(start)
	if err != nil {
		safeError := newChatRequestError(ctx, err, responseStatusCode(httpResp))
		log.ErrorContext(ctx, "openai.chat.failed",
			"latency_ms", latency.Milliseconds(),
			"status_code", responseStatusCode(httpResp),
			"error_type", fmt.Sprintf("%T", err),
			"err", safeError)
		return ChatResult{}, safeError
	}
	if len(resp.Choices) == 0 {
		err := fmt.Errorf("OpenAI-compatible backend returned no choices")
		log.ErrorContext(ctx, "openai.chat.no_choices",
			"latency_ms", latency.Milliseconds(),
			"status_code", responseStatusCode(httpResp),
			"err", err)
		return ChatResult{}, err
	}
	content := resp.Choices[0].Message.Content
	if err := detectRepetition(content); err != nil {
		log.ErrorContext(ctx, "openai.chat.repetition_detected",
			"latency_ms", latency.Milliseconds(),
			"status_code", responseStatusCode(httpResp),
			"response_bytes", len(content),
			"err", err)
		return ChatResult{
			Content:            content,
			RequestID:          "",
			ActualModel:        "",
			BackendFingerprint: "",
			BackendVersion:     "",
			PromptTokens:       0,
			CompletionTokens:   0,
			TotalTokens:        0,
			FinishReason:       "",
			Latency:            0,
		}, err
	}
	log.InfoContext(ctx, "openai.chat.end",
		"latency_ms", latency.Milliseconds(),
		"status_code", responseStatusCode(httpResp),
		"choice_count", len(resp.Choices),
		"response_bytes", len(content))
	backendVersion := ""
	if httpResp != nil {
		backendVersion = firstHeader(
			httpResp.Header,
			"X-Backend-Version",
			"X-LMD-Version",
			"OpenAI-Version",
		)
	}
	return ChatResult{
		Content:            content,
		RequestID:          requestID,
		ActualModel:        resp.Model,
		BackendFingerprint: backendFingerprint(resp.RawJSON()),
		BackendVersion:     backendVersion,
		PromptTokens:       resp.Usage.PromptTokens,
		CompletionTokens:   resp.Usage.CompletionTokens,
		TotalTokens:        resp.Usage.TotalTokens,
		FinishReason:       resp.Choices[0].FinishReason,
		Latency:            latency,
	}, nil
}

func backendFingerprint(rawResponse string) string {
	var metadata struct {
		SystemFingerprint string `json:"system_fingerprint"`
	}
	if err := json.Unmarshal([]byte(rawResponse), &metadata); err != nil {
		return ""
	}
	return metadata.SystemFingerprint
}

type chatRequestError struct {
	statusCode int
	errorType  string
	cause      error
}

func newChatRequestError(ctx context.Context, requestError error, statusCode int) error {
	var cause error
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(requestError, context.Canceled) {
		cause = context.Canceled
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(requestError, context.DeadlineExceeded) {
		cause = context.DeadlineExceeded
	}
	return &chatRequestError{
		statusCode: statusCode,
		errorType:  fmt.Sprintf("%T", requestError),
		cause:      cause,
	}
}

func (e *chatRequestError) Error() string {
	if e.statusCode != 0 {
		return fmt.Sprintf(
			"OpenAI-compatible chat failed: status %d, error type %s",
			e.statusCode,
			e.errorType,
		)
	}
	return "OpenAI-compatible chat failed: error type " + e.errorType
}

func (e *chatRequestError) Unwrap() error {
	return e.cause
}

func (c *Client) requestLogger(ctx context.Context, metadata requestmeta.Metadata, requestID, systemPrompt, userMessage string) *slog.Logger {
	return gklog.LoggerFromContext(ctx).With(
		"component", "lm-review",
		"subcomponent", "openai_compat",
		"request_id", requestID,
		"review_id", metadata.ReviewID,
		"scope", metadata.Scope,
		"mode", metadata.Mode,
		"depth", metadata.Depth,
		"chunk_index", metadata.ChunkIndex,
		"chunk_total", metadata.ChunkTotal,
		"base_url", c.baseURL,
		"model", c.model,
		"system_bytes", len(systemPrompt),
		"user_bytes", len(userMessage),
		"max_response_tokens", c.maxTokens,
		"temperature", c.settings.Temperature,
		"top_p", derefFloat64(c.settings.TopP),
		"top_p_set", c.settings.TopP != nil,
		"top_k", derefInt(c.settings.TopK),
		"top_k_set", c.settings.TopK != nil,
		"presence_penalty", derefFloat64(c.settings.PresencePenalty),
		"presence_penalty_set", c.settings.PresencePenalty != nil,
		"frequency_penalty", derefFloat64(c.settings.FrequencyPenalty),
		"frequency_penalty_set", c.settings.FrequencyPenalty != nil,
		"repeat_penalty", derefFloat64(c.settings.RepeatPenalty),
		"repeat_penalty_set", c.settings.RepeatPenalty != nil,
		"seed", derefInt64(c.settings.Seed),
		"seed_set", c.settings.Seed != nil,
		"stop_count", len(c.settings.Stop),
		"timeout_ms", c.requestTimeout.Milliseconds(),
	)
}

func (c *Client) requestOptions(
	metadata requestmeta.Metadata,
	requestID string,
	httpResp **http.Response,
	includeConfiguredGenerationOptions bool,
) []option.RequestOption {
	opts := []option.RequestOption{
		option.WithHeader("X-LM-Review-Request-ID", requestID),
		option.WithRequestTimeout(c.requestTimeout),
		option.WithResponseInto(httpResp),
	}
	if metadata.ReviewID != "" {
		opts = append(opts, option.WithHeader("X-LM-Review-ID", metadata.ReviewID))
	}
	if metadata.ChunkIndex > 0 && metadata.ChunkTotal > 0 {
		opts = append(opts,
			option.WithHeader("X-LM-Review-Chunk-Index", strconv.Itoa(metadata.ChunkIndex)),
			option.WithHeader("X-LM-Review-Chunk-Total", strconv.Itoa(metadata.ChunkTotal)),
		)
	}
	if includeConfiguredGenerationOptions && c.settings.TopK != nil {
		opts = append(opts, option.WithJSONSet("top_k", *c.settings.TopK))
	}
	if includeConfiguredGenerationOptions && c.settings.RepeatPenalty != nil {
		opts = append(opts, option.WithJSONSet("repeat_penalty", *c.settings.RepeatPenalty))
	}
	return opts
}

func (c *Client) chatParams(
	systemPrompt string,
	userMessage string,
	responseFormat *openai.ChatCompletionNewParamsResponseFormatUnion,
	schemaOptions *SchemaGenerationOptions,
) openai.ChatCompletionNewParams {
	params := openai.ChatCompletionNewParams{
		Model: c.model,
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(systemPrompt),
			openai.UserMessage(userMessage),
		},
	}
	if schemaOptions != nil {
		maxCompletionTokens := c.maxTokens
		if schemaOptions.MaxCompletionTokens != nil {
			maxCompletionTokens = *schemaOptions.MaxCompletionTokens
		}
		params.MaxCompletionTokens = param.NewOpt(maxCompletionTokens)
		if schemaOptions.ReasoningEffort != "" {
			params.ReasoningEffort = shared.ReasoningEffort(schemaOptions.ReasoningEffort)
		}
		if schemaOptions.Temperature != nil {
			params.Temperature = param.NewOpt(*schemaOptions.Temperature)
		}
	} else {
		params.Temperature = param.NewOpt(c.settings.Temperature)
		params.MaxTokens = param.NewOpt[int64](c.maxTokens)
	}
	if schemaOptions == nil && c.settings.TopP != nil {
		params.TopP = param.NewOpt(*c.settings.TopP)
	}
	if schemaOptions == nil && c.settings.PresencePenalty != nil {
		params.PresencePenalty = param.NewOpt(*c.settings.PresencePenalty)
	}
	if schemaOptions == nil && c.settings.FrequencyPenalty != nil {
		params.FrequencyPenalty = param.NewOpt(*c.settings.FrequencyPenalty)
	}
	if schemaOptions == nil && c.settings.Seed != nil {
		params.Seed = param.NewOpt(*c.settings.Seed)
	}
	if schemaOptions == nil && len(c.settings.Stop) > 0 {
		params.Stop = openai.ChatCompletionNewParamsStopUnion{
			OfStringArray: append([]string{}, c.settings.Stop...),
		}
	}
	if responseFormat != nil {
		params.ResponseFormat = *responseFormat
	}
	return params
}

func firstHeader(header http.Header, names ...string) string {
	for _, name := range names {
		if value := header.Get(name); value != "" {
			return value
		}
	}
	return ""
}

func responseStatusCode(resp *http.Response) int {
	if resp == nil {
		return 0
	}
	return resp.StatusCode
}

func derefFloat64(value *float64) float64 {
	if value == nil {
		return 0
	}
	return *value
}

func derefInt(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func derefInt64(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func newChatRequestID() string {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err == nil {
		return "lmchat-" + hex.EncodeToString(raw[:])
	}
	return fmt.Sprintf("lmchat-%d", clock.Now().UnixNano())
}

// detectRepetition checks whether the response contains a repeated block of
// text, which indicates the model is stuck in a generation loop. Returns an
// error describing the loop if detected.
func detectRepetition(s string) error {
	if len(s) < repWindowSize*repThreshold {
		return nil
	}
	// Scan from the end: take a window and count how many times it appears.
	tail := s[len(s)-repWindowSize:]
	count := strings.Count(s, tail)
	if count >= repThreshold {
		return fmt.Errorf("repetition loop detected: %d-char block repeated %d times, response likely degenerate", repWindowSize, count)
	}
	return nil
}

// ModelID returns the model identifier this client is configured to use.
func (c *Client) ModelID() string {
	return c.model
}

// Ping checks the server is reachable and the token is valid.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.inner.Models.List(ctx)
	if err != nil {
		return fmt.Errorf("LM Studio unreachable: %w", err)
	}
	return nil
}
