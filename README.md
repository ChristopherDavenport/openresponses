# openresponses

A small Go library for the [Open Responses](https://www.openresponses.org)
specification (version 2026-04-24). It models the wire format and speaks
all three transports in both directions:

- **Client**: JSON over HTTP, Server-Sent Events, WebSocket.
- **Server**: an `http.Handler` that exposes any backend through a small
  `Adapter` interface, with request validation, SSE framing, sequence
  numbering, error envelopes and the WebSocket turn protocol handled for
  you.
- **Lossless extensions**: slug-prefixed items, content parts, tools,
  annotations and events that the spec does not define are kept verbatim,
  so a proxy built on this package never drops data.

The server side passes all 17 scenarios of the official compliance suite.
The core depends only on the standard library plus
`github.com/coder/websocket`.

## Install

```sh
go get github.com/christopherdavenport/openresponses
```

Requires Go 1.25.

## Client

```go
client := openresponses.NewClient("https://api.openai.com/v1",
    openresponses.WithAPIKey(os.Getenv("OPENAI_API_KEY")))

resp, err := client.Create(ctx, openresponses.Request{
    Model: "gpt-5",
    Input: openresponses.Input{openresponses.UserText("Say hello.")},
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
if err := stream.Err(); err != nil { /* ... */ }
final := stream.Response() // accumulated from the events
```

WebSocket turns run one at a time on a connection and support
`previous_response_id` continuation even with `store: false`:

```go
conn, err := client.Dial(ctx)
first, err := conn.Turn(ctx, req)
second, err := conn.Turn(ctx, openresponses.Request{
    Model: "gpt-5", PreviousResponseID: first.ID,
    Input: openresponses.Input{openresponses.UserText("And then?")},
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

`CollectStream` derives `Create` from `CreateStream`, and the
`UnsupportedStreaming` and `UnsupportedCompaction` types can be embedded
to decline what you do not implement. `NewResponse(req)` builds a
spec-shaped response that echoes the request's settings, and `NewID`
mints identifiers. See `examples/server` and the `echo` package.

The handler:

- routes `POST .../responses`, `POST .../responses/compact` and the
  WebSocket upgrade on `GET .../responses`;
- validates requests and returns `invalid_request` errors with `param`;
- assigns `sequence_number`, frames SSE, emits `error` +
  `response.failed` when an adapter fails mid-stream, synthesizes a
  terminal event if the adapter forgets one, and always ends with
  `[DONE]`;
- runs WebSocket turns sequentially, rejects the forbidden `stream`,
  `stream_options` and `background` fields, keeps a per-connection cache
  for `previous_response_id`, returns `previous_response_not_found` and
  evicts the cache entry after a failed continuation, and enforces the
  60-minute lifetime with `websocket_connection_limit_reached`.

## Extensions

Unknown types decode to `UnknownItem`, `UnknownContent`, `UnknownTool`,
`UnknownAnnotation` and `UnknownEvent`, which re-marshal their original
bytes. Register your own decoders with `RegisterItem`, `RegisterContent`,
`RegisterTool`, `RegisterAnnotation` and `RegisterEvent`. Unknown
top-level request and response keys are preserved in `Extra`.

## Compliance

`make compliance` clones the official
[openresponses/openresponses](https://github.com/openresponses/openresponses)
repository, serves the `echo` adapter on port 8000 and runs
`bin/compliance-test.ts` against it. It needs [bun](https://bun.sh).

`make test` runs the Go suite, which validates every marshalled shape
against the OpenAPI document in `testdata/`, round-trips golden fixtures
that include extension types, and exercises the SSE parser, client,
handler and WebSocket transport with in-process servers.

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
| `client.go` | `Client` |
| `handler.go` | `Handler`, `Adapter`, `EventSink` |
| `websocket.go` | WebSocket server session and `WebSocketConn` |
| `echo/` | deterministic adapter used by the compliance run |
| `cmd/openresponses-echo` | server binary for the compliance run |

## License

MIT. See [LICENSE](LICENSE).
