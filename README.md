# lm-review

Local LLM code review using an OpenAI-compatible local backend, defaulting to `lmd`. Runs on `make build`, posts results as living PR comments, and exposes tools to Claude Code via MCP.

## Architecture

```
make build / git commit
       │
       ▼
lm-review CLI ──gRPC──► lm-review daemon ──HTTP──► lmd (localhost:5400)
                              │
Claude Code ──MCP────────────┘
```

The daemon serializes all backend calls and writes a structured audit trail to `~/.local/state/lm-review/audit.jsonl`.

## Prerequisites

- [`lmd`](https://github.com/agoodkind/lmd) running on `http://localhost:5400`, or another OpenAI-compatible backend
- Go 1.26+
- `gh` CLI (for PR comment posting)
- `protoc` + `protoc-gen-go` + `protoc-gen-go-grpc` (for proto regeneration only)

## Install

```bash
go install goodkind.io/lm-review/cmd/lm-review@latest
```

Or from source:

```bash
make deploy
```

## Config

Create `~/.config/lm-review/config.toml`:

```toml
[openai_compat]
url        = "http://localhost:5400"
token      = "sk-lm-your-token-here"
fast_model = "qwen3-coder-30b-a3b-instruct-dwq-lr9e8"
deep_model = "qwen3.5-122b-a10b-text-qx85-mlx"
```

Use the token and model IDs exposed by your chosen backend. The default local target is `lmd` on `http://localhost:5400`.

## Commands

```bash
lm-review diff            # review staged changes (fast model)
lm-review diff --deep     # review staged changes (deep model)
lm-review pr              # review branch vs main
lm-review repo            # full repo health review
lm-review repo --async    # full repo review in background
lm-review daemon          # start daemon manually (auto-started on first call)
lm-review mcp             # start MCP stdio server for Claude Code
```

## Makefile integration

Add to your project's Makefile:

```makefile
build:
	go build ./...
	@which lm-review > /dev/null && lm-review diff || true
```

## MCP (Claude Code)

Add to `~/.claude.json`:

```json
{
  "mcpServers": {
    "lm-review": {
      "type": "stdio",
      "command": "/path/to/lm-review",
      "args": ["mcp"]
    }
  }
}
```

Or add `.mcp.json` to your project root (see `.mcp.json` in this repo).

Tools available in Claude Code:
- `review_diff` - review staged changes
- `review_pr` - review branch vs main
- `review_repo` - full repo health review

## Review output

Each review returns:

| Field | Description |
|-------|-------------|
| `verdict` | `pass` / `warn` / `block` |
| `summary` | One-sentence overview |
| `issues` | Findings with file, line, rule, message, suggestion, confidence |
| `highlights` | Positive findings |
| `tech_debt` | Overall debt assessment |
| `stats` | Error/warning/info counts |

Large repos (>80KB) are reviewed in chunks and merged automatically.

## Audit log

Every review is logged to `~/.local/state/lm-review/audit.jsonl`:

```json
{"ts":"2026-04-11T15:00:00Z","scope":"diff","model":"qwen3-coder-30b...","diff_hash":"a1b2c3d4","latency_ms":2179,"verdict":"warn","issue_count":1}
```

## Token rotation

```bash
lm-review token rotate   # generates new token, updates config (coming soon)
```

For now, generate a new token in LM Studio Developer tab and update `~/.config/lm-review/config.toml`.
