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
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"

	"goodkind.io/lm-review/api/inferencepb"
	"goodkind.io/lm-review/internal/config"
	"goodkind.io/lm-review/internal/lmstudio"
	"goodkind.io/lm-review/internal/version"
)

const (
	schemaResourceURL               = "urn:lm-review:inference-output"
	bareEnumObjectNormalizationKind = "bare_enum_object"
)

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
	Output                  string
	RequestID               string
	UpstreamResponseID      string
	ActualModel             string
	BackendFingerprint      string
	BackendVersion          string
	PromptTokens            int64
	PromptTokensPresent     bool
	CompletionTokens        int64
	CompletionTokensPresent bool
	TotalTokens             int64
	TotalTokensPresent      bool
	FinishReason            string
	Latency                 time.Duration
	// Confidence is a token-logprob-derived confidence in [0, 1], present only
	// when the backend returned per-token logprobs.
	Confidence        float64
	ConfidencePresent bool
}

// ModelClient performs one schema-driven inference call.
type ModelClient interface {
	Infer(context.Context, ModelRequest) (ModelResult, error)
}

// Server implements the generic inference.v1 Inference service.
//
// A server routes each request to a backend by model. With a single backend the
// backends map is nil and every request goes to modelClient. With per-model
// backends the backends map selects the client, and a request for a model with
// no backend fails with FailedPrecondition.
type Server struct {
	inferencepb.UnimplementedInferenceServer
	defaultModel   string
	modelClient    ModelClient
	backends       map[string]ModelClient
	serviceVersion string
}

// NewServer creates an inference server backed by a single modelClient for
// every request.
func NewServer(defaultModel string, modelClient ModelClient) *Server {
	return &Server{
		UnimplementedInferenceServer: inferencepb.UnimplementedInferenceServer{},
		defaultModel:                 defaultModel,
		modelClient:                  modelClient,
		backends:                     nil,
		serviceVersion:               version.Version + "+" + version.Commit,
	}
}

// NewRoutingServer creates an inference server that selects a backend by the
// request's model. A request for a model absent from backends fails with
// FailedPrecondition.
func NewRoutingServer(defaultModel string, backends map[string]ModelClient) *Server {
	return &Server{
		UnimplementedInferenceServer: inferencepb.UnimplementedInferenceServer{},
		defaultModel:                 defaultModel,
		modelClient:                  nil,
		backends:                     backends,
		serviceVersion:               version.Version + "+" + version.Commit,
	}
}

