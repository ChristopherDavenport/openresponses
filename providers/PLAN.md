# Provider adapters

This directory holds adapters that serve third-party model APIs through
`openresponses.Adapter`. Each provider is a nested Go module, so its SDK
stays out of the root package's dependency graph, and each lives in this
repository so a root API change and the adapter updates it forces land
in one pull request while the root is pre-1.0.

## Layout

```
providers/
  PLAN.md
  anthropic/   module github.com/ChristopherDavenport/openresponses/providers/anthropic
  gemini/      module github.com/ChristopherDavenport/openresponses/providers/gemini
```

Each module has the same shape:

- `go.mod` requires a released root version and has no `replace`
  directive. `conformance/` requires `v0.0.0` and replaces it with `../`
  because nothing imports it; a published module cannot, since
  consumers ignore `replace`.
- `New` takes the provider SDK's own client, already constructed. The
  adapter never sees a URL, a key or a credential.
- `Create` is `openresponses.CollectStream` over `CreateStream`, so
  there is one translation path.
- `openresponses.UnsupportedCompaction` is embedded until compaction has
  a design.
- Tests replay recorded provider responses from an `httptest.Server`
  through `streamtest.Run`. CI needs no network and no keys.
- A `README.md` shows client construction for each endpoint the
  provider serves. Examples live in an `examples/` directory inside the
  module, because the root module cannot import a provider without
  pulling the provider's SDK into the root graph. Each module keeps its
  own `CHANGELOG.md`, since it is versioned on its own.

## Rules every adapter follows

1. Map shapes; do not validate model capabilities. A field the provider
   rejects for a given model comes back as `invalid_request` carrying the
   provider's message. An adapter that knows which model supports which
   parameter lags every model release.
2. Endpoints and credentials belong to the client. Vertex, Bedrock and
   API keys are lines in the README that construct the SDK client; the
   adapter has no Vertex code.
3. Nothing is dropped silently. A request field with no provider
   equivalent returns `invalid_request` with `param` set. Any exception
   is listed in the adapter's README.
4. Provider extensions ride on the slug-prefixed unknown types,
   `anthropic.` and `gemini.`, per CONTRIBUTING. Server tools go in as
   `UnknownTool` and their results come out as `UnknownItem`.
5. Reasoning round-trips through `ReasoningItem.EncryptedContent`.
   Anthropic's thinking `signature` and Gemini's `thoughtSignature` both
   go there, base64 for Gemini's bytes, and come back out as the
   provider's block on the next turn. It is always emitted, since stored
   continuation depends on it being in what the handler saves.
6. `previous_response_id` is the handler's job through a
   `ResponseStore`. Without one the adapter returns
   `PreviousResponseNotFound`.
7. Streaming goes through `Emitter`. Both providers emit content blocks
   sequentially, which matches its one-open-item model.
8. `Request.Extra` is dropped by default, with an option to forward it.

## Build and release

A committed `go.work` at the repository root lists `.`, `conformance`
and each provider module as it is created. In workspace mode the
provider modules build against the local root, so an adapter can use
root API that has not been tagged yet and CI sees the combination on
the pull request. `./...` does not cross module boundaries in workspace
mode, so the Makefile loops stay; `PROVIDERS` lists the provider
modules and `SUBMODULES` includes them. `go mod tidy` runs per module
as before.

A `release-check` target runs `go vet` and `go test` in each provider
module with `GOWORK=off`, which builds what consumers get. It runs
before every provider tag.

Workspace mode rejects `-mod=mod`, so a `GOFLAGS=-mod=mod` in the
environment fails as soon as `go.work` exists. The default
`-mod=readonly` works.

Tags are `providers/anthropic/vX.Y.Z` and `providers/gemini/vX.Y.Z`,
versioned independently of the root. When a root change breaks an
adapter: tag the root, bump the adapter's `require`, run
`release-check`, tag the adapter.

CI caches each module's `go.sum`. Live tests against a real provider
are a manual target that reads a key from the environment; they never
run in CI.

## Environment variables

The library takes explicit configuration; only examples read the
environment. For Vertex the examples use Google's names,
`GOOGLE_CLOUD_PROJECT` and `GOOGLE_CLOUD_LOCATION`, because the Gemini
SDK reads exactly those itself (with `GOOGLE_GENAI_USE_VERTEXAI`
selecting the backend) and the Anthropic example can read the same two.
Keys come from `ANTHROPIC_API_KEY` and `GEMINI_API_KEY`. The Open
Responses side keeps `OPENRESPONSES_BASE_URL`, `OPENRESPONSES_API_KEY`
and `OPENRESPONSES_MODEL`.

## Anthropic

