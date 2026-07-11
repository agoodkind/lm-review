package inference

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"goodkind.io/lm-review/api/inferencepb"
)

const (
	decisionSchema = `{"type":"object","additionalProperties":false,"properties":{"decision":{"type":"string","enum":["block","allow"]}},"required":["decision"]}`
	dogSchema      = `{"type":"object","additionalProperties":false,"properties":{"breed":{"type":"string"},"traits":{"type":"array","items":{"type":"string","enum":["friendly","active"]},"minItems":1}},"required":["breed","traits"]}`
)

type modelFunc func(context.Context, ModelRequest) (string, error)

func (f modelFunc) Infer(ctx context.Context, call ModelRequest) (string, error) {
	return f(ctx, call)
}

func TestInferSupportsUnrelatedCallerSchemas(t *testing.T) {
	server := NewServer("fallback-model", modelFunc(func(_ context.Context, call ModelRequest) (string, error) {
		if strings.Contains(call.OutputSchema, `"decision"`) {
			return `{"decision":"allow"}`, nil
		}
		return `{"breed":"Labrador","traits":["friendly"]}`, nil
	}))

	tests := []struct {
		name       string
		schema     string
		wantOutput string
	}{
		{name: "decision", schema: decisionSchema, wantOutput: `{"decision":"allow"}`},
		{name: "dog breed", schema: dogSchema, wantOutput: `{"breed":"Labrador","traits":["friendly"]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reply, err := server.Infer(context.Background(), validRequest(test.schema))
			if err != nil {
				t.Fatalf("Infer returned error: %v", err)
			}
			if reply.GetOutputJson() != test.wantOutput {
				t.Fatalf("output_json=%q, want %q", reply.GetOutputJson(), test.wantOutput)
			}
			if reply.GetStatus() != inferencepb.InferenceStatus_INFERENCE_STATUS_COMPLETE {
				t.Fatalf("status=%s, want complete", reply.GetStatus())
			}
		})
	}
}

func TestInferRejectsInvalidRequestsBeforeModelInvocation(t *testing.T) {
	var calls atomic.Int32
	server := NewServer("fallback-model", modelFunc(func(context.Context, ModelRequest) (string, error) {
		calls.Add(1)
		return `{}`, nil
	}))

	tests := []struct {
		name    string
		request *inferencepb.InferRequest
	}{
		{name: "empty prompt", request: &inferencepb.InferRequest{Input: "input", OutputSchema: dogSchema}},
		{name: "empty input", request: &inferencepb.InferRequest{Prompt: "prompt", OutputSchema: dogSchema}},
		{name: "malformed schema", request: validRequest(`{"type":`)},
		{name: "invalid schema", request: validRequest(`{"type":"not-a-type"}`)},
		{name: "malformed context", request: withContext(validRequest(dogSchema), `{"secret":`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := server.Infer(context.Background(), test.request)
			if status.Code(err) != codes.InvalidArgument {
				t.Fatalf("code=%s, want invalid argument", status.Code(err))
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("model calls=%d, want 0", calls.Load())
	}
}

func TestInferRejectsInvalidModelOutput(t *testing.T) {
	tests := []struct {
		name   string
		schema string
		output string
	}{
		{name: "malformed JSON", schema: dogSchema, output: `{"breed":`},
		{name: "missing required", schema: dogSchema, output: `{"breed":"Poodle"}`},
		{name: "enum", schema: decisionSchema, output: `{"decision":"maybe"}`},
		{name: "type", schema: dogSchema, output: `{"breed":7,"traits":["friendly"]}`},
		{name: "additional properties", schema: decisionSchema, output: `{"decision":"allow","reason":"extra"}`},
		{name: "nested constraint", schema: dogSchema, output: `{"breed":"Poodle","traits":["quiet"]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := NewServer("fallback-model", modelFunc(func(context.Context, ModelRequest) (string, error) {
				return test.output, nil
			}))
			_, err := server.Infer(context.Background(), validRequest(test.schema))
			if status.Code(err) != codes.DataLoss {
				t.Fatalf("code=%s, want data loss", status.Code(err))
			}
		})
	}
}

func TestInferSelectsRequestModelOrFallback(t *testing.T) {
	var models []string
	server := NewServer("fallback-model", modelFunc(func(_ context.Context, call ModelRequest) (string, error) {
		models = append(models, call.Model)
		return `{"decision":"allow"}`, nil
	}))

	override := validRequest(decisionSchema)
	override.Model = "request-model"
	if _, err := server.Infer(context.Background(), override); err != nil {
		t.Fatalf("override Infer returned error: %v", err)
	}
	if _, err := server.Infer(context.Background(), validRequest(decisionSchema)); err != nil {
		t.Fatalf("fallback Infer returned error: %v", err)
	}
	if got := strings.Join(models, ","); got != "request-model,fallback-model" {
		t.Fatalf("models=%q, want request-model,fallback-model", got)
	}
}

func TestInferPassesPromptInputContextAndSchema(t *testing.T) {
	var captured ModelRequest
	server := NewServer("fallback-model", modelFunc(func(_ context.Context, call ModelRequest) (string, error) {
		captured = call
		return `{"decision":"allow"}`, nil
	}))
	request := validRequest(decisionSchema)
	request.Context = `{"opaque":{"value":42}}`

	if _, err := server.Infer(context.Background(), request); err != nil {
		t.Fatalf("Infer returned error: %v", err)
	}
	if captured.Prompt != request.Prompt || captured.Input != request.Input || captured.Context != request.Context {
		t.Fatalf("captured call does not preserve prompt, input, and context")
	}
	if captured.OutputSchema != request.OutputSchema {
		t.Fatal("captured schema differs from request schema")
	}
}

func TestInferMapsCancellationDeadlineAndModelFailure(t *testing.T) {
	tests := []struct {
		name    string
		call    func(context.Context, ModelRequest) (string, error)
		context func() context.Context
		code    codes.Code
	}{
		{name: "canceled", call: func(ctx context.Context, _ ModelRequest) (string, error) { return "", ctx.Err() }, context: canceledContext, code: codes.Canceled},
		{name: "deadline", call: func(context.Context, ModelRequest) (string, error) { return "", context.DeadlineExceeded }, context: context.Background, code: codes.DeadlineExceeded},
		{name: "model failure", call: func(context.Context, ModelRequest) (string, error) { return "", errors.New("backend failed") }, context: context.Background, code: codes.Unavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := NewServer("fallback-model", modelFunc(test.call))
			_, err := server.Infer(test.context(), validRequest(decisionSchema))
			if status.Code(err) != test.code {
				t.Fatalf("code=%s, want %s", status.Code(err), test.code)
			}
		})
	}
}

func TestInferConcurrentCallsAreSafe(t *testing.T) {
	server := NewServer("fallback-model", modelFunc(func(context.Context, ModelRequest) (string, error) {
		return `{"decision":"allow"}`, nil
	}))
	var waitGroup sync.WaitGroup
	for range 32 {
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			if _, err := server.Infer(context.Background(), validRequest(decisionSchema)); err != nil {
				t.Errorf("Infer returned error: %v", err)
			}
		}()
	}
	waitGroup.Wait()
}

func TestServeListenerRemainsAvailableForMultipleCalls(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	serveDone := make(chan error, 1)
	server := NewServer("fallback-model", modelFunc(func(context.Context, ModelRequest) (string, error) {
		return `{"decision":"allow"}`, nil
	}))
	go func() {
		serveDone <- ServeListener(ctx, listener, server)
	}()

	connection, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		cancel()
		t.Fatalf("dial: %v", err)
	}
	defer connection.Close()
	client := inferencepb.NewInferenceClient(connection)
	for range 2 {
		callCtx, callCancel := context.WithTimeout(context.Background(), time.Second)
		_, callErr := client.Infer(callCtx, validRequest(decisionSchema))
		callCancel()
		if callErr != nil {
			cancel()
			t.Fatalf("Infer RPC returned error: %v", callErr)
		}
	}
	cancel()
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("ServeListener returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ServeListener did not stop after cancellation")
	}
}

func TestInferErrorsDoNotEchoSensitivePayloads(t *testing.T) {
	const sensitive = "sensitive-canary-value"
	server := NewServer("fallback-model", modelFunc(func(context.Context, ModelRequest) (string, error) {
		return "", errors.New(sensitive)
	}))
	request := validRequest(decisionSchema)
	request.Prompt = sensitive
	request.Input = sensitive
	request.Context = `{"secret":"` + sensitive + `"}`

	_, err := server.Infer(context.Background(), request)
	if strings.Contains(err.Error(), sensitive) {
		t.Fatalf("error echoes sensitive payload: %v", err)
	}
}

func validRequest(schema string) *inferencepb.InferRequest {
	return &inferencepb.InferRequest{Prompt: "Classify the input", Input: "sample", OutputSchema: schema}
}

func withContext(request *inferencepb.InferRequest, value string) *inferencepb.InferRequest {
	request.Context = value
	return request
}

func canceledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}
