package review

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"golang.org/x/sync/errgroup"
)

// ChunkedRepoReview reviews a large codebase by splitting it into chunks,
// reviewing each independently, then merging the results into one Result.
// chunkBytes controls the max bytes per chunk sent to the LLM.
// parallelism controls how many chunks are reviewed concurrently (1 = sequential).
func ChunkedRepoReview(ctx context.Context, client ChatClient, files string, scope string, rules []string, chunkBytes, parallelism int) (*Result, error) {
	chunks := splitIntoChunks(files, chunkBytes)

	if len(chunks) == 1 {
		r := New(client, scope, rules)
		return r.ReviewRepo(ctx, files)
	}

	if parallelism < 1 {
		parallelism = 1
	}

	type chunkResult struct {
		index  int
		result *Result
	}

	var (
		mu      sync.Mutex
		results []chunkResult
	)

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(parallelism)

	for i, chunk := range chunks {
		g.Go(func() error {
			raw, err := client.Chat(gctx, BuildSystemPrompt(rules), ChunkPrompt(chunk, i+1, len(chunks)))
			if err != nil && raw == "" {
				return fmt.Errorf("chunk %d/%d review: %w", i+1, len(chunks), err)
			}

			result, parseErr := Parse(raw)
			if parseErr != nil {
				return nil // soft failure: skip bad chunks
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

// splitIntoChunks splits a files string into chunks of at most maxBytes,
// respecting file boundaries (never splits mid-file).
func splitIntoChunks(files string, maxBytes int) []string {
	if len(files) <= maxBytes {
		return []string{files}
	}

	var chunks []string
	var current string

	// Split on file boundaries: each file starts with "// FILE: "
	parts := splitOnFileMarker(files)
	for _, part := range parts {
		if len(current)+len(part) > maxBytes && current != "" {
			chunks = append(chunks, current)
			current = part
		} else {
			current += part
		}
	}
	if current != "" {
		chunks = append(chunks, current)
	}

	return chunks
}

func splitOnFileMarker(files string) []string {
	const marker = "// FILE: "
	var parts []string
	remaining := files
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