Dependency: `github.com/anthropics/anthropic-sdk-go` (v1.74.0 at the
time of writing). Its module graph includes `aws-sdk-go-v2`,
`google.golang.org/api`, `grpc` and OpenTelemetry, which is why this is
a module rather than a package.

The adapter takes an `anthropic.MessageService`, the one service it
uses. Both `anthropic.Client` and `bedrock.MantleClient` expose one as
`.Messages`, so a single constructor covers every endpoint:

```go
sdk.NewClient(option.WithAPIKey(key)).Messages                       // direct
sdk.NewClient(vertex.WithGoogleAuth(ctx, location, project)).Messages // Vertex, Application Default Credentials
sdk.NewClient(bedrock.WithLoadDefaultConfig(ctx)).Messages            // Bedrock, InvokeModel
mantle.Messages                                                       // Bedrock Mantle, from bedrock.NewMantleClient
```

The SDK's package is also named `anthropic`, so code that imports both
aliases one; the examples import the SDK as `sdk`.

`vertex.WithGoogleAuth` resolves ADC through
`google.FindDefaultCredentials` and maps `global`, `us`, `eu` and
regional hosts. Per-model regions mean one client per region; route by
`req.Model` outside the adapter.

### Request

| Open Responses | Messages |
|---|---|
| `instructions`, `system` and `developer` messages | `system` |
| consecutive same-role items | one message; every `function_call_output` after an assistant turn joins one user message of `tool_result` blocks |
| `FunctionTool` | `tools[]` with `input_schema` and `strict` |
| `tool_choice` auto / none / required / function | auto / none / any / tool |
| `parallel_tool_calls: false` | `disable_parallel_tool_use` |
| `max_output_tokens` | `max_tokens`, which is required; `WithMaxTokens` sets the default (32768) |
| `reasoning.effort` | `output_config.effort`; `minimal` becomes `low`, `none` becomes `thinking: disabled` |
| `reasoning.summary` set | `thinking: adaptive` with `display: summarized` |
| `text.format` json_schema | `output_config.format`; `json_object` is `invalid_request`, there is no schema-free mode |
| `safety_identifier` | `metadata.user_id` |
| `metadata` | accepted and echoed on the response, never sent upstream |
| `prompt_cache_key` | top-level `cache_control: ephemeral`; the client asked for caching, and the key has nothing to name |
| `service_tier` | `auto` and `default` (`standard_only`); `flex` and `priority` are `invalid_request` |
| `temperature`, `top_p` | passed through |
| `presence_penalty`, `frequency_penalty`, `top_logprobs`, `max_tool_calls`, `tool_choice` allowed_tools, `input_video` | `invalid_request` |
| `input_image.file_id`, `input_file.file_id` | Files API sources |
| `ReasoningItem` | `thinking` block from the summary text and the signature |
| `anthropic.*` items | the `block` they carry, replayed verbatim through `param.Override` |

### Response

| Messages | Open Responses |
|---|---|
| `text` | message `output_text`; citations become annotations |
| `tool_use` | `FunctionCall`; `id` becomes `call_id`, input is re-encoded as JSON |
| `thinking` | `ReasoningItem`: text as the summary, signature as `encrypted_content` |
| `redacted_thinking`, `server_tool_use`, tool result blocks, anything newer | `anthropic.<block type>` `UnknownItem` carrying the block; input deltas are folded in before it is emitted |
| `end_turn`, `tool_use`, `stop_sequence` | `Complete` |
| `max_tokens`, `model_context_window_exceeded` | `Incomplete(max_output_tokens)` |
| `refusal` | a `Refusal` content part with the explanation, then `Complete` |
| `pause_turn` | the turn's output is replayed as the assistant message and the call re-issued, up to `WithContinuations` (8); then `Complete` |
| citations | `web_search_result_location` becomes a `url_citation` spanning the text block it arrived on; other citation types are `anthropic.<type>` annotations |
| usage | `input_tokens` is input + cache read + cache creation; cache read becomes `cached_tokens`, thinking tokens `reasoning_tokens`; summed across resumed turns |

Streaming: `content_block_start` opens a `Message`, `FunctionCall` or
`Reasoning` writer; deltas feed `Text`, `Arguments`, or `Text` and
`EncryptedContent`; `content_block_stop` closes; `message_delta`
carries the stop reason and usage.

Errors: `*anthropic.Error` maps to `*openresponses.Error` by status.
`Retry-After` and the rate-limit headers are copied into
`Error.Headers`, as `Client` already does for Open Responses upstreams.

Open: whether compaction uses the server-side beta or a summarising
call. `redacted_thinking` is an extension item rather than a reasoning
item because a reasoning item with only `encrypted_content` is the
common shape of a thinking block whose display is omitted, and the two
must replay as different blocks.

