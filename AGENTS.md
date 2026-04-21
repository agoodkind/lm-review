# AGENTS.md

Guidance for AI agents working in this repository.

## What this repo does

`lm-review` is a local LLM code review tool. It runs LM Studio headlessly, exposes a gRPC daemon, and provides MCP tools for Claude Code. The critical paths are:

1. **MCP tools** (`internal/mcpserver/`) - called by Claude Code. Must be fast to start, graceful on error.
2. **gRPC daemon** (`internal/daemon/`) - long-lived process. Owns LM Studio lifecycle and audit log.
3. **Review logic** (`internal/review/`) - prompt construction, JSON parsing, result formatting.
4. **LM Studio client** (`internal/lmstudio/`) - HTTP API calls and `lms` CLI management.

## Build and test

```bash
make build    # build binary
make test     # run tests
make deploy   # install to $GOPATH/bin
```

Never run `go build` or `go test` directly. Always use `make`.

## Key rules

- All config is TOML. Never JSON for user-facing config.
- Use `slog` for logging. Never `fmt.Fprintf(os.Stderr)` for diagnostics.
- XDG paths only. See `internal/xdg/xdg.go`.
- The daemon auto-starts on first `daemon.Connect()` call. Tests should kill and clean the socket.
- `result.go` is the canonical review output type. The proto `ReviewResponse` is only for gRPC transport - convert at the daemon boundary.

## MCP tools

The MCP server (`lm-review mcp`) is a stdio process started by Claude Code. It must:

- Start cleanly with no side effects
- Return friendly text (not errors) when git repo or daemon is unavailable
- Auto-detect git root via `git rev-parse --show-toplevel`

## Proto

If you change `api/review.proto`, regenerate with:

```bash
protoc --proto_path=api --go_out=api/reviewpb --go_opt=paths=source_relative \
  --go-grpc_out=api/reviewpb --go-grpc_opt=paths=source_relative review.proto
```

## LM Studio token

The API token is stored in `~/.config/lm-review/config.toml` (0600). Never log or expose it. The token format is `sk-lm-{id}:{passkey}` and is validated against a SHA512 hash stored in `~/.lmstudio/.internal/permissions-store.json`.

## Large codebase reviews

Repos over 80KB of Go source are automatically split into chunks and reviewed in parallel, then merged. See `internal/review/chunked.go`.