package review

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"
	"goodkind.io/gklog"
	"goodkind.io/lm-review/internal/clock"
	"goodkind.io/lm-review/internal/requestmeta"
)

// ChunkPromptBuilder builds the user message for one chunk.
type ChunkPromptBuilder func(input string, chunkNum, totalChunks int) string

// InputSplitter splits prepared review input into bounded chunks.
type InputSplitter func(input string, maxBytes int) []string

type chunkResult struct {
	index  int
	result *Result
}

// ChunkedReview reviews prepared input by splitting it into chunks,
// reviewing each independently, then merging the results into one Result.
func ChunkedReview(ctx context.Context, client ChatClient, input string, scope string, rules []string, buildSystemPrompt PromptBuilder, buildFullPrompt UserPromptBuilder, buildChunkPrompt ChunkPromptBuilder, splitInput InputSplitter, chunkBytes, parallelism int) (*Result, error) {
	chunks := splitInput(input, chunkBytes)

	if len(chunks) == 1 {
		r := NewWithPromptBuilder(client, scope, rules, buildSystemPrompt)
		return r.ReviewInput(ctx, input, buildFullPrompt)
	}

	if parallelism < 1 {
		parallelism = 1
	}

	var (
		mu      sync.Mutex
		results []chunkResult
	)

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(parallelism)

	for i, chunk := range chunks {
		g.Go(func() error {
			result, err := reviewChunk(gctx, client, chunkReviewRequest{
				chunk:             chunk,
				index:             i,
				total:             len(chunks),
				parallelism:       parallelism,
				rules:             rules,
				buildSystemPrompt: buildSystemPrompt,
				buildChunkPrompt:  buildChunkPrompt,
			})
			if err != nil {
				return err
			}
			if result == nil {
				return nil
			}

			mu.Lock()
			results = append(results, chunkResult{index: i, result: result})
			mu.Unlock()
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	// Sort by chunk index to maintain deterministic ordering.
	sort.Slice(results, func(i, j int) bool {
		return results[i].index < results[j].index
	})

	var allIssues []Issue
	var summaries []string
	var techDebts []string
	var highlights []string
	worstVerdict := VerdictPass

	for _, cr := range results {
		r := cr.result
		allIssues = append(allIssues, r.Issues...)
		summaries = append(summaries, r.Summary)
		if r.TechDebt != "" {
			techDebts = append(techDebts, r.TechDebt)
		}
		highlights = append(highlights, r.Highlights...)
		if verdictWeight(r.Verdict) > verdictWeight(worstVerdict) {
			worstVerdict = r.Verdict
		}
	}

	// Deduplicate issues by (file, line, rule).
	allIssues = deduplicateIssues(allIssues)

	// Sort: errors first, then by file+line.
	sevOrd := map[string]int{"error": 0, "warning": 1, "info": 2}
	sort.Slice(allIssues, func(i, j int) bool {
		if allIssues[i].Severity != allIssues[j].Severity {
			return sevOrd[allIssues[i].Severity] < sevOrd[allIssues[j].Severity]
		}
		if allIssues[i].File != allIssues[j].File {
			return allIssues[i].File < allIssues[j].File
		}
		return allIssues[i].Line < allIssues[j].Line
	})

	merged := &Result{
		Verdict:    worstVerdict,
		Summary:    mergeSummaries(summaries, len(chunks)),
		Issues:     allIssues,
		Highlights: deduplicateStrings(highlights),
		TechDebt:   mergeStrings(techDebts, "\n\n"),
		Scope:      scope,
		Model:      client.ModelID(),
	}
	merged.recalcStats()
	merged.inferVerdict()

	return merged, nil
}

type chunkReviewRequest struct {
	chunk             string
	index             int
	total             int
	parallelism       int
	rules             []string
	buildSystemPrompt PromptBuilder
	buildChunkPrompt  ChunkPromptBuilder
}

func reviewChunk(ctx context.Context, client ChatClient, req chunkReviewRequest) (*Result, error) {
	chunkCtx := requestmeta.With(ctx, requestmeta.Metadata{
		ReviewID:   "",
		Scope:      "",
		Mode:       "",
		Depth:      "",
		ChunkIndex: req.index + 1,
		ChunkTotal: req.total,
	})
	log := gklog.LoggerFromContext(chunkCtx).With(
		"component", "lm-review",
		"subcomponent", "chunked_review",
		"request_id", requestmeta.From(chunkCtx).RequestID(),
		"chunk_index", req.index+1,
		"chunk_total", req.total,
		"chunk_bytes", len(req.chunk),
		"parallelism", req.parallelism,
	)
	start := clock.Now()
	log.DebugContext(chunkCtx, "review.chunk.begin")
	var raw string
	var err error
	if structuredClient, ok := client.(reviewChatClient); ok {
		raw, err = structuredClient.ChatReview(chunkCtx, req.buildSystemPrompt(req.rules), req.buildChunkPrompt(req.chunk, req.index+1, req.total))
	} else {
		raw, err = client.Chat(chunkCtx, req.buildSystemPrompt(req.rules), req.buildChunkPrompt(req.chunk, req.index+1, req.total))
	}
	latency := clock.Since(start)
	if err != nil && raw == "" {
		log.ErrorContext(chunkCtx, "review.chunk.failed", "latency_ms", latency.Milliseconds(), "err", err)
		return nil, fmt.Errorf("chunk %d/%d review: %w", req.index+1, req.total, err)
	}

	result, parseErr := Parse(raw)
	if parseErr != nil {
		log.WarnContext(chunkCtx, "review.chunk.unparseable",
			"latency_ms", latency.Milliseconds(),
			"response_bytes", len(raw),
			"err", parseErr)
		return nil, nil
	}
	log.DebugContext(chunkCtx, "review.chunk.end",
		"latency_ms", latency.Milliseconds(),
		"response_bytes", len(raw),
		"issue_count", len(result.Issues),
		"verdict", result.Verdict)
	return result, nil
}

// SplitRepoInput splits a repo snapshot into chunks of at most maxBytes when
// possible, respecting file boundaries first and falling back to hard splits.
func SplitRepoInput(input string, maxBytes int) []string {
	return splitIntoChunks(input, maxBytes, "// FILE: ")
}

// SplitDiffInput splits a diff into chunks of at most maxBytes when possible,
// respecting file-diff boundaries first and falling back to hard splits.
func SplitDiffInput(input string, maxBytes int) []string {
	return splitIntoChunks(input, maxBytes, "diff --git ")
}

func splitIntoChunks(input string, maxBytes int, marker string) []string {
	if maxBytes <= 0 || len(input) <= maxBytes {
		return []string{input}
	}

	var chunks []string
	var current strings.Builder

	parts := splitOnMarker(input, marker)
	for _, part := range parts {
		if len(part) > maxBytes {
			if current.Len() > 0 {
				chunks = append(chunks, current.String())
				current.Reset()
			}
			chunks = append(chunks, hardSplit(part, maxBytes)...)
			continue
		}
		if current.Len()+len(part) > maxBytes && current.Len() > 0 {
			chunks = append(chunks, current.String())
			current.Reset()
		}
		current.WriteString(part)
	}
	if current.Len() > 0 {
		chunks = append(chunks, current.String())
	}

	return chunks
}

func splitOnMarker(input, marker string) []string {
	var parts []string
	remaining := input
	for {
		idx := indexAfterFirst(remaining, marker, 1)
		if idx < 0 {
			parts = append(parts, remaining)
			break
		}
		parts = append(parts, remaining[:idx])
		remaining = remaining[idx:]
	}
	return parts
}

func hardSplit(input string, maxBytes int) []string {
	var chunks []string
	for len(input) > maxBytes {
		chunks = append(chunks, input[:maxBytes])
		input = input[maxBytes:]
	}
	if input != "" {
		chunks = append(chunks, input)
	}
	return chunks
}

func indexAfterFirst(s, substr string, skip int) int {
	found := 0
	offset := 0
	for {
		idx := indexOf(s[offset:], substr)
		if idx < 0 {
			return -1
		}
		found++
		if found > skip {
			return offset + idx
		}
		offset += idx + len(substr)
	}
}

func indexOf(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

func verdictWeight(v Verdict) int {
	switch v {
	case VerdictBlock:
		return 2
	case VerdictWarn:
		return 1
	default:
		return 0
	}
}

func deduplicateIssues(issues []Issue) []Issue {
	seen := make(map[string]bool)
	var out []Issue
	for _, issue := range issues {
		key := fmt.Sprintf("%s:%d:%s", issue.File, issue.Line, issue.Rule)
		if !seen[key] {
			seen[key] = true
			out = append(out, issue)
		}
	}
	return out
}

func deduplicateStrings(ss []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func mergeSummaries(summaries []string, chunks int) string {
	if len(summaries) == 0 {
		return fmt.Sprintf("Reviewed %d chunks", chunks)
	}
	if len(summaries) == 1 {
		return summaries[0]
	}
	return fmt.Sprintf("%d-chunk review: %s", chunks, summaries[0])
}

func mergeStrings(ss []string, sep string) string {
	var out string
	for i, s := range ss {
		if i > 0 {
			out += sep
		}
		out += s
	}
	return out
}
