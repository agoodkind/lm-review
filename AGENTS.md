# AGENTS.md

Guidance for AI agents working in this repository.

## What this repo does

`lm-review` is a local LLM code review tool. It runs LM Studio headlessly, exposes a gRPC daemon, and provides MCP tools for Claude Code. The critical paths are:

1. **MCP tools** (`internal/mcpserver/`) - called by Claude Code. Must be fast to start, graceful on error.
2. **gRPC daemon** (`internal/daemon/`) - long-lived process. Owns LM Studio lifecycle and audit log.
3. **Review logic** (`internal/review/`) - prompt construction, JSON parsing, result formatting.
4. **LM Studio client** (`internal/lmstudio/`) - HTTP API calls and `lms` CLI management.
5. **Inference service** (`internal/inference/`) - declaration-driven structured inference over gRPC.

## Key packages

- `internal/review/` - `Result`, `Parse()`, `Reviewer`, and chunked reviews. This is the core review type system.
- `internal/lmstudio/` - HTTP client plus `lms` CLI lifecycle management.
- `internal/daemon/` - gRPC server, serialized review execution, and audit logging.
- `internal/mcpserver/` - MCP stdio server using `mark3labs/mcp-go`.
- `internal/xdg/` - XDG path helpers.
- `internal/audit/` - JSONL audit log at `~/.local/state/lm-review/audit.jsonl`.
- `internal/inference/` - generic `Inference` gRPC service and caller-supplied JSON Schema validation. See `docs/inference/overview.md`.
- `api/review.proto` - gRPC service definition.
- `api/inferencepb/inference.proto` - `Inference` gRPC service definition.

## Build and test

```bash
make build    # build binary
make test     # run tests
make deploy   # install to $GOPATH/bin
make check    # full validation suite
```

Never run `go build` or `go test` directly. Always use `make`.

## Key rules

- All config is TOML. Never JSON for user-facing config.
- Config lives at `~/.config/lm-review/config.toml`.
- Use `slog` for logging. Never `fmt.Fprintf(os.Stderr)` for diagnostics.
- XDG paths only. See `internal/xdg/xdg.go`.
- The daemon auto-starts on first `daemon.Connect()` call. Tests should kill and clean the socket.
- `result.go` is the canonical review output type. The proto `ReviewResponse` is only for gRPC transport - convert at the daemon boundary.
- MCP tool handlers must return friendly text on error, never hard error results.
- Inference prompts, inputs, contexts, schemas, outputs, tokens, and backend response bodies must never appear in logs or errors.

## MCP tools

The MCP server (`lm-review mcp`) is a stdio process started by Claude Code. It must:

- Start cleanly with no side effects
- Return friendly text (not errors) when git repo or daemon is unavailable
- Auto-detect git root via `git rev-parse --show-toplevel`

## Daemon

The daemon auto-starts on first `daemon.Connect()` call. To restart it after a binary update:

```bash
pkill -f "lm-review daemon" && rm -f $TMPDIR/lm-review/daemon.sock
```

## Proto

If you change `api/review.proto` or `api/inferencepb/inference.proto`, regenerate both with `make proto`. That target runs `protoc` for `reviewpb` and `inferencepb` together, so run it rather than a single hand-written `protoc` invocation.

## LM Studio token

The API token is stored in `~/.config/lm-review/config.toml` (0600). Never log or expose it. The token format is `sk-lm-{id}:{passkey}` and is validated against a SHA512 hash stored in `~/.lmstudio/.internal/permissions-store.json`.

## Large codebase reviews

Repos over 80KB of Go source are automatically split into chunks and reviewed in parallel, then merged. See `internal/review/chunked.go`.
