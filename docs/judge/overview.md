# Judge Overview

lm-review serves a generic `Judge` gRPC service that turns a command, its context, and a rule-set id into a block or allow verdict. The service is defined in `api/judgepb/judge.proto` and implemented in `internal/judge/`.

## Surface

`Judge.Evaluate(input_text, context, rule_set_id)` returns a verdict, a reason, and a confidence. `Judge.ListRuleSets` returns each rule set and the context fields it needs. The `lm-review judge` subcommand in `cmd/lm-review/main.go` serves the gRPC on the configured `[judge] listen_address`.

## Rule sets

`internal/judge/ruleset.go` holds the registry. Each rule set declares its required context and renders a system and user prompt from the request.

- `search-guard` (`ruleset_search.go`) requires `indexed_roots`. It judges whether a code-search command reads inside an indexed codebase.
- `worktree-guard` (`ruleset_worktree.go`) requires `worktree`. It judges whether a command writes to a primary checkout, mutates git on a protected branch, or moves a ref checked out by another worktree.

## Model call

`internal/judge/model.go` `Decide` makes a decision-only lmd call (temperature 0, small max_tokens) and parses the first block or allow token. A transport error is returned so the caller decides. The model id comes from `[judge] model` and is served by lmd. The call reuses the `internal/lmstudio` client.

## Consumption

agent-gate dials `judgepb.NewJudgeClient` and calls `Evaluate` in parallel with its own deterministic oracle. The judge is one input to that composition. lm-review does not enforce anything itself.

## Tests

`internal/judge/ruleset_test.go`, `internal/judge/model_test.go`, and `internal/judge/service_test.go` hold the behavior.
