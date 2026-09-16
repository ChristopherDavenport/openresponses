# Plan: a Go implementation of the Open Responses specification

Target spec: **Open Responses 2026-04-24** (https://www.openresponses.org/specification).
Source of truth for shapes: `openapi.json` from `openresponses/openresponses` at
`public/openapi/2026-04-24/openapi.json` (a copy sits next to this file as
`openresponses-openapi-2026-04-24.json`).

Reference implementation to crib design from, not to depend on:
`github.com/joeychilson/openresponses@v0.0.0-20260610175401-1bc6e6763ed5` (MIT).
The GitHub repo is gone but the module is still in the Go proxy
(`go mod download github.com/joeychilson/openresponses@v0.0.0-20260610175401-1bc6e6763ed5`).
It requires Go 1.26; this plan targets Go 1.25.

## 1. Goals and non-goals

Goals

- A single importable package that speaks the spec on the wire, in both
  directions: client (talk to any Open Responses server) and server (expose a
  backend as a spec-conformant endpoint).
- All three transports: JSON over HTTP, SSE streaming, WebSocket.
- Lossless handling of provider extensions (slug-prefixed item, content, event
  and tool types) so a proxy built on this package never drops data.
- Passes the official compliance suite (`bin/compliance-test.ts`) on the server
  side, and round-trips every example in the OpenAPI file on the type side.
- Standard library only for the core; one dependency for WebSocket.

Non-goals

- Provider-specific presets (OpenRouter headers, OpenAI-only fields). Those
  belong in tiny sibling packages later, if at all.
- An agent loop, tool runner, or retry policy. Those are consumers of this
  package.
- Modelling OpenAI-proprietary items (web_search_call, etc.) as first-class
  types. They flow through as `UnknownItem`.

## 2. Spec facts the design has to honour

Endpoints (OpenAPI `paths`)

| Path | Method | Notes |
|---|---|---|
| `/responses` | POST | JSON body. Returns `ResponseResource`, or `text/event-stream` when `stream: true`. |
| `/responses` | GET + Upgrade | WebSocket transport (`x-openresponses-websocket`). |
| `/responses/compact` | POST | Returns `CompactResource` with `object: "response.compaction"`. |

Item types (discriminator `type`)

| type | Direction | Notes |
|---|---|---|
| `message` | both | `role` is user / system / developer / assistant. Assistant messages also carry `phase`. Content is `input_*` for non-assistant and `output_text` / `refusal` for assistant. |
| `function_call` | both | `call_id`, `name`, `arguments` (JSON string). Output form requires `id` and `status`; input form does not. |
| `function_call_output` | both | `output` is **either a JSON string or an array of content parts**. |
| `reasoning` | both | `summary` (required, list of `summary_text`), `content` (list of `reasoning_text`), `encrypted_content`. |
| `compaction` | both | `encrypted_content` required, `created_by` on output. |
| `item_reference` | input only | `{type, id}`; `type` is optional in the param form. |

Content part types

| type | Where |
|---|---|
| `input_text`, `input_image`, `input_file`, `input_video` | user / system / developer messages |
| `output_text` (with `annotations`, optional `logprobs`), `refusal` | assistant messages |
| `text` | function_call_output parts |
| `summary_text` | reasoning.summary |
| `reasoning_text` | reasoning.content |

Annotation types: `url_citation` only. Tool types: `function` only. Everything
else is an extension.

Other unions

- `input`: string **or** `[]Item`. A bare string means one user message.
- `tool_choice`: `"none" | "auto" | "required"` **or** `{type:"function", name}`
  **or** `{type:"allowed_tools", tools, mode}`.
- `text.format`: `{type:"text"}` | `{type:"json_object"}` | `{type:"json_schema", name, schema, strict, description}`.
- `include`: `reasoning.encrypted_content`, `message.output_text.logprobs`.
- `reasoning.effort`: `none | low | medium | high | xhigh` (plus `minimal` in descriptions; accept it).
- `truncation`: `auto | disabled`. `service_tier`: `auto | default | flex | priority`.

Statuses

- Item: `in_progress`, `completed`, `incomplete`. Terminal states are
  `completed` and `incomplete`; an `incomplete` item must be the last item and
  forces the response to `incomplete`.
- Response: `queued`, `in_progress`, `completed`, `incomplete`, `failed`
  (OpenAPI leaves it a free string; model as a string-typed const set).

Streaming (SSE)

- Header `Content-Type: text/event-stream`.
- Each frame: `event: <type>` line must equal `data.type`; `id:` should not be used.
- Terminal frame is the literal `data: [DONE]`.
- `sequence_number` is monotonically increasing per response.
- 24 event types plus `error`; the first event is `response.created` and the
  last is one of `response.completed | response.failed | response.incomplete`.
  An `error` event is always followed by `response.failed`.
- Item lifecycle on the wire: `output_item.added` → (`content_part.added` →
  deltas → `<kind>.done` → `content_part.done`)* → `output_item.done`.
- Reasoning summaries use `summary_index` instead of `content_index`.
- Delta events may carry `obfuscation` padding when `stream_options.include_obfuscation` is set.

WebSocket

- Upgrade on `GET /v1/responses`.
- Client sends `{"type":"response.create", ...CreateResponseBody}`. The fields
  `stream`, `stream_options`, `background` MUST NOT be present.
- At most one in-flight response per connection; extra `response.create`
  messages queue and run sequentially.
- Server events are the same SSE event objects, one JSON object per text frame,
  no `[DONE]`.
- Server SHOULD keep the latest response in connection-local memory so
  `previous_response_id` continuation works even with `store: false`.
- If a referenced previous response is unavailable: error code
  `previous_response_not_found`, and that id is evicted.
- Hard 60-minute lifetime; on expiry the server sends an error with code
  `websocket_connection_limit_reached`.
- WebSocket errors are `{"type":"error","status":<int>,"error":{code,message,type?,param?}}`.

Errors

| `type` | HTTP |
|---|---|
| `invalid_request` (also seen as `invalid_request_error`) | 400 |
| `not_found` | 404 |
| `too_many_requests` | 429 |
| `model_error` | 500 |
| `server_error` | 500 |

HTTP body: `{"error": {"type","code","message","param"}}`. Stream:
`{"type":"error","sequence_number",…,"error":{type,code,message,param,headers?}}`.

Extensions

- Item, content, tool, annotation and event types outside the spec MUST be
  prefixed with an implementor slug: `acme:search_result`, `openai:web_search_call`.
- Every item, including extensions, has `id`, `type`, `status`.
- Clients SHOULD tolerate unknown items and events (ignore or treat as opaque).

## 3. Package shape

One module, one main package, zero-to-one dependencies.

```
openresponses/
  doc.go            package docs, spec version constant
  enums.go          string-typed enums + constants (statuses, effort, truncation, …)
  items.go          Item interface, concrete items, Items slice, registry, UnknownItem
  content.go        Content interface, concrete parts, Contents slice, registry, UnknownContent
  tools.go          Tool interface, FunctionTool, ToolChoice union, TextFormat union, registries
  request.go        Request (CreateResponseBody), Input union, CompactRequest, Extra passthrough
  response.go       Response (ResponseResource), Usage, IncompleteDetails, CompactResponse, helpers
  errors.go         Error payload, *APIError (client side), *SpecError (server side), status mapping
  events.go         StreamEvent interface, 25 event structs, UnknownEvent, DecodeEvent, EncodeEvent
  sse.go            SSE reader (bufio.Scanner split func) and writer (flush per frame, [DONE])
  client.go         Client, options, Create, CreateStream, Compact
  stream.go         EventStream: Next/Event/Err/Close + Events() iter.Seq
  handler.go        Handler (net/http), Adapter interface, EventSink, HTTP/SSE dispatch
  websocket.go      WS client (Dial, Send, Events) and server upgrade + turn cache
  internal/jsonx/   tiny helpers: peekType(raw) string, merge known struct + extra map
  testdata/
    openapi-2026-04-24.json
    golden/*.json    hand-written or generated frames and bodies
```

Go version: `go 1.25`. Use `iter.Seq` (1.23+). Do not use `new(expr)` (1.26).

Dependencies

- Core: standard library only.
- WebSocket: `github.com/coder/websocket` (the maintained fork of nhooyr). Keep
  it in the main package; an `x/net/websocket` alternative is not worth it.
- Tests only: `github.com/santhosh-tekuri/jsonschema/v6` to validate every
  marshalled payload against the OpenAPI schemas.
- Optional later: `github.com/google/jsonschema-go` to derive `FunctionTool.Parameters`
  from a Go struct. Skip in v1; accept `json.RawMessage` / `map[string]any`.

## 4. Type design decisions

**One Go type per item, not Param/Resource pairs.** The OpenAPI splits
`FunctionCallItemParam` (input, `id` optional) from `FunctionCall` (output, `id`
and `status` required). Model each as one struct with `ID string` and
`Status Status` marked `omitempty`. The server side fills them in; the client
side leaves them empty on input. This halves the surface and matches how
consumers think.

**Polymorphism via small interfaces plus a decode registry.**

```go
type Item interface{ ItemType() string }
type Content interface{ ContentType() string }
type Tool interface{ ToolType() string }
type Annotation interface{ AnnotationType() string }
type StreamEvent interface{ EventType() string; Sequence() int64 }
```

Decoding peeks at `"type"` and dispatches through a `map[string]func(json.RawMessage) (T, error)`
guarded by `sync.RWMutex`. Built-ins are registered in `init`. Exported
`RegisterItem`, `RegisterContent`, `RegisterTool`, `RegisterAnnotation`,
`RegisterEvent` let extension packages plug in. Unregistered types decode to
`UnknownItem{Type, ID, Status, Raw json.RawMessage}` (and the analogues), and
re-marshal as the original bytes. This is the property that makes a proxy lossless.

**Message role decides which content is legal, but the type does not enforce it.**
A single `Message{Role, Content Contents, Phase, Status}` with constructors
`UserText`, `UserMessage(parts…)`, `SystemMessage`, `DeveloperMessage`,
`AssistantText`. Validation of role/content pairing lives in a `Validate()`
method used by the server handler, not in the marshaller.

**Two-valued fields get a dedicated type with custom JSON.**

- `Input` wraps `[]Item` and unmarshals a bare string into one user message.
  Marshal always emits the array form. Remember the original shape only if a
  test proves a provider cares; none does today.
- `FunctionCallOutputData{Text *string, Parts Contents}` marshals whichever is set.
- `ToolChoice{Mode string, Function *FunctionToolChoice, Allowed *AllowedToolChoice}`.
- `TextFormat{Type string, Name, Description string, Schema json.RawMessage, Strict *bool}`.

**Nullable vs absent.** `ResponseResource` requires most fields present with
`null` allowed (`completed_at`, `previous_response_id`, `error`, `usage`, ...).
Use pointers there and marshal explicitly, no `omitempty`, so a server built on
this package emits spec-shaped nulls. On `Request` use `omitempty` everywhere;
clients should send the minimum.

**Extra fields.** `Request.Extra map[string]any` and `Response.Extra map[string]any`
flatten into the top-level object on marshal and capture unknown keys on
unmarshal. Implemented in `internal/jsonx` by marshalling the struct, decoding
into a map, merging, and re-encoding. Slower, but only on the envelope, not on
every delta.

**Enums are string types with constants, never validated on decode.** A new
value from a provider must not break parsing.

**Events.** A `baseEvent{Type string; SequenceNumber int64}` embedded in all
25 concrete events. Events that carry an `Item`, `Content` part or `Annotation`
need custom `UnmarshalJSON` to go through the registries. `DecodeEvent(data)`
peeks the type, looks up a factory, falls back to `UnknownEvent`. It also
tolerates the bare `{"error": {...}}` envelope some servers emit mid-stream and
promotes it to `ErrorEvent`.

## 5. Transport design

SSE reader

- `bufio.Scanner` with a custom split func that yields one frame per blank line,
  handling both `\n\n` and `\r\n\r\n`. Raise the buffer cap (e.g. 16 MiB) because
  `output_item.done` carries whole items.
- Ignore `event:`, `id:`, `retry:` and comment lines; only `data:` matters.
  Concatenate multiple `data:` lines with `\n` per the SSE spec.
- `data: [DONE]` ends the stream cleanly. EOF without `[DONE]` and without a
  terminal response event is an error (`ErrTruncatedStream`).
- Context cancellation closes the body.

SSE writer (server)

- Set `Content-Type: text/event-stream`, `Cache-Control: no-cache`,
  `X-Accel-Buffering: no`. Write `event: <type>\ndata: <json>\n\n`, flush each frame.
- The sink assigns `sequence_number` itself so adapters never have to.
- On adapter error mid-stream: emit `error` then `response.failed`, then `[DONE]`.

HTTP client

- `Client{baseURL, apiKey, http *http.Client, headers, middleware}`; functional options.
- `Create` sets `stream=false`; `CreateStream` forces `stream=true` and
  `Accept: text/event-stream`.
- Non-2xx: read the body, decode `{"error": …}`, return `*APIError{Status, Type, Code, Message, Param, Headers, Body}`.
  Implement `Is`/`As` friendly helpers: `IsNotFound(err)`, `IsRateLimited(err)`.
- No retries in v1. Expose `WithMiddleware(func(http.RoundTripper) http.RoundTripper)` so callers add them.

HTTP server

- `Handler` implements `http.Handler`, routes `POST {prefix}/responses`,
  `POST {prefix}/responses/compact`, and WebSocket upgrade on `GET {prefix}/responses`.
- `Adapter` interface the backend implements:

  ```go
  type Adapter interface {
      Create(ctx context.Context, req Request) (*Response, error)
      CreateStream(ctx context.Context, req Request, sink EventSink) error
      Compact(ctx context.Context, req CompactRequest) (*CompactResponse, error)
  }
  ```

  Ship `UnsupportedStreaming` and `UnsupportedCompaction` embeddable structs
  that return `invalid_request` errors so small adapters stay small.
- Errors returned by adapters that implement `HTTPStatus() int` or are
  `*SpecError` map to the right envelope; anything else becomes `server_error`.
- `Validate()` on the request runs before dispatch and returns `invalid_request`
  with `param` set.

WebSocket

- Server: upgrade with `coder/websocket`, read loop decodes `response.create`,
  strips forbidden fields (or rejects with `invalid_request`), queues turns and
  runs them one at a time through `Adapter.CreateStream` with a sink that writes
  one JSON text frame per event. Per-connection `turnCache` (map from response
  id to output `Items`, last-N with N=1 by default) implements the
  `previous_response_id` continuation and eviction rules. A timer enforces the
  60-minute lifetime and sends `websocket_connection_limit_reached`.
- Client: `Dial(ctx) (*WebSocketStream, error)`; `Send(Request) error` writes a
  `response.create`; `Events() iter.Seq[StreamEvent]`; `Err()`; `Close()`.
  Per-turn helper `Turn(ctx, req) (*Response, error)` that drains until a
  terminal event and returns the final response.

## 6. Convenience layer (small, deliberate)

- `Response.OutputText() string` concatenates all `output_text` parts of assistant messages.
- `Response.FunctionCalls() []FunctionCall`.
- `Items.Append`, `Input.Add` helpers for building multi-turn context.
- `Accumulator` that folds a stream of events into a `Response` (needed by the
  WebSocket `Turn` helper and handy for clients that want both deltas and the
  final object).
- `FunctionTool` constructor taking `name, description, parameters json.RawMessage`.

Leave out: struct-to-JSON-Schema derivation, tool dispatch, conversation stores.

## 7. Testing strategy

1. **Schema conformance.** Load `testdata/openapi-2026-04-24.json`, resolve
   `#/components/schemas/<Name>`, and validate the marshalled bytes of every
   fixture against it with `santhosh-tekuri/jsonschema`. One table test per
   schema family: items, content, request, response, each event, error envelopes.
2. **Round-trip.** For every golden JSON fixture: unmarshal → marshal →
   semantic JSON equality (compare via `map[string]any`), including unknown
   slug-prefixed items, events and extra top-level fields.
3. **SSE framing.** Feed hand-written streams (CRLF and LF, multi-line data,
   comments, missing `event:` line, missing `[DONE]`, error then `response.failed`)
   through `EventStream` and assert the decoded sequence.
4. **Client against httptest.** JSON success, SSE success, 400/404/429/500
   envelopes, malformed body, context cancellation mid-stream.
5. **Handler against httptest.** A fake `Adapter` that echoes; assert headers,
   sequence numbers, `[DONE]`, error mapping, compaction, and `Validate()` rejects.
6. **WebSocket.** In-process server + client: sequential turns, continuation
   with `store:false`, `previous_response_not_found`, eviction after a failed
   continuation, lifetime expiry (inject a short lifetime).
7. **Official compliance suite.** `make compliance` runs the echo/fake adapter
   server on `:8000` and executes
   `bun run test:compliance --base-url http://localhost:8000/v1` from a checkout
   of `openresponses/openresponses`. Target: all 17 scenarios green
   (basic, streaming, system prompt, tool calling, image input, multi-turn,
   assistant phase, compaction + missing model, and the 9 WebSocket ones).
8. **Fuzz** `DecodeEvent` and `UnmarshalItem` with `go test -fuzz` seeds from the goldens.

## 8. Phased delivery

| Phase | Deliverable | Est. lines |
|---|---|---|
| 1 | `enums.go`, `items.go`, `content.go`, `tools.go`, registries, `UnknownX`, schema + round-trip tests | 1,200 |
| 2 | `request.go`, `response.go`, `errors.go`, `Extra` passthrough, `Validate()` | 600 |
| 3 | `events.go`, `sse.go`, `stream.go`, `Accumulator`, framing tests | 1,100 |
| 4 | `client.go` (Create, CreateStream, Compact, options, APIError), httptest suite | 400 |
| 5 | `handler.go` (Adapter, EventSink, SSE writer, error mapping), compliance HTTP scenarios green | 700 |
| 6 | `websocket.go` client + server + turn cache, compliance WebSocket scenarios green | 700 |
| 7 | Docs, examples (`examples/basic`, `streaming`, `tools`, `server`, `websocket`), README, tag v0.1.0 | 300 |

Phases 1 to 3 are pure types and parsing and can be validated without any
provider. Phases 4 and 5 are independent of each other once 3 is done. Phase 6
depends on 5.

## 9. Decisions still open

- **Module path.** Standalone `github.com/christopherdavenport/openresponses`
  versus an `openresponses/` package inside `dex`. The package has no dependency
  on dex, so standalone is the default unless dex is meant to stay a monorepo.
- **Server side in v1 or not.** If dex is client-only today, phases 5 and 6
  server code can be deferred, but keep the `Adapter` interface shape so the
  package does not need a breaking change later.
- **Provider presets.** OpenRouter needs attribution headers; OpenAI needs
  nothing. Decide whether `openrouter.New(...)` ships here or in dex.
- **Spec drift.** Pin the OpenAPI date in `doc.go` (`SpecVersion = "2026-04-24"`)
  and add a `make spec-update` target that fetches the latest file and re-runs
  the conformance tests to surface schema changes.

## 10. References

- Specification: https://www.openresponses.org/specification/2026-04-24
- OpenAPI + compliance CLI: https://github.com/openresponses/openresponses
- Compliance web UI: https://www.openresponses.org/compliance
- Reference Go design (proxy only): `github.com/joeychilson/openresponses@v0.0.0-20260610175401-1bc6e6763ed5`
- Background: https://huggingface.co/blog/open-responses