## Gemini

Dependency: `google.golang.org/genai` (v1.71.0 at the time of writing).
The adapter takes a `*genai.Client`:

```go
genai.NewClient(ctx, &genai.ClientConfig{APIKey: key})                                              // Gemini API
genai.NewClient(ctx, &genai.ClientConfig{Backend: genai.BackendVertexAI, Project: p, Location: l}) // Vertex, ADC
```

### Where it differs from Anthropic

- `FunctionResponse` needs `Name`, and `function_call_output` carries
  only `call_id`. Look back through `req.Input` for the `FunctionCall`
  with that `call_id`; the handler's inlined history guarantees it is
  there.
- Function-call arguments are `map[string]any`, so `arguments` goes
  through `json.Marshal` and `json.Unmarshal`. The Gemini API often
  omits the call `ID`; mint one with `openresponses.NewID`.
- Thoughts are summaries. `Part{Thought: true}` text feeds
  `ReasoningWriter.Summary`. Gemini puts `ThoughtSignature` on the first
  part after the thoughts (the function call, in a tool turn), so the
  decoder emits a reasoning item carrying it in `encrypted_content`
  just before that part, and the encoder holds a reasoning item's
  signature until the next non-thought part of the model turn and puts
  it there, giving it a thought part of its own only when the turn
  ends without one. The replayed turn has Gemini's own layout.
- Tools use `FunctionDeclaration.ParametersJsonSchema`; `Parameters
  *Schema` is an OpenAPI subset and lossy. `strict` has no equivalent
  and is accepted without effect, the documented exception to rule 3.
  A `gemini.<name>` tool is the server tool of that name: the slug is
  stripped, `snake_case` becomes `camelCase`, and the tool's other keys
  are its configuration in the SDK's wire form, so
  `{"type": "gemini.google_search", "excludeDomains": [...]}` needs no
  per-tool code. Extension items carry a part the same way:
  `{"type": "gemini.<kind>", "id": ..., "part": <genai part>}`, and are
  replayed into the model turn on input.
- `tool_choice` auto / none / required map to
  `FunctionCallingConfig.Mode` AUTO / NONE / ANY; a named function is
  ANY with `AllowedFunctionNames`. There is no `parallel_tool_calls`
  knob.
- `reasoning.effort` minimal / low / medium / high map to
  `ThinkingLevel` one to one; `xhigh` is `invalid_request`. Any
  `reasoning.summary` sets `IncludeThoughts: true`.
- Temperature, top_p, both penalties and `top_logprobs`
  (`ResponseLogprobs` with `Logprobs`) are native. `max_output_tokens`
  is optional. `CandidateCount` is pinned to 1. `metadata` becomes
  `Labels` on Vertex only.
- Structured output is `ResponseMIMEType: "application/json"` with
  `ResponseJsonSchema`.
- Images, files and video: data URLs become `InlineData`; every other
  URL becomes `FileData` with the media type inferred from the path.
  Which references a backend can read (`gs://` on Vertex AI, Files API
  and YouTube URLs on the Gemini API) is for the backend to say, per
  rule 1; a fetcher option for plain `https://` images can come later.
  `file_id` has no lookup here and is `invalid_request`.
- Finish reasons: STOP is `Complete`; MAX_TOKENS is
  `Incomplete(max_output_tokens)`; SAFETY, RECITATION, BLOCKLIST,
  PROHIBITED_CONTENT, SPII and the IMAGE_* reasons are
  `Incomplete(content_filter)`; MALFORMED_FUNCTION_CALL,
  UNEXPECTED_TOOL_CALL and OTHER are `ModelError`. A blocked prompt
  (`PromptFeedback.BlockReason` with no candidates) is
  `Incomplete(content_filter)` with empty output.
- Usage: `PromptTokenCount` is input, `CachedContentTokenCount` is
  cached, `CandidatesTokenCount + ThoughtsTokenCount` is output,
  `ThoughtsTokenCount` is `reasoning_tokens`. `ResponseID` and
  `ModelVersion` fill the response.
- Grounding becomes `URLCitation` annotations from
  `GroundingChunks[i].Web` joined through `GroundingSupports[].Segment`.
  Gemini's segment offsets are bytes within a part and the spec's
  `start_index` is a character index within the message, so each
  segment is located by its text in the accumulated message and
  converted to character offsets; `end_index` is exclusive.
