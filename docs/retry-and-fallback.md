# Retry and fallback

This document describes the request order used by the current execution pipeline.

## Request order

For a model request, the pipeline is:

```text
credential selection
→ credential preparation and execution
→ credential failover and cooldown handling
→ request retry rounds for the same model
→ model fallback, if the final error is eligible
→ final client response
```

Model fallback does not normally happen after the first credential failure. The auth manager first finishes the applicable credential and request retry logic for the current model.

The exact number of attempts depends on available credentials, per-credential overrides, cooldown state, Home scheduling, and the request retry settings.

## Current model fallback behavior

The active deployment configuration is expected to define a rule similar to:

```yaml
model-fallback:
  enabled: true
  preserve-requested-model: true
  trigger-status-codes: [429, 500, 502, 503, 504]
  no-fallback-status-codes: [400, 401, 403, 422]
  rules:
    - from: "gpt-*"
      to: ["grok-4.7", "grok-4.6"]
```

The fallback targets are ordered. If the first target fails with an eligible status, the next target is tried. Invalid requests, authentication failures, explicit request termination, and client cancellation do not enter this model fallback path.

## Streaming

Before the first valid response byte is sent to the client:

- a stream creation failure goes through auth manager retry handling;
- a bootstrap read failure may receive an additional same-model retry;
- a final eligible failure may switch to the next fallback model.

After response content has been sent, the current stream cannot be replaced by another model. A later disconnect is reported as a stream failure.

## Response model

When `preserve-requested-model` is enabled, a response served by a fallback model keeps the client-requested model in response model fields where supported. The actual provider and model remain visible in internal usage and diagnostic records.

## Tests required for changes

Changes to this behavior should cover the affected path, including as applicable:

- credential retry before model fallback;
- fallback status allow and deny lists;
- ordered fallback targets;
- non-streaming and streaming requests;
- bootstrap retry before the first byte;
- no fallback after streaming output starts;
- response model rewriting;
- Home retry and cooldown behavior.

