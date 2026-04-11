package review

import (
	"context"
	"fmt"
	"sort"

	"github.com/openai/openai-go/v3"

	"goodkind.io/lm-review/internal/lmstudio"
)

const (
	// chunkBytes is the max bytes per chunk sent to the LLM.
	// ~80KB fits comfortably in a 32K context window with the system prompt.
	chunkBytes = 80_000

	// largeRepoBytes triggers chunked mode instead of single-shot.
	largeRepoBytes = chunkBytes
)

// ChunkedRepoReview reviews a large codebase by splitting it into chunks,
// reviewing each independently, then merging the results into one Result.
func ChunkedRepoReview(ctx context.Context, client *lmstudio.Client, files string, scope string) (*Result, error) {
	chunks := splitIntoChunks(files, chunkBytes)

	if len(chunks) == 1 {
		r := New(client, scope)
		return r.ReviewRepo(ctx, files)
	}

	// Review each chunk independently.
	var allIssues []Issue
	var summaries []string
	var techDebts []string
	var highlights []string
	worstVerdict := VerdictPass

	for i, chunk := range chunks {
		raw, err := client.Chat(ctx, []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(systemPrompt),
			openai.UserMessage(ChunkPrompt(chunk, i+1, len(chunks))),
		})
		if err != nil {
			return nil, fmt.Errorf("chunk %d/%d review: %w", i+1, len(chunks), err)
		}

		result, err := Parse(raw)
		if err != nil {
			// Soft failure: skip bad chunks, continue.
			continue
		}

		allIssues = append(allIssues, result.Issues...)
		summaries = append(summaries, result.Summary)
		if result.TechDebt != "" {
			techDebts = append(techDebts, result.TechDebt)
		}
		highlights = append(highlights, result.Highlights...)

		if verdictWeight(result.Verdict) > verdictWeight(worstVerdict) {
			worstVerdict = result.Verdict
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
