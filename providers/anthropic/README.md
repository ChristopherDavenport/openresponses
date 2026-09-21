# anthropic

An [openresponses](https://github.com/ChristopherDavenport/openresponses)
adapter for Claude, over the
[anthropic-sdk-go](https://pkg.go.dev/github.com/anthropics/anthropic-sdk-go)
Messages API. It is a separate module so the SDK stays out of the root
library's dependency graph:

```sh
go get github.com/ChristopherDavenport/openresponses/providers/anthropic
```

Requests map onto one Messages call and its stream maps back onto Open
Responses items. Compaction is not supported. `providers/PLAN.md` at
the root of the repository has the full mapping.

## Client construction

The adapter takes the SDK's `MessageService` and knows nothing about
endpoints or credentials. Every client the SDK builds exposes the
service as its `Messages` field, so the Claude API, Vertex AI and
Bedrock differ only in how the client is built:

```go
import (
    sdk "github.com/anthropics/anthropic-sdk-go"
    "github.com/anthropics/anthropic-sdk-go/bedrock"
    "github.com/anthropics/anthropic-sdk-go/option"
    "github.com/anthropics/anthropic-sdk-go/vertex"
)

messages := sdk.NewClient(option.WithAPIKey(key)).Messages                       // Claude API
messages := sdk.NewClient(vertex.WithGoogleAuth(ctx, location, project)).Messages // Vertex AI
messages := sdk.NewClient(bedrock.WithLoadDefaultConfig(ctx)).Messages            // Bedrock

http.Handle("/v1/", openresponses.NewHandler(anthropic.New(messages)))
```

The Vertex form uses Application Default Credentials
(`gcloud auth application-default login`, `GOOGLE_APPLICATION_CREDENTIALS`,
or the metadata server) and takes `global`, `us`, `eu` or a region as
the location. A `bedrock.MantleClient` works the same way through its
`Messages` field. The SDK's package is also named `anthropic`, so code
that imports both aliases one.

`WithMaxTokens` sets the `max_tokens` sent when a request has no
`max_output_tokens` (the Messages API requires it; the default is
32768). `WithContinuations` sets how many times a turn Claude pauses
for a long-running server tool is resumed before the response
completes with what it has (default 8).

## What the request mapping rejects and ignores

A request field the Messages API has no equivalent for comes back as an
`invalid_request` error with `code: unsupported_parameter` and `param`
naming the field, rather than being dropped: `presence_penalty`,
`frequency_penalty`, `top_logprobs`, `max_tool_calls`, `truncation:
auto`, `text.verbosity`, `text.format` of type `json_object` (there is
no schema-free JSON mode), `service_tier` `flex` and `priority`,
`tool_choice` of type `allowed_tools`, `input_video`, `compaction` and
`item_reference` items, and tools and items from other providers'
slugs. `previous_response_id` reaching the adapter means the handler
has no `ResponseStore`, and is `previous_response_not_found`.

Accepted without effect: `metadata` (echoed on the response, never sent
upstream), `input_image.detail`, `include`, `stream_options` and
`store` (the handler's concerns). `safety_identifier` becomes
`metadata.user_id`. `prompt_cache_key` turns on prompt caching for the
request: Claude caches on request, and the key itself has nothing to
name. `reasoning.effort` maps onto `output_config.effort`, with
`minimal` sent as `low` and `none` disabling thinking; any
`reasoning.summary` asks for summarized thinking.

Extension types use the `anthropic.` slug. A tool `{"type":
"anthropic.web_search_20260209", "name": "web_search"}` is that server
tool, sent as written with the slug removed, so every tool the API
offers is reachable without adapter changes.

## Responses

Content blocks become items in order: text blocks open or extend an
assistant message, thinking blocks are reasoning items, `tool_use`
blocks are `function_call` items streamed argument by argument, and
every other block (`redacted_thinking`, `server_tool_use`, the tool
result blocks) becomes an `anthropic.<block type>` item whose `block`
key is the block as received. Send those items back in `input` and the
block is replayed unchanged.

Thinking round-trips through `reasoning` items: the thinking text is
the item's summary and the signature is `encrypted_content`, and
encoding the item back produces the same thinking block. Keep
reasoning items in the input you replay; Claude needs the signature
on the next tool-use turn.

`end_turn`, `tool_use` and `stop_sequence` complete the response.
`max_tokens` and `model_context_window_exceeded` end it incomplete
with `max_output_tokens`. `refusal` adds a `refusal` content part
carrying the explanation and completes. `pause_turn` resumes the turn
with its own output replayed, up to the configured continuations.
Web search citations become `url_citation` annotations spanning the
cited text block; document citations pass through as
`anthropic.<citation type>` annotations. Usage counts cached and
cache-creation tokens in `input_tokens`, reports cache reads in
`cached_tokens` and thinking in `reasoning_tokens`, and sums across
resumed turns; `model` on the response is the version Claude served.

Upstream errors keep their HTTP status and the API's error type as the
code; `Retry-After`, the rate-limit headers and the request id travel
with them.

## Versioning

The module is tagged `providers/anthropic/vX.Y.Z`, independently of the
root library, and [CHANGELOG.md](CHANGELOG.md) records its changes.
