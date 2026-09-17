# Changelog

All user-visible changes to this library. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
uses [Semantic Versioning](https://semver.org/); before v1.0.0 minor
versions may break the API.

## Unreleased

Nothing yet.

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
