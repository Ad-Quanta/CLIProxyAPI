# Development

## Repository layout

- `cmd/`: server and maintenance commands.
- `internal/api/`: HTTP handlers, routes, and middleware.
- `internal/runtime/`: provider executors.
- `internal/translator/`: provider protocol translation.
- `internal/home/`: CLIProxyAPIHome communication and dispatch.
- `internal/registry/`: model registration and updates.
- `internal/watcher/`: configuration reload and watchers.
- `sdk/cliproxy/`: embeddable service and execution pipeline.
- `test/`: cross-module and integration tests.

Read `AGENTS.md` before changing code. It contains the binding architecture, security, payload, timeout, and testing rules.

## Local development

The default configuration is `config.yaml`; use `config.example.yaml` as a template. Authentication material belongs under `auths/` and must not be committed.

Run the server locally:

```bash
go run ./cmd/server
```

Build the server:

```bash
go build -o cli-proxy-api ./cmd/server
```

Run all tests:

```bash
go test ./...
```

For a focused change, run the affected package tests first, then run the full suite before review. The fallback integration harness is under `test/fallback/` and uses its own mock upstream.

## Implementation rules

- Keep changes small and preserve existing module boundaries.
- Add defaults, normalization, and parsing tests for new configuration.
- Keep business payload configuration as the final semantic step before sending a request.
- Treat streaming before the first client-visible byte differently from streaming after output has started.
- Do not add network timeouts after an upstream connection is established, except for the documented exceptions in `AGENTS.md`.
- Use mock credentials and local test services; never test against production accounts.

## Change checklist

1. Identify the request path and affected provider or protocol.
2. Check whether CLIProxyAPIHome needs a matching protocol or behavior change.
3. Add or update focused regression tests.
4. Run `gofmt`, `go test ./...`, and the required build command.
5. Review `git diff` and confirm no secrets or local deployment files are included.

## Related documentation

- [Retry and fallback](docs/retry-and-fallback.md)
- [CLIProxyAPIHome integration](docs/home-integration.md)
- [v8 Management API](docs/management-api-v8.md)
- [SDK documentation](docs/sdk-usage.md)
- [Home deployment notes](home/README.md)
- [Operations notes](OPS.md)

