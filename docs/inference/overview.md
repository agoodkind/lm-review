# Inference Overview

lm-review serves a declaration-driven `Inference` gRPC service. Each request supplies a prompt, input, output JSON Schema, optional opaque JSON context, optional model override, and optional typed generation settings.

`Inference.Infer` validates the request before invoking the configured OpenAI-compatible backend. The service sends the caller's schema through strict JSON Schema `response_format`, then validates the returned JSON against the same schema before returning it. A model that returns one exact bare enum token remains compatible when the schema declares one required string enum property and rejects additional properties; lm-review wraps that token into the declared object before validation. Other non-JSON output remains an error.

Generation settings can select reasoning effort, a maximum completion token count, and temperature. Requests without generation options use the configured legacy chat settings and `max_tokens`. Once a caller supplies an option, the service sends the explicit settings and uses the configured response-token limit as `max_completion_tokens` when the caller omits that limit.

Each successful reply includes generic invocation metadata. The metadata records request and service identity, requested and actual models, backend identity when available, prompt and schema hashes, token usage when the backend reports it, finish reason, and model-call latency. Unknown actual model identity and absent or null token usage remain unset. Callers retain the original input and output beside this metadata when they need a complete decision record.

The context value is optional JSON. lm-review preserves it as opaque data and does not interpret its keys or assign application meaning to it.

Run `lm-review inference` to start the persistent listener. Configure its default model and listen address under `[inference]` in `config.toml`. When `base_url` is omitted, inference inherits the global endpoint and token. An inference `token_file` replaces the token on that inherited endpoint without changing ordinary review credentials. Setting `base_url` does not inherit the global token, so configure `token_file` when the inference endpoint requires one. Relative file paths resolve from the lm-review config directory, and paths may start with `~/`. Inline `token` remains supported as a mutually exclusive alternative. A request-level model selects only the model identifier and does not change the endpoint or credential.

```toml
[inference]
model = "your-structured-output-model"
listen_address = "[::1]:5401"
# base_url = "https://inference.example.com"
# token_file = "~/.config/lm-review/inference.token"
```
