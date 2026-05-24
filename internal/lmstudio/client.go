// Package lmstudio wraps the official openai-go/v3 SDK pointed at an OpenAI-compatible endpoint.
package lmstudio

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/packages/param"

	"goodkind.io/gklog"
	"goodkind.io/lm-review/internal/clock"
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
}

// New creates a Client targeting the given OpenAI-compatible base URL with the provided token.
// maxTokens caps the response length to prevent infinite generation loops.
func New(baseURL, token, model string, maxTokens int, requestTimeout time.Duration) *Client {
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

func (c *Client) chat(ctx context.Context, systemPrompt, userMessage string, responseFormat *openai.ChatCompletionNewParamsResponseFormatUnion) (string, error) {
	metadata := requestmeta.From(ctx)
	requestID := metadata.RequestID()
	if requestID == "" {
		requestID = newChatRequestID()
	}

	log := gklog.LoggerFromContext(ctx).With(
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
		"timeout_ms", c.requestTimeout.Milliseconds(),
	)

	var httpResp *http.Response
	opts := []option.RequestOption{
		option.WithHeader("X-LM-Review-Request-ID", requestID),
		option.WithRequestTimeout(c.requestTimeout),
		option.WithResponseInto(&httpResp),
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

	start := clock.Now()
	log.InfoContext(ctx, "openai.chat.begin")
	params := openai.ChatCompletionNewParams{
		Model: c.model,
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(systemPrompt),
			openai.UserMessage(userMessage),
		},
		Temperature: param.NewOpt[float64](0.1),
		MaxTokens:   param.NewOpt[int64](c.maxTokens),
	}
	if responseFormat != nil {
		params.ResponseFormat = *responseFormat
	}
	resp, err := c.inner.Chat.Completions.New(ctx, params, opts...)
	latency := clock.Since(start)
	if err != nil {
		log.ErrorContext(ctx, "openai.chat.failed",
			"latency_ms", latency.Milliseconds(),
			"status_code", responseStatusCode(httpResp),
			"err", err)
		return "", fmt.Errorf("OpenAI-compatible chat: %w", err)
	}
	if len(resp.Choices) == 0 {
		err := fmt.Errorf("OpenAI-compatible backend returned no choices")
		log.ErrorContext(ctx, "openai.chat.no_choices",
			"latency_ms", latency.Milliseconds(),
			"status_code", responseStatusCode(httpResp),
			"err", err)
		return "", err
	}
	content := resp.Choices[0].Message.Content
	if err := detectRepetition(content); err != nil {
		log.ErrorContext(ctx, "openai.chat.repetition_detected",
			"latency_ms", latency.Milliseconds(),
			"status_code", responseStatusCode(httpResp),
			"response_bytes", len(content),
			"err", err)
		return content, err
	}
	log.InfoContext(ctx, "openai.chat.end",
		"latency_ms", latency.Milliseconds(),
		"status_code", responseStatusCode(httpResp),
		"choice_count", len(resp.Choices),
		"response_bytes", len(content))
	return content, nil
}

func responseStatusCode(resp *http.Response) int {
	if resp == nil {
		return 0
	}
	return resp.StatusCode
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
