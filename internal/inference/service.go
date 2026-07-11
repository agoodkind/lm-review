// Package inference implements declaration-driven structured inference.
package inference

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"strings"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"goodkind.io/lm-review/api/inferencepb"
	"goodkind.io/lm-review/internal/config"
	"goodkind.io/lm-review/internal/lmstudio"
	"goodkind.io/lm-review/internal/version"
)

const schemaResourceURL = "urn:lm-review:inference-output"

var errInvalidSchema = errors.New("invalid JSON Schema")

// ModelRequest is the payload passed to the OpenAI-compatible model client.
type ModelRequest struct {
	Model             string
	Prompt            string
	Input             string
	Context           string
	OutputSchema      string
	GenerationOptions GenerationOptions
}

// GenerationOptions contains validated caller-declared model controls.
type GenerationOptions struct {
	ReasoningEffort     string
	MaxCompletionTokens *int64
	Temperature         *float64
}

// ModelResult contains structured output and backend invocation metadata.
type ModelResult struct {
	Output             string
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

// ModelClient performs one schema-driven inference call.
type ModelClient interface {
	Infer(context.Context, ModelRequest) (ModelResult, error)
}

// Server implements the generic inference.v1 Inference service.
type Server struct {
	inferencepb.UnimplementedInferenceServer
	defaultModel   string
	modelClient    ModelClient
	serviceVersion string
}

// NewServer creates an inference server backed by modelClient.
func NewServer(defaultModel string, modelClient ModelClient) *Server {
	return &Server{
		UnimplementedInferenceServer: inferencepb.UnimplementedInferenceServer{},
		defaultModel:                 defaultModel,
		modelClient:                  modelClient,
		serviceVersion:               version.Version + "+" + version.Commit,
	}
}

// NewOpenAICompatibleServer creates a server backed by an OpenAI-compatible API.
func NewOpenAICompatibleServer(
	defaultModel string,
	baseURL string,
	token string,
	maxTokens int,
	requestTimeout time.Duration,
	settings config.ChatSettings,
) *Server {
	client := &openAICompatibleClient{
		baseURL:        baseURL,
		token:          token,
		maxTokens:      maxTokens,
		requestTimeout: requestTimeout,
		settings:       settings,
	}
	return NewServer(defaultModel, client)
}

// Infer validates the declaration, invokes the model, and validates its output.
func (s *Server) Infer(ctx context.Context, request *inferencepb.InferRequest) (*inferencepb.InferReply, error) {
	if request == nil || strings.TrimSpace(request.GetPrompt()) == "" {
		return nil, status.Error(codes.InvalidArgument, "prompt is required")
	}
	if strings.TrimSpace(request.GetInput()) == "" {
		return nil, status.Error(codes.InvalidArgument, "input is required")
	}

	schema, err := compileSchema(request.GetOutputSchema())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "output_schema must be a valid JSON Schema")
	}
	if contextValue := request.GetContext(); contextValue != "" && !json.Valid([]byte(contextValue)) {
		return nil, status.Error(codes.InvalidArgument, "context must be valid JSON")
	}
	if s.modelClient == nil {
		return nil, status.Error(codes.FailedPrecondition, "model client is not configured")
	}
	generationOptions, err := generationOptionsFromProto(request.GetGenerationOptions())
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	model := strings.TrimSpace(request.GetModel())
	if model == "" {
		model = strings.TrimSpace(s.defaultModel)
	}
	if model == "" {
		return nil, status.Error(codes.FailedPrecondition, "model is not configured")
	}
	result, err := s.modelClient.Infer(ctx, ModelRequest{
		Model:             model,
		Prompt:            request.GetPrompt(),
		Input:             request.GetInput(),
		Context:           request.GetContext(),
		OutputSchema:      request.GetOutputSchema(),
		GenerationOptions: generationOptions,
	})
	if err != nil {
		return nil, modelStatusError(ctx, err)
	}

	value, err := jsonschema.UnmarshalJSON(bytes.NewReader([]byte(result.Output)))
	if err != nil {
		return nil, status.Error(codes.DataLoss, "model output is not valid JSON")
	}
	if err := schema.Validate(value); err != nil {
		return nil, status.Error(codes.DataLoss, "model output does not match output_schema")
	}
	actualModel := strings.TrimSpace(result.ActualModel)
	if actualModel == "" {
		actualModel = model
	}
	return &inferencepb.InferReply{
		OutputJson: result.Output,
		Status:     inferencepb.InferenceStatus_INFERENCE_STATUS_COMPLETE,
		Metadata: &inferencepb.InvocationMetadata{
			RequestId:          result.RequestID,
			ServiceVersion:     s.serviceVersion,
			RequestedModel:     model,
			ActualModel:        actualModel,
			BackendFingerprint: result.BackendFingerprint,
			BackendVersion:     result.BackendVersion,
			PromptSha256:       sha256Hex(request.GetPrompt()),
			SchemaSha256:       sha256Hex(request.GetOutputSchema()),
			PromptTokens:       result.PromptTokens,
			CompletionTokens:   result.CompletionTokens,
			TotalTokens:        result.TotalTokens,
			FinishReason:       result.FinishReason,
			LatencyMs:          result.Latency.Milliseconds(),
		},
	}, nil
}

