# CLAUDE.md

## What this is

`lm-review` is a local LLM code review tool. It exposes a gRPC daemon, CLI commands, and MCP tools for Claude Code. It calls LM Studio headlessly via the `lms` CLI.

## Build

```bash
make build    # build + run lm-review diff if installed
make deploy   # go install to $GOPATH/bin
make test     # run tests
make check    # full: build + vet + lint + test + govulncheck
```

Never run `go build` or `go test` directly. Always use make.

## Key packages

- `internal/review/` - `Result`, `Parse()`, `Reviewer`, chunked reviews. This is the core type system.
- `internal/lmstudio/` - HTTP client (openai-go/v3) + `lms` CLI lifecycle management
- `internal/daemon/` - gRPC server, serializes LM Studio calls, writes audit log
- `internal/mcpserver/` - MCP stdio server using mark3labs/mcp-go
- `internal/xdg/` - XDG path helpers
- `internal/audit/` - JSONL audit log at `~/.local/state/lm-review/audit.jsonl`
- `api/review.proto` - gRPC service definition (regenerate with `make proto`)

## Rules

- All config is TOML. Config lives at `~/.config/lm-review/config.toml`.
- Use `slog` for logging. Never `fmt.Fprintf(os.Stderr)` for diagnostics.
- XDG paths only. See `internal/xdg/xdg.go`.
- `Result` in `internal/review/result.go` is the canonical output type. Convert to proto only at the daemon boundary.
- MCP tool handlers must return friendly text on error, never hard error results.

## Proto regeneration

```bash
protoc --proto_path=api --go_out=api/reviewpb --go_opt=paths=source_relative \
  --go-grpc_out=api/reviewpb --go-grpc_opt=paths=source_relative review.proto
```

## Daemon

The daemon auto-starts on first `daemon.Connect()` call. To restart after binary update:

```bash
pkill -f "lm-review daemon" && rm -f $TMPDIR/lm-review/daemon.sock
```

## LM Studio token

Stored in `~/.config/lm-review/config.toml` (mode 0600). Token format: `sk-lm-{id}:{passkey}`. The passkey SHA512 hash is stored in `~/.lmstudio/.internal/permissions-store.json`. Never log or expose the token.

## Large repos

Repos over 80KB of Go source are automatically chunked. See `internal/review/chunked.go`.