// NewOpenAICompatibleClient builds a model client for one OpenAI-compatible
// backend. Use it to assemble the per-model backends passed to NewRoutingServer.
func NewOpenAICompatibleClient(
	baseURL string,
	token string,
	maxTokens int,
	requestTimeout time.Duration,
	settings config.ChatSettings,
) ModelClient {
	return &openAICompatibleClient{
		baseURL:        baseURL,
		token:          token,
		maxTokens:      maxTokens,
		requestTimeout: requestTimeout,
		settings:       settings,
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
	client := NewOpenAICompatibleClient(baseURL, token, maxTokens, requestTimeout, settings)
	return NewServer(defaultModel, client)
}

// clientForModel returns the backend client for model. With per-model backends
// an unmapped model is a FailedPrecondition; with a single backend the model
// client is used when configured.
func (s *Server) clientForModel(model string) (ModelClient, error) {
	if len(s.backends) > 0 {
		client, ok := s.backends[model]
		if !ok {
			return nil, status.Error(
				codes.FailedPrecondition,
				fmt.Sprintf("model %q has no configured backend", model),
			)
		}
		return client, nil
	}
	if s.modelClient == nil {
		return nil, status.Error(codes.FailedPrecondition, "model client is not configured")
	}
	return s.modelClient, nil
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
	modelClient, err := s.clientForModel(model)
	if err != nil {
		return nil, err
	}
	result, err := modelClient.Infer(ctx, ModelRequest{
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

	outputJSON := result.Output
	outputNormalized := false
	normalizationKind := ""
	value, parseErr := jsonschema.UnmarshalJSON(bytes.NewReader([]byte(outputJSON)))
	validationErr := error(nil)
	if parseErr == nil {
		validationErr = schema.Validate(value)
	}
	if parseErr != nil || validationErr != nil {
		normalizedOutput, normalized := normalizeBareEnumObjectOutput(
			request.GetOutputSchema(), result.Output,
		)
		if !normalized && parseErr != nil {
			return nil, status.Error(codes.DataLoss, "model output is not valid JSON")
		}
		if !normalized {
			return nil, status.Error(codes.DataLoss, "model output does not match output_schema")
		}
		outputJSON = normalizedOutput
		outputNormalized = true
		normalizationKind = bareEnumObjectNormalizationKind
		value, err = jsonschema.UnmarshalJSON(bytes.NewReader([]byte(outputJSON)))
		if err != nil {
			return nil, status.Error(codes.DataLoss, "model output is not valid JSON")
		}
		if err := schema.Validate(value); err != nil {
			return nil, status.Error(codes.DataLoss, "model output does not match output_schema")
		}
	}
	return &inferencepb.InferReply{
		OutputJson: outputJSON,
		Status:     inferencepb.InferenceStatus_INFERENCE_STATUS_COMPLETE,
		Metadata: &inferencepb.InvocationMetadata{
			RequestId:          result.RequestID,
			UpstreamResponseId: result.UpstreamResponseID,
			ServiceVersion:     s.serviceVersion,
			RequestedModel:     model,
			ActualModel:        strings.TrimSpace(result.ActualModel),
			BackendFingerprint: result.BackendFingerprint,
			BackendVersion:     result.BackendVersion,
			PromptSha256:       sha256Hex(request.GetPrompt()),
			SchemaSha256:       sha256Hex(request.GetOutputSchema()),
			PromptTokens:       optionalInt64(result.PromptTokens, result.PromptTokensPresent),
			CompletionTokens: optionalInt64(
				result.CompletionTokens,
				result.CompletionTokensPresent,
			),
			TotalTokens:       optionalInt64(result.TotalTokens, result.TotalTokensPresent),
			FinishReason:      result.FinishReason,
			LatencyMs:         result.Latency.Milliseconds(),
			OutputNormalized:  outputNormalized,
			NormalizationKind: normalizationKind,
			RawOutputSha256:   sha256Hex(result.Output),
			Confidence:        optionalFloat64(result.Confidence, result.ConfidencePresent),
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

func optionalInt64(value int64, present bool) *int64 {
	if !present {
		return nil
	}
	return &value
}

func optionalFloat64(value float64, present bool) *float64 {
	if !present {
		return nil
	}
	return &value
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
	healthServer := health.NewServer()
	healthServer.SetServingStatus(
		inferencepb.Inference_ServiceDesc.ServiceName,
		healthpb.HealthCheckResponse_SERVING,
	)
	healthpb.RegisterHealthServer(grpcServer, healthServer)
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
		Output:                  result.Content,
		RequestID:               result.RequestID,
		UpstreamResponseID:      result.UpstreamResponseID,
		ActualModel:             result.ActualModel,
		BackendFingerprint:      result.BackendFingerprint,
		BackendVersion:          result.BackendVersion,
		PromptTokens:            result.PromptTokens,
		PromptTokensPresent:     result.PromptTokensPresent,
		CompletionTokens:        result.CompletionTokens,
		CompletionTokensPresent: result.CompletionTokensPresent,
		TotalTokens:             result.TotalTokens,
		TotalTokensPresent:      result.TotalTokensPresent,
		FinishReason:            result.FinishReason,
		Latency:                 result.Latency,
		Confidence:              result.Confidence,
		ConfidencePresent:       result.ConfidencePresent,
	}, nil
}
