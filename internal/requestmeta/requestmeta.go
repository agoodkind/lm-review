// Package requestmeta carries non-sensitive review request metadata through
// context so provider clients can log and forward stable correlation IDs.
package requestmeta

import (
	"context"
	"fmt"
)

type contextKey struct{}

// Metadata describes one review-level or chunk-level chat request.
type Metadata struct {
	ReviewID   string
	Scope      string
	Mode       string
	Depth      string
	ChunkIndex int
	ChunkTotal int
}

// With returns a context carrying metadata merged with any existing metadata.
func With(ctx context.Context, metadata Metadata) context.Context {
	current := From(ctx)
	if metadata.ReviewID == "" {
		metadata.ReviewID = current.ReviewID
	}
	if metadata.Scope == "" {
		metadata.Scope = current.Scope
	}
	if metadata.Mode == "" {
		metadata.Mode = current.Mode
	}
	if metadata.Depth == "" {
		metadata.Depth = current.Depth
	}
	if metadata.ChunkIndex == 0 {
		metadata.ChunkIndex = current.ChunkIndex
	}
	if metadata.ChunkTotal == 0 {
		metadata.ChunkTotal = current.ChunkTotal
	}
	return context.WithValue(ctx, contextKey{}, metadata)
}

// From returns metadata from ctx, if present.
func From(ctx context.Context) Metadata {
	metadata, _ := ctx.Value(contextKey{}).(Metadata)
	return metadata
}

// RequestID returns a stable identifier for the provider call.
func (m Metadata) RequestID() string {
	if m.ReviewID == "" {
		return ""
	}
	if m.ChunkIndex > 0 && m.ChunkTotal > 0 {
		return fmt.Sprintf("%s-chunk-%d-of-%d", m.ReviewID, m.ChunkIndex, m.ChunkTotal)
	}
	return m.ReviewID + "-single"
}
