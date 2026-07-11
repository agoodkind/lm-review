# Inference Overview

lm-review serves a declaration-driven `Inference` gRPC service. Each request supplies a prompt, input, output JSON Schema, optional opaque JSON context, optional model override, and optional typed generation settings.

`Inference.Infer` validates the request before invoking the configured OpenAI-compatible backend. The service sends the caller's schema through strict JSON Schema `response_format`, then validates the returned JSON against the same schema before returning it.

Generation settings can select reasoning effort, a maximum completion token count, and temperature. Omitted settings remain absent from the backend request, except that the service applies its configured response-token limit when the caller omits that limit.

Each successful reply includes generic invocation metadata. The metadata records request and service identity, requested and actual models, backend identity when available, prompt and schema hashes, token usage, finish reason, and model-call latency. Callers retain the original input and output beside this metadata when they need a complete decision record.

The context value is optional JSON. lm-review preserves it as opaque data and does not interpret its keys or assign application meaning to it.

Run `lm-review inference` to start the persistent listener. Configure its default model and listen address under `[inference]` in `config.toml`.
