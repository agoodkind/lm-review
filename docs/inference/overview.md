# Inference Overview

lm-review serves a declaration-driven `Inference` gRPC service. Each request supplies a prompt, input, output JSON Schema, optional opaque JSON context, and optional model override.

`Inference.Infer` validates the request before invoking the configured OpenAI-compatible backend. The service sends the caller's schema through strict JSON Schema `response_format`, then validates the returned JSON against the same schema before returning it.

The context value is optional JSON. lm-review preserves it as opaque data and does not interpret its keys or assign application meaning to it.

Run `lm-review inference` to start the persistent listener. Configure its default model and listen address under `[inference]` in `config.toml`.
