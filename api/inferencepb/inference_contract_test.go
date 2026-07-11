package inferencepb

import (
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestInferenceWireContract(t *testing.T) {
	requestFields := (&InferRequest{}).ProtoReflect().Descriptor().Fields()
	wantRequestFields := map[string]int32{
		"prompt":             1,
		"input":              2,
		"output_schema":      3,
		"context":            4,
		"model":              5,
		"generation_options": 6,
	}
	for name, number := range wantRequestFields {
		field := requestFields.ByName(protoreflect.Name(name))
		if field == nil || int32(field.Number()) != number {
			t.Fatalf("request field %q number=%v, want %d", name, field, number)
		}
	}

	replyFields := (&InferReply{}).ProtoReflect().Descriptor().Fields()
	if got := int32(replyFields.ByName("output_json").Number()); got != 1 {
		t.Fatalf("output_json field number=%d, want 1", got)
	}
	if got := int32(replyFields.ByName("status").Number()); got != 2 {
		t.Fatalf("status field number=%d, want 2", got)
	}
	if got := int32(replyFields.ByName("metadata").Number()); got != 3 {
		t.Fatalf("metadata field number=%d, want 3", got)
	}

	optionsFields := File_inference_proto.Messages().ByName("GenerationOptions").Fields()
	wantOptionFields := map[string]int32{
		"reasoning_effort":      1,
		"max_completion_tokens": 2,
		"temperature":           3,
	}
	for name, number := range wantOptionFields {
		field := optionsFields.ByName(protoreflect.Name(name))
		if field == nil || int32(field.Number()) != number {
			t.Fatalf("generation option field %q number=%v, want %d", name, field, number)
		}
	}

	metadataFields := File_inference_proto.Messages().ByName("InvocationMetadata").Fields()
	wantMetadataFields := map[string]int32{
		"request_id":          1,
		"service_version":     2,
		"requested_model":     3,
		"actual_model":        4,
		"backend_fingerprint": 5,
		"backend_version":     6,
		"prompt_sha256":       7,
		"schema_sha256":       8,
		"prompt_tokens":       9,
		"completion_tokens":   10,
		"total_tokens":        11,
		"finish_reason":       12,
		"latency_ms":          13,
	}
	for name, number := range wantMetadataFields {
		field := metadataFields.ByName(protoreflect.Name(name))
		if field == nil || int32(field.Number()) != number {
			t.Fatalf("metadata field %q number=%v, want %d", name, field, number)
		}
	}
	if Inference_Infer_FullMethodName != "/inference.v1.Inference/Infer" {
		t.Fatalf("method name=%q", Inference_Infer_FullMethodName)
	}
}
