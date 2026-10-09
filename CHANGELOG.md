# Changelog

All user-visible changes to this library. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
uses [Semantic Versioning](https://semver.org/); before v1.0.0 minor
versions may break the API.

## Unreleased

- A function call that the token limit cut off now ends `incomplete` in
  the `chatcompletions` adapter, on `finish_reason` `length` or
  `content_filter`. The adapter closed the call before it read the
  finish reason, so `output_item.done` reported a call with partial,
  unparseable arguments as `completed`, and a consumer that ran calls as
  their items finished would run it. The call now stays open until the
  response ends, and `output_item.done` and the final response both say
  `incomplete`; a call the model finishes is `completed` as before.
- `Emitter.Incomplete` no longer marks an item whose writer was already
  closed. It marked whichever writer it held last, open or not, so the
  final response could call an item `incomplete` after its
  `output_item.done` had said `completed`. That happened to a Gemini
  function call, which arrives whole, when `MAX_TOKENS` ended the
  output right after it: the call was complete, and now both say so. An
  adapter that learns why output stopped only after an item ends has to
  leave that item open for `Incomplete` to mark it.

## v0.0.15 - 2026-10-04

- Decoding accepts the names OpenAI's Responses API gives the reasoning
  text events, `response.reasoning_text.delta` and
  `response.reasoning_text.done`, as the same `ReasoningDeltaEvent` and
  `ReasoningDoneEvent` the specification spells `response.reasoning.delta`
  and `response.reasoning.done`. A server that follows OpenAI (OpenRouter
  among them) streamed its reasoning whole as far as the client was
  concerned: the deltas decoded to `UnknownEvent`, so reasoning showed
  once at completion instead of streaming. Marshalling still emits the
  specification's names.

## v0.0.14 - 2026-10-02

- `Accumulator.Position` and `Accumulator.ItemAt` map an output index to
  the item the stream's events there currently name: its position in
  `Response().Output`, and the item itself. Since v0.0.13 appends an item
  opened at an already-closed index, `Output[i]` is no longer the item an
  event at index `i` names once an index is reused; a consumer that reads
  items by index as events arrive no longer has to mirror the
  accumulator's bookkeeping to find them. For a conforming stream
  `Position(i)` is `(i, true)` for every index opened. The result is
  false for an index never opened and for a negative index. A terminal
  snapshot replaces `Output`, and so does one before it that carries
  output while no index has been reused; positions are indexes again,
  so the mapping is only that of the server's own list from then on. A
  snapshot with no output keeps what is held, and one after a reuse
  keeps the mapping and appends the items it alone names with no index.

## v0.0.13 - 2026-10-02

- `streamtest.WithOutputIndexReuse` is an `Option`, accepted by `Validate`
  and `Run`, that accepts an output index reused after `output_item.done`
  at that index, the stream Ollama 0.23 emits for parallel function calls.
  Validation stays strict without it, and the option relaxes only that one
  rule: a first use of an index must still be the next unused one, the
  events inside an item must still name the index it was added at, and the
  terminal response must still hold every item added. It is for tests of
  code that reads such a stream, which no longer have to rewrite the
  indexes to satisfy the validator. `Validate` and `Run` gain a variadic
  `...Option` parameter, so existing calls compile unchanged.
- `Accumulator` keeps every item of a stream that reuses an output index
  after closing the item there, which is how Ollama 0.23 streams parallel
  function calls: the later item is appended instead of overwriting the
  first, the events that follow at that index reach it, and
  `Accumulator.ReusedIndexes` and `EventStream.ReusedIndexes` report the
  reuse, since a position in `Output` is then not an output index. A
  response read mid-stream, or from a stream cut before its terminal
  snapshot, now holds every call; the terminal snapshot still replaces
  `Output` as before, while a snapshot before it is not taken once an
  index has been reused, since its positions and the indexes of the
  events still to come would not agree; an item only such a snapshot
  names is appended, by id. `streamtest.Validate` still rejects such a stream
  as non-conforming unless given `streamtest.WithOutputIndexReuse`. (#26)
- Release process: `release-guard` takes the version floor, and the
  tag-exists check, from the tags origin has published, read once with
  `git ls-remote` and joined with the local tags, so a clone that has not
  fetched cannot approve a version below one already on the proxy. An
  unreachable origin refuses rather than falling back to the stale floor.
  A test under `scripts/` reproduces the stale clone. (#20)

## v0.0.12 - 2026-09-23

- Each provider module now requires the root at exactly the version it
  is released at, rather than at the previous release, and carries a
  `replace` of the root pointing at the tree. A consumer who takes only
  `providers/anthropic` at a version now gets the root commit that
  provider was built and tested against, instead of the one before it.
  Consumers ignore a `replace` in a dependency, so only the `require`
  reaches them; the published `go.sum` files no longer carry first-party
  entries. This is the shape OpenTelemetry-Go publishes.

## v0.0.11 - 2026-09-23

No library changes; the API is identical to v0.0.10. Released so every
published module in the repository shares one version line.

- Release process: `make release-guard TAG=...` checks a tag before it
  is pushed, refusing a version that sorts below the current root
  release, a required root version the proxy does not serve, or a module
  that fails to build with `GOWORK=off`. CI runs `make release-check` on
  every push, so `main` stays taggable rather than the check running
  after a tag is already permanent. `CLAUDE.md` documents the procedure.
- `make no-replace`, part of `check`, refuses a `replace` of a
  first-party module in any published module. Consumers ignore a
  `replace`, so a provider carrying one builds green everywhere here,
  `release-check` included, while shipping a `go.mod` naming a root
  version it was never built against. `conformance` is the one
  exemption, and it is never published.
- `make tidy-check` fails when `go mod tidy` would change any `go.mod`
  or `go.sum`, without writing, so a stray requirement surfaces in
  `check` instead of only in CI's diff.
- `providers/anthropic` and `providers/gemini` v0.0.11 retract their
  v0.0.1, which required root v0.0.9 and so downgraded consumers that
  resolved it.

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
