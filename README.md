# openresponses

[![Go Reference](https://pkg.go.dev/badge/github.com/ChristopherDavenport/openresponses.svg)](https://pkg.go.dev/github.com/ChristopherDavenport/openresponses)
[![CI](https://github.com/ChristopherDavenport/openresponses/actions/workflows/ci.yml/badge.svg)](https://github.com/ChristopherDavenport/openresponses/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/ChristopherDavenport/openresponses)](https://goreportcard.com/report/github.com/ChristopherDavenport/openresponses)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

A small Go library for the [Open Responses](https://www.openresponses.org)
specification (version 2026-04-24). It models the wire format and speaks
all three transports in both directions:

- **Client**: JSON over HTTP, Server-Sent Events, and WebSocket through
  the `websocket` subpackage.
- **Server**: an `http.Handler` that exposes any backend through a small
  `Adapter` interface, with request validation, SSE framing, sequence
  numbering and error envelopes handled for you; `websocket.Handler`
  wraps it and adds the WebSocket turn protocol.
- **Lossless passthrough**: slug-prefixed items, content parts, tools,
  annotations and events that the spec does not define are kept verbatim,
  unknown top-level keys survive in `Extra`, and a decoded response
  re-encodes without keys its source never sent, so a proxy built on
  this package never drops or invents data.

The server side passes all 17 scenarios of the official compliance suite.
The root package depends on the standard library alone, so it can be the
shared vocabulary of tool libraries that never open a socket; only the
`websocket` subpackage pulls in `github.com/coder/websocket`.

## Install

```sh
go get github.com/ChristopherDavenport/openresponses
```

Requires Go 1.25. The library is pre-1.0: minor versions may change the
API, and [CHANGELOG.md](CHANGELOG.md) records every break.

## Client

```go
client := openresponses.NewClient("https://api.openai.com/v1",
    openresponses.WithAPIKey(os.Getenv("OPENAI_API_KEY")))

resp, err := client.Create(ctx, openresponses.Request{
    Model: "gpt-5",
    Input: openresponses.Items{openresponses.UserText("Say hello.")},
})
fmt.Println(resp.OutputText())
```

Streaming yields decoded events; switch on the concrete pointer types:

```go
stream, err := client.CreateStream(ctx, req)
defer stream.Close()
for ev := range stream.Events() {
    if d, ok := ev.(*openresponses.OutputTextDeltaEvent); ok {
        fmt.Print(d.Delta)
    }
}
final, err := stream.Wait() // the response folded from the events, or the failure
```

WebSocket turns run one at a time on a connection and support
`previous_response_id` continuation even with `store: false`. The
transport is the `websocket` subpackage and dials through the same
client:

```go
conn, err := websocket.Dial(ctx, client)
first, err := conn.Turn(ctx, req)
second, err := conn.Turn(ctx, openresponses.Request{
    Model: "gpt-5", PreviousResponseID: first.ID,
    Input: openresponses.Items{openresponses.UserText("And then?")},
})
```

Non-2xx responses and error events surface as `*openresponses.Error`,
with `IsNotFound`, `IsRateLimited` and `IsInvalidRequest` helpers.

## Server

Implement `Adapter` and mount the handler under any prefix:

```go
type Adapter interface {
    Create(ctx context.Context, req Request) (*Response, error)
    CreateStream(ctx context.Context, req Request, sink EventSink) error
    Compact(ctx context.Context, req CompactRequest) (*CompactResponse, error)
}

http.Handle("/v1/", openresponses.NewHandler(myAdapter))
```

To serve WebSocket as well, wrap the handler; the wrapper shares the
adapter, body limit and response store, and passes everything but the
upgrade through:

```go
http.Handle("/v1/", websocket.Handler(openresponses.NewHandler(myAdapter), websocket.WithOrigins("app.example")))
```

Streaming adapters should not hand-roll the item lifecycle. `Emitter`
owns the bookend events, indices, item IDs and the response snapshot;
adapters only supply content:

```go
func (a *myAdapter) CreateStream(ctx context.Context, req openresponses.Request, sink openresponses.EventSink) error {
    em := openresponses.NewEmitter(sink, openresponses.NewResponse(req))
    msg, err := em.Message(openresponses.PhaseFinalAnswer)
    if err != nil {
        return err
    }
    for delta := range upstream {
        if err := msg.Text(delta); err != nil {
            return err
        }
    }
    em.Response().Usage = &usage
    return em.Complete() // closes the message, sends response.completed
}
```

`Emitter.FunctionCall`, `Emitter.Reasoning` and `Emitter.Item` cover the
other item kinds. To fail a response, return an error; the handler emits
the error event and `response.failed` with the right transport
semantics, and forwards any `Error.Headers` (for example `Retry-After`
from an upstream 429) to the HTTP response and the error payload.

`CollectStream` derives `Create` from `CreateStream`, `Events` turns a
streaming adapter into a pull-shaped `iter.Seq2` for in-process use,
`client.AsAdapter()` serves a remote server as an `Adapter` (so a proxy,
fallback chain or fan-out composes through one interface, WebSocket
downstream included), and the `UnsupportedStreaming` and
`UnsupportedCompaction` types can be embedded to decline what you do not
implement. `NewResponse(req)` builds a
spec-shaped response that echoes the request's settings, `NewID` mints
identifiers, and `InvalidRequest`, `NotFound`, `PreviousResponseNotFound`,
`TooManyRequests`, `ModelError` and `ServerError` build the errors the
handler maps to the right envelope.

`previous_response_id` is resolved by the handler when it has a
`ResponseStore`:

```go
openresponses.NewHandler(adapter, openresponses.WithResponseStore(openresponses.NewMemoryStore(1024)))
```

Over HTTP, on `/responses/compact` and over WebSocket alike, the stored
history is inlined ahead of the new input and the field is cleared, so
the adapter always sees a self-contained conversation; stored responses
are saved after they complete, `store: false` ones never are, and an
unknown ID yields `previous_response_not_found`. `MemoryStore` is a
bounded in-memory implementation for a single replica; a fleet supplies
its own three-method implementation. Without a store, HTTP requests
reach the adapter with `previous_response_id` untouched.

The `streamtest` package unit-tests an adapter's stream without a
server: `streamtest.Run` records the events, validates the item
lifecycle ordering, and returns the folded response. See `examples/server` and the `echo` package.

Adapters for model APIs that do not speak Open Responses come with the
repository. `chatcompletions` serves any Chat Completions endpoint, the
protocol most providers offer, over the same `Client` options used for
an Open Responses upstream:

```go
upstream := openresponses.NewClient("https://api.deepseek.com/v1", openresponses.WithAPIKey(key))
http.Handle("/v1/", openresponses.NewHandler(chatcompletions.New(upstream)))
```

`providers/anthropic` and `providers/gemini` serve Claude and Gemini
through their own SDKs (Vertex AI and Bedrock included) and are
separate modules so those SDKs stay out of this one's dependency
graph. Every adapter maps fields it cannot express to an
`invalid_request` naming the field rather than dropping them, and
carries provider-specific tools, blocks and reasoning signatures
through slug-prefixed extension types so a conversation replays without
loss; each README lists the exceptions.

The handler:

- routes `POST .../responses` and `POST .../responses/compact`, with
  `websocket.Handler` adding the upgrade on `GET .../responses`;
- validates requests and returns `invalid_request` errors with `param`,
  rejects `background` (it runs every response to completion) and caps
  bodies at 16 MiB by default (`WithMaxBodyBytes`);
- assigns `sequence_number`, frames SSE, emits `error` +
  `response.failed` when an adapter fails mid-stream, synthesizes a
  terminal event if the adapter forgets one, and always ends with
  `[DONE]`;
- with `websocket.Handler`, runs WebSocket turns sequentially, rejects
  the forbidden `stream`, `stream_options` and `background` fields,
  keeps a per-connection cache for `previous_response_id` (consulted
  before the shared store), returns `previous_response_not_found` and
  evicts the cache entry after a failed continuation, enforces the
  60-minute lifetime with `websocket_connection_limit_reached`, and
  pings idle connections every 30 seconds so a vanished peer is dropped
  (`websocket.WithKeepalive`).

## Running the examples

The client examples read `OPENRESPONSES_BASE_URL`, `OPENRESPONSES_API_KEY`
and `OPENRESPONSES_MODEL`, so the same programs run against any server
that implements the specification. Against OpenAI:

```sh
OPENRESPONSES_API_KEY=sk-... go run ./examples/tools
```

Against a local [Ollama](https://ollama.com), which serves Open Responses
under `/v1` with no API key (it does not offer the WebSocket transport):

```sh
OPENRESPONSES_BASE_URL=http://localhost:11434/v1 OPENRESPONSES_MODEL=qwen3:1.7b go run ./examples/tools
```

Against the example server in this repository, which speaks all three
transports:

```sh
go run ./examples/server &
OPENRESPONSES_BASE_URL=http://localhost:8000/v1 go run ./examples/websocket
```

`examples/proxy` is a `Handler` over `client.AsAdapter()` with a
`ResponseStore`, forwarding to Ollama by default. It adds what the
upstream lacks, the WebSocket transport and stored continuation, so the
WebSocket example runs against Ollama through it:

```sh
go run ./examples/proxy &
OPENRESPONSES_BASE_URL=http://localhost:8000/v1 OPENRESPONSES_MODEL=qwen3:1.7b go run ./examples/websocket
```

## Extensions

Unknown types decode to `UnknownItem`, `UnknownContent`, `UnknownTool`,
`UnknownAnnotation` and `UnknownEvent`, which re-marshal their original
bytes (an `UnknownEvent` takes the sequence number a sink assigns it, so
a proxied extension event stays in order). Register your own decoders
with `RegisterItem`, `RegisterContent`, `RegisterTool`,
`RegisterAnnotation` and `RegisterEvent`. Unknown top-level request and
response keys are preserved in `Extra`. That passthrough is a policy
decision for a server: any key a client sends reaches the upstream
provider unless the adapter clears or filters `Extra`.

Errors built from remote data (an HTTP error response, an error event or
a WebSocket error frame) carry only the headers that describe a failure,
such as `Retry-After` and the rate-limit family, so a peer cannot plant
headers on a server that forwards its error.

## Compliance

`make compliance` fetches the official
[openresponses/openresponses](https://github.com/openresponses/openresponses)
repository at the commit pinned in the Makefile, serves the `echo`
adapter on port 8000 and runs `bin/compliance-test.ts` against it. It
needs [bun](https://bun.sh). CI runs the same pinned commit; a weekly
workflow runs the suite from upstream `main` and checks the published
OpenAPI document against the copy in `testdata/` so drift surfaces
without breaking pull requests.

`make test` runs the Go suite, which round-trips golden fixtures that
include extension types and exercises the SSE parser, client, handler
and WebSocket transport with in-process servers, and then the
`conformance` module, which validates every marshalled shape against the
OpenAPI document in `testdata/`. That module is nested so the JSON Schema
validator it needs stays out of the library's dependency graph. `make
deps` checks that the root package builds from the standard library
alone; `github.com/coder/websocket` is reachable only through the
`websocket` subpackage.

## Layout

| File | Contents |
|---|---|
| `enums.go` | statuses, roles, effort levels and wire type constants |
| `items.go`, `content.go`, `tools.go` | items, content parts, annotations, tools, `ToolChoice`, `TextFormat` |
| `registry.go` | type registries and `Unmarshal*` dispatch |
| `request.go`, `response.go` | `Request`, `CompactRequest`, `Response`, `CompactResponse`, `Validate` |
| `errors.go` | `Error`, `ErrorPayload`, error types and codes |
| `events.go` | the 24 streaming events, `ErrorEvent`, `UnknownEvent`, `DecodeEvent` |
| `sse.go`, `stream.go` | SSE framing, `EventStream`, `Accumulator` |
| `emitter.go` | `Emitter` and the item writers for streaming adapters |
| `client.go` | `Client` |
| `clientadapter.go` | `ClientAdapter`, a remote server as an `Adapter` |
| `handler.go` | `Handler`, `Adapter`, `EventSink`, `Events` |
| `store.go` | `ResponseStore`, `MemoryStore`, continuation resolution |
| `websocket/` | the WebSocket transport: `Handler` wraps the HTTP handler, `Dial` opens a client `Conn` |
| `chatcompletions/` | adapter serving a Chat Completions endpoint, built on `Client` |
| `streamtest/` | recording sink and stream validator for adapter tests |
| `echo/` | deterministic adapter used by the compliance run |
| `cmd/openresponses-echo` | server binary for the compliance run |
| `examples/` | client programs, a server, and a proxy over `ClientAdapter` |
| `conformance/` | nested module: schema validation of every wire shape against the OpenAPI document |
| `providers/` | adapters for Claude and Gemini through their SDKs, one nested module each; see `providers/PLAN.md` |

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for the development workflow and
the release process, and [SECURITY.md](SECURITY.md) for reporting
vulnerabilities.

## License

MIT. See [LICENSE](LICENSE). The OpenAPI document under `testdata/` is
redistributed from the Open Responses project under the Apache License
2.0; see [NOTICE](NOTICE).