- Streaming: `GenerateContentStream` yields chunks whose parts are
  deltas for text and thoughts, but a `FunctionCall` part arrives whole
  by default. Open, write and close it in one step; the client sees one
  `function_call_arguments.delta` with the full JSON, which the spec
  allows. `FunctionCallingConfig.StreamFunctionCallArguments` and
  `PartialArgs` exist for streamed arguments and are out of scope for
  the first version. The last chunk carries `FinishReason` and
  `UsageMetadata`.
- Errors: `genai.APIError{Code, Message, Status}` maps by `Code`. It
  exposes no headers, so `Retry-After` is synthesized from a
  `google.rpc.RetryInfo` in `Details` when one is present.
  `finishMessage` reaches the SDK only on Vertex AI; the Gemini API
  converter drops it, so model errors carry a generic message there.
- Compaction is unsupported. Implicit caching is automatic; explicit
  `CachedContent` is a resource name that could ride in `Extra` later.

## Chat Completions

The third adapter is a protocol, not a provider: `chatcompletions/` in
the root module, next to `websocket/`, because it needs nothing beyond
the standard library and reuses `Client` for address, auth and
transport. It covers the long tail (DeepSeek, Mistral, Groq, Together,
Fireworks, Cerebras, xAI, Perplexity, llama.cpp, LiteLLM, Vertex's
OpenAI-compatible endpoint, Azure's legacy path) with one
implementation. The slug is `chatcompletions.`, though nothing uses it
yet: the protocol has no extension blocks to carry.

Where it differs from the SDK-backed adapters:

- Most of the request maps one to one: sampling and penalties,
  `top_logprobs`, `parallel_tool_calls`, `reasoning_effort`,
  `response_format` (both `json_object` and `json_schema`),
  `verbosity`, `service_tier`, `safety_identifier`,
  `prompt_cache_key`, `metadata` and `tool_choice` including
  `allowed_tools`. `n` is pinned to 1 and `stream_options.include_usage`
  is always on.
- Reasoning has no standard field. Decoding accepts `reasoning_content`
  (DeepSeek) and a string `reasoning` (OpenRouter, Groq) into the
  reasoning item's content; there is no signature. Replay is opt-in
  through `WithReasoningReplay(field)`, because providers disagree on
  whether reasoning may be sent back at all; without it reasoning
  items are accepted and not sent, the documented exception to rule 3,
  and `reasoning.summary` has no effect.
- `max_output_tokens` is sent as `max_tokens` unless
  `WithMaxTokensField` says `max_completion_tokens`; providers split.
- `WithExtra` forwards `Request.Extra` for provider parameters
  (`top_k`, routing), never overriding a mapped key. Off by default.
- Function outputs are one `tool` message each and text only; images in
  tool results, `input_video`, `file_url` and image `file_id` have no
  place in the protocol and are `invalid_request`. Assistant history
  folds into one assistant message per turn since tool calls are a
  field of the message that made them.
- Tool calls stream by index and are opened when the name is known
  (the first delta everywhere in practice); a call resuming after a
  later one started is an upstream error. A mid-stream `{"error": …}`
  frame fails the response. Errors keep the envelope's `code`, `type`,
  `param` and the failure headers.
- The SSE reader moved to `internal/sse` so the root package and this
  one share it.

## Shared code

With both adapters written, this is what they share. Duplicated
verbatim: `parseDataURL`, `typeByExtension`, the `unsupported` and
`invalid` error helpers, and `messageText`. Same shape on different
SDK types, so not shareable as code: turn folding, message content
encoding, function output encoding, the extension item wrapper (`part`
for Gemini, `block` for Anthropic) and the streaming decoder's
one-open-writer discipline. Provider-specific with no counterpart: the
`call_id` to name lookback and the pending-signature carrier (Gemini),
`pause_turn` continuation and block-boundary citations (Anthropic).

Nested modules cannot import a root `internal/`, so the duplicated
helpers stay duplicated until they are worth a root export.
`ParseDataURL` is the candidate: data URLs are part of the spec's own
`input_image` and `input_file`, so a root helper is justified on its
own terms. The rest is too small to export.

## Order of work

1. Wiring: `go.work`, `SUBMODULES`, `release-check`, CI cache entries,
   and a sentence in CONTRIBUTING that provider knowledge lives here and
   never in the root.
2. `providers/gemini`: module, request encoder with its
   `invalid_request` cases, streaming decoder, fixture tests, README.
   Done.
3. `providers/anthropic`: the same, plus the thinking round-trip
   through a tool-use turn and `pause_turn` continuation. Done.
4. Examples inside each module, and a manual live-test target that
   reads a key from the environment.
5. A fetcher option for `https://` images on Gemini.
6. Compaction for both, once there is a design.
7. `ParseDataURL` to the root, if a third consumer appears or the two
   copies drift.
