package inferencepb

import (
	"testing"

	"google.golang.org/protobuf/reflect/protoreflect"
)

func TestInferenceWireContract(t *testing.T) {
	requestFields := (&InferRequest{}).ProtoReflect().Descriptor().Fields()
	wantRequestFields := map[string]int32{
		"prompt":        1,
		"input":         2,
		"output_schema": 3,
		"context":       4,
		"model":         5,
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
	if Inference_Infer_FullMethodName != "/inference.v1.Inference/Infer" {
		t.Fatalf("method name=%q", Inference_Infer_FullMethodName)
	}
}
