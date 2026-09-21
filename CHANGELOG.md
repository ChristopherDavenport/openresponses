# Changelog

All user-visible changes to this library. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
uses [Semantic Versioning](https://semver.org/); before v1.0.0 minor
versions may break the API.

## v0.0.10 - 2026-09-21

- `chatcompletions`: an `Adapter` that serves any Chat Completions
  endpoint, built on `Client` for address, authentication and
  transport. `WithMaxTokensField`, `WithReasoningReplay` and `WithExtra`
  cover the places providers differ. Reasoning items round-trip only
  into the field `WithReasoningReplay` names, since the protocol has
  none.
- Provider adapters for Claude (`providers/anthropic`) and Gemini
  (`providers/gemini`) as separate modules, so their SDKs stay out of
  this module's dependency graph; each has its own changelog. A
  committed `go.work` builds them against the local root, and `make
  release-check` builds them the way consumers do.

## v0.0.9 - 2026-09-18

- **Breaking**: the WebSocket transport moved to the `websocket`
  subpackage so the root package depends on the standard library alone.
  `client.Dial(ctx)` is now `websocket.Dial(ctx, client)` and returns a
  `*websocket.Conn` (was `*WebSocketConn`); the handler options
  `WithWebSocketKeepalive`, `WithWebSocketLifetime`,
  `WithWebSocketOrigins` and `WithWebSocketCacheSize` are now
  `websocket.WithKeepalive`, `WithLifetime`, `WithOrigins` and
  `WithCacheSize` on `websocket.Handler`, which wraps a `Handler` to add
  the upgrade on `GET .../responses`. A bare `Handler` answers an upgrade
  request with `method_not_allowed`.
- For transports built outside the package: `ResolveContinuation`,
  `History` and `StampPreviousID` are exported, as are the `Adapter`,
  `Store` and `MaxBodyBytes` accessors on `Handler` and `RequestHeaders`,
  `HTTPClient` and `MaxResponseBytes` on `Client`, plus
  `ErrorFromResponse`.
- `make deps` (also in CI) fails if the root package ever depends on
  anything outside the standard library.

## v0.0.8 - 2026-09-16

- CI tests the minimum (1.25) and current Go versions, runs staticcheck
  and govulncheck, and pins the official compliance suite to a fixed
  upstream commit. A weekly workflow tracks upstream drift instead.
- Pushing an annotated tag now publishes a GitHub release.
- The vendored OpenAPI document carries its Apache 2.0 notice (see
  `NOTICE`).
- The internal planning document was removed from the repository.

## v0.0.7 - 2026-09-16

- Review fixes: `Stream` renamed to `Events`; `SequenceSetter` exported;
  absent-key passthrough on `Response`; WebSocket keepalive with a
  client-side reader goroutine; 16 MiB default body limit; header
  allowlist on `ErrorPayload.Err`; the store clones history.
- Schema validation moved to the nested `conformance` module.
- GitHub Actions CI.
- `examples/proxy`: a `Handler` over `client.AsAdapter()`.

## v0.0.6 - 2026-09-16

- `ClientAdapter` serves a remote server as an `Adapter`.
- `Error.Headers` carries only error-describing headers;
  `ResponseHeaders` holds the rest.

## v0.0.5 - 2026-09-16

- Documentation: the layer beyond the wire, the streaming lifecycle and
  the `ResponseStore` asymmetry.

## v0.0.4 - 2026-09-16

- `ResponseStore` resolves `previous_response_id` over every transport.
- Stream iterator.

## v0.0.3 - 2026-09-16

- `MessageWriter.Logprobs`.
- Generated call IDs documented as opaque.

## v0.0.2 - 2026-09-16

- `Emitter` and the `streamtest` package.
- Error headers.
- Unified `Usage` and `Reasoning` shapes.

## v0.0.1 - 2026-09-16

- First release.