func generationOptionsFromProto(options *inferencepb.GenerationOptions) (GenerationOptions, error) {
	if options == nil {
		return emptyGenerationOptions(), nil
	}
	reasoningEffort := ""
	switch options.GetReasoningEffort() {
	case inferencepb.ReasoningEffort_REASONING_EFFORT_UNSPECIFIED:
	case inferencepb.ReasoningEffort_REASONING_EFFORT_NONE:
		reasoningEffort = "none"
	case inferencepb.ReasoningEffort_REASONING_EFFORT_MINIMAL:
		reasoningEffort = "minimal"
	case inferencepb.ReasoningEffort_REASONING_EFFORT_LOW:
		reasoningEffort = "low"
	case inferencepb.ReasoningEffort_REASONING_EFFORT_MEDIUM:
		reasoningEffort = "medium"
	case inferencepb.ReasoningEffort_REASONING_EFFORT_HIGH:
		reasoningEffort = "high"
	case inferencepb.ReasoningEffort_REASONING_EFFORT_XHIGH:
		reasoningEffort = "xhigh"
	default:
		return emptyGenerationOptions(), errors.New("reasoning_effort is invalid")
	}
	if options.MaxCompletionTokens != nil && options.GetMaxCompletionTokens() <= 0 {
		return emptyGenerationOptions(), errors.New("max_completion_tokens must be greater than zero")
	}
	if options.Temperature != nil {
		temperature := options.GetTemperature()
		if math.IsNaN(temperature) || math.IsInf(temperature, 0) || temperature < 0 || temperature > 2 {
			return emptyGenerationOptions(), errors.New("temperature must be between zero and two")
		}
	}
	return GenerationOptions{
		ReasoningEffort:     reasoningEffort,
		MaxCompletionTokens: copyInt64(options.MaxCompletionTokens),
		Temperature:         copyFloat64(options.Temperature),
	}, nil
}

func sha256Hex(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func emptyGenerationOptions() GenerationOptions {
	return GenerationOptions{
		ReasoningEffort:     "",
		MaxCompletionTokens: nil,
		Temperature:         nil,
	}
}

func copyInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func copyFloat64(value *float64) *float64 {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

// Serve starts the inference gRPC service at listenAddress.
func Serve(ctx context.Context, listenAddress string, server *Server) error {
	listenConfig := &net.ListenConfig{}
	listener, err := listenConfig.Listen(ctx, "tcp", listenAddress)
	if err != nil {
		slog.ErrorContext(ctx, "inference.listen.failed", "err", err)
		return fmt.Errorf("listen for inference service: %w", err)
	}
	return ServeListener(ctx, listener, server)
}

// ServeListener serves inference calls until ctx is canceled or serving fails.
func ServeListener(ctx context.Context, listener net.Listener, server *Server) error {
	grpcServer := grpc.NewServer()
	inferencepb.RegisterInferenceServer(grpcServer, server)
	stopped := make(chan struct{})
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.ErrorContext(
					ctx,
					"inference.stop.panic",
					"err",
					errors.New("gRPC graceful stop panicked"),
				)
			}
		}()
		select {
		case <-ctx.Done():
			grpcServer.GracefulStop()
		case <-stopped:
		}
	}()
	err := grpcServer.Serve(listener)
	close(stopped)
	if err != nil && !errors.Is(err, grpc.ErrServerStopped) {
		slog.ErrorContext(ctx, "inference.serve.failed", "err", err)
		return fmt.Errorf("serve inference gRPC: %w", err)
	}
	return nil
}

func compileSchema(raw string) (*jsonschema.Schema, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, errInvalidSchema
	}
	decoded, err := jsonschema.UnmarshalJSON(bytes.NewReader([]byte(raw)))
	if err != nil {
		return nil, errInvalidSchema
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(schemaResourceURL, decoded); err != nil {
		return nil, errInvalidSchema
	}
	schema, err := compiler.Compile(schemaResourceURL)
	if err != nil {
		return nil, errInvalidSchema
	}
	return schema, nil
}

func modelStatusError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
		return status.Error(codes.Canceled, "inference canceled")
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return status.Error(codes.DeadlineExceeded, "inference deadline exceeded")
	}
	return status.Error(codes.Unavailable, "model inference failed")
}

type openAICompatibleClient struct {
	baseURL        string
	token          string
	maxTokens      int
	requestTimeout time.Duration
	settings       config.ChatSettings
}

func (c *openAICompatibleClient) Infer(ctx context.Context, request ModelRequest) (ModelResult, error) {
	client := lmstudio.New(
		c.baseURL,
		c.token,
		request.Model,
		c.maxTokens,
		c.requestTimeout,
		c.settings,
	)
	input := request.Input
	if request.Context != "" {
		input += "\n\nOpaque JSON context:\n" + request.Context
	}
	result, err := client.ChatSchemaDetailed(
		ctx,
		request.Prompt,
		input,
		json.RawMessage(request.OutputSchema),
		lmstudio.SchemaGenerationOptions{
			ReasoningEffort:     request.GenerationOptions.ReasoningEffort,
			MaxCompletionTokens: request.GenerationOptions.MaxCompletionTokens,
			Temperature:         request.GenerationOptions.Temperature,
		},
	)
	if err != nil {
		slog.ErrorContext(
			ctx,
			"inference.model.failed",
			"err",
			errors.New("OpenAI-compatible schema chat failed"),
		)
		return ModelResult{}, fmt.Errorf("schema chat: %w", err)
	}
	return ModelResult{
		Output:             result.Content,
		RequestID:          result.RequestID,
		ActualModel:        result.ActualModel,
		BackendFingerprint: result.BackendFingerprint,
		BackendVersion:     result.BackendVersion,
		PromptTokens:       result.PromptTokens,
		CompletionTokens:   result.CompletionTokens,
		TotalTokens:        result.TotalTokens,
		FinishReason:       result.FinishReason,
		Latency:            result.Latency,
	}, nil
}
