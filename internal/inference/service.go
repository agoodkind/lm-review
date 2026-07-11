// Package inference implements declaration-driven structured inference.
package inference

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
)

const schemaResourceURL = "urn:lm-review:inference-output"

var errInvalidSchema = errors.New("invalid JSON Schema")

// ModelRequest is the payload passed to the OpenAI-compatible model client.
type ModelRequest struct {
	Model        string
	Prompt       string
	Input        string
	Context      string
	OutputSchema string
}

// ModelClient performs one schema-driven inference call.
type ModelClient interface {
	Infer(context.Context, ModelRequest) (string, error)
}

// Server implements the generic inference.v1 Inference service.
type Server struct {
	inferencepb.UnimplementedInferenceServer
	defaultModel string
	modelClient  ModelClient
}

// NewServer creates an inference server backed by modelClient.
func NewServer(defaultModel string, modelClient ModelClient) *Server {
	return &Server{
		UnimplementedInferenceServer: inferencepb.UnimplementedInferenceServer{},
		defaultModel:                 defaultModel,
		modelClient:                  modelClient,
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

	model := strings.TrimSpace(request.GetModel())
	if model == "" {
		model = s.defaultModel
	}
	output, err := s.modelClient.Infer(ctx, ModelRequest{
		Model:        model,
		Prompt:       request.GetPrompt(),
		Input:        request.GetInput(),
		Context:      request.GetContext(),
		OutputSchema: request.GetOutputSchema(),
	})
	if err != nil {
		return nil, modelStatusError(ctx, err)
	}

	value, err := jsonschema.UnmarshalJSON(bytes.NewReader([]byte(output)))
	if err != nil {
		return nil, status.Error(codes.DataLoss, "model output is not valid JSON")
	}
	if err := schema.Validate(value); err != nil {
		return nil, status.Error(codes.DataLoss, "model output does not match output_schema")
	}
	return &inferencepb.InferReply{
		OutputJson: output,
		Status:     inferencepb.InferenceStatus_INFERENCE_STATUS_COMPLETE,
	}, nil
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

func (c *openAICompatibleClient) Infer(ctx context.Context, request ModelRequest) (string, error) {
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
	output, err := client.ChatSchema(ctx, request.Prompt, input, json.RawMessage(request.OutputSchema))
	if err != nil {
		slog.ErrorContext(
			ctx,
			"inference.model.failed",
			"err",
			errors.New("OpenAI-compatible schema chat failed"),
		)
		return "", fmt.Errorf("schema chat: %w", err)
	}
	return output, nil
}
