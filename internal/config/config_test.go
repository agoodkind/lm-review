package config

import (
	"testing"
	"time"
)

func TestResolveReviewChunkBytesCapsLargeContext(t *testing.T) {
	cfg := OpenAICompat{ContextLength: 262144}

	got := cfg.ResolveReviewChunkBytes()

	if got != maxReviewChunkBytes {
		t.Fatalf("chunk bytes=%d, want %d", got, maxReviewChunkBytes)
	}
}

func TestResolveReviewChunkBytesKeepsSmallContext(t *testing.T) {
	cfg := OpenAICompat{ContextLength: 8192}

	got := cfg.ResolveReviewChunkBytes()
	want := cfg.ResolveRepoMaxBytes()

	if got != want {
		t.Fatalf("chunk bytes=%d, want %d", got, want)
	}
}

func TestResolveRequestTimeout(t *testing.T) {
	if got := (OpenAICompat{}).ResolveRequestTimeout(); got != 5*time.Minute {
		t.Fatalf("default timeout=%s, want 5m0s", got)
	}

	cfg := OpenAICompat{RequestTimeoutSec: 12}
	if got := cfg.ResolveRequestTimeout(); got != 12*time.Second {
		t.Fatalf("configured timeout=%s, want 12s", got)
	}
}
