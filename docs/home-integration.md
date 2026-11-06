# CLIProxyAPIHome integration

CLIProxyAPIHome is the control plane for Home mode. CLIProxyAPI remains responsible for request execution, provider translation, response handling, and usage accounting.

## When a change must be checked in Home

Review the Home side whenever a change affects:

- configuration publication or revision fields;
- dispatch requests and responses;
- credential selection or concurrency;
- retry rounds or retry limits;
- cooldown and `retry-after` behavior;
- snapshots, node membership, leases, or heartbeats;
- Home-mode management endpoints;
- protocol or capability negotiation.

## Cooldown and retry

Home may mark a credential or model unavailable until a recovery time. The scheduler can then select another credential or return a retryable dispatch result such as `retry_after_ms`. Cooldown is credential/model availability scheduling; it is not model fallback.

The normal order is:

```text
Home credential selection
→ cooldown and concurrency decision
→ same-model credential/request retry
→ model fallback in the API handler, when eligible
```

Do not treat one unavailable credential as a reason to switch models immediately.

## Compatibility rules

- Prefer additive, optional fields.
- Older Home versions must not fail on unknown optional fields.
- New CLIProxyAPI code must have an explicit behavior when an older Home omits a field.
- Keep retry, cooldown, and concurrency meanings stable once published.
- Add protocol fixtures and tests for both success and retryable failure responses.

## Development and testing

Use mock Home responses and local credentials. Do not connect development tests to the production Home instance or production database.

For a protocol change, update both sides in a compatible order:

1. define the wire change and fallback behavior;
2. add CLIProxyAPI fixtures and tests;
3. implement the matching Home change;
4. test old and new combinations;
5. document the compatibility window;
6. release only after both components pass their checks.

Home deployment and recovery procedures are documented in [home/README.md](../home/README.md). The existing API contract is documented in [management-api-v8.md](management-api-v8.md).

