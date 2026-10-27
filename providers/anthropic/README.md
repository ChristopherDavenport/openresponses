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
`WithCacheTTL` sets how long the prompt cache keeps what a request
writes, for requests that carry `prompt_cache_key`. Unset, the API's
five minutes apply; `sdk.CacheControlEphemeralTTLTTL1h` keeps it for
an hour. A one-hour write costs twice the base input price, against
1.25 times for five minutes, so pick it when requests sharing a prefix
arrive 5 to 60 minutes apart.

## Request

`instructions`, then the system and developer messages that come before
every other input item, in order, form `system`. A system or developer
message after any other item stays where it is, as text blocks of a user
turn with its text unchanged, joining the user message around it: a
note after tool results follows them in the same user message. A later
system message is therefore never moved into `system`, which is the
front of the prefix the prompt cache matches. Claude's mid-conversation
system message is not used.

Claude holds a `tool_use` id to `^[a-zA-Z0-9_-]+$`, and a `call_id`
from another server may fall outside it (`functions.read:0`). Such a
`call_id` is sent as `call_` followed by the first 24 hex digits of the
SHA-256 of the `call_id`'s UTF-8 bytes, on the `tool_use` and on the
`tool_result` that answers it, so the two still pair; `functions.read:0`
becomes `call_c216cab2de2e56d547b4e634`. A `call_id` the pattern accepts
is sent unchanged. The rewrite holds within the request only: response
items carry the ids Claude issued. The other items fold into alternating
messages: every user-side item (user messages, function outputs) joins
one user message and every assistant-side item (assistant messages,
function calls, reasoning, `anthropic.` items) one assistant message.

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
name; `WithCacheTTL` sets how long the cache keeps it.
`reasoning.effort` and `reasoning.summary` map as the next section
says.

## Reasoning effort

Claude has taken three encodings of a reasoning effort, one per model
generation, and each generation refuses the others. The adapter picks
the encoding from the model ID and never refuses an effort on a model's
behalf: a value the model does not take comes back as the API's
`invalid_request`.

| Effort | Claude before 4.6 (`ThinkingBudget`) | 4.6 up to 5.5 (`ThinkingAdaptive`) | 5.5 and later (`ThinkingBetweenTools`) |
|---|---|---|---|
| `none` | `thinking: disabled` | `thinking: disabled` | `thinking: between_tools` |
| `minimal`, `low` | `thinking: enabled`, `budget_tokens: 1024` | `output_config.effort: low` | as 4.6 |
| `medium` | `budget_tokens: 4096` | `output_config.effort: medium` | as 4.6 |
| `high` | `budget_tokens: 16384` | `output_config.effort: high` | as 4.6 |
| `xhigh` | `budget_tokens: 24576` | `output_config.effort: xhigh` | as 4.6 |
| `openresponses-experimental.effort: max` | `budget_tokens: 32000` | `output_config.effort: max` | as 4.6 |
| no effort, a summary | `budget_tokens: 16384` (the API's default effort, `high`) | `thinking: adaptive`, no `output_config.effort` | as 4.6 |

- Under the adaptive encodings, every effort except `none` also sends
  `thinking: adaptive`, with `display: summarized` when
  `reasoning.summary` is set and `display: omitted` when it is not.
  Claude 4.6 to 4.8 do not think without it; with `omitted` the thinking
  text is empty and the signature still comes back, so the reasoning
  item round-trips.
- Under `ThinkingBudget`, `output_config.effort` is not sent, and any
  summary setting leaves the display to the model. The budget must be
  below `max_tokens`: when it is not, it is lowered to `max_tokens - 1`,
  but never below the API's minimum of 1024, so a `max_output_tokens`
  of 1024 or less with an effort is the API's to refuse.
- `none` turns thinking off and ignores `reasoning.summary`.
- The shared member `openresponses-experimental.effort: max` on
  `reasoning` (docs/extensions.md, section 3) takes the place of
  `reasoning.effort`. The specification's `xhigh` is never sent as
  `max`, because Claude has both. The response does not echo the
  member.
- The generation is read from the model ID: the first number after
  `claude-`, and the one after it when that is a one- or two-digit minor
  version, so `claude-opus-4-5`, `claude-3-7-sonnet-20250219`,
  `claude-sonnet-4@20250514` (Vertex) and
  `us.anthropic.claude-haiku-4-5-20251001-v1:0` (Bedrock) all read. An
  ID with no `claude-` and a number after it, such as an inference
  profile ARN, is taken to be the newest generation. `WithThinking`
  fixes the encoding for such an ID.

Extension types use the `anthropic.` slug. A tool `{"type":
"anthropic.web_search_20260209", "name": "web_search"}` is that server
tool, sent as written with the slug removed, so every tool the API
offers is reachable without adapter changes.

A content part `{"type": "anthropic.<block type>", "block": <block>}`
is the Claude block it carries, for a block that has no Open Responses
part, such as the `tool_reference` blocks of a tool search result. The
adapter sends the block as given wherever it sends content: in a
function call output's `tool_result` and in a user or assistant
message. Whether the Messages API takes that block type there is the
API's to say. A part without a JSON `block` is `invalid_request`, and
one in a leading system or developer message, which becomes the text
of `system`, is refused with `unsupported_parameter`, as are content
parts under any other slug.

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

Each reasoning item the adapter emits carries
`"openresponses-experimental.issuer": "anthropic"` (docs/extensions.md,
section 4), from its `output_item.added` event on. On input, a
reasoning item goes back as a thinking block only when its issuer is
exactly `anthropic` or it has none, as items recorded before the member
existed do, and its `encrypted_content` is not empty. Any other item is
left out of the request, the documented exception to dropping nothing:
another issuer's signature, or an empty one, is a 400 from Claude, and
the adapter does not send the item's summary as text instead. Claude's
signatures and the base64 the Gemini adapter writes cannot be told
apart, so an item with no issuer is sent as Claude's.

`end_turn`, `tool_use` and `stop_sequence` complete the response.
`max_tokens` and `model_context_window_exceeded` end it incomplete
with `max_output_tokens`. `refusal`, a stop Claude's safety classifiers
make, ends it incomplete with `content_filter`, as the Gemini adapter
ends a safety stop. Claude's `stop_details.explanation`, when it sends
one, becomes a `refusal` content part of the assistant message, after
any text already streamed; when it sends none there is no refusal part,
since the adapter writes no refusal text of its own. The category
(`cyber`, `bio`, ...) is the member `anthropic.refusal_category` of
`incomplete_details`, present when Claude names one:

```json
"incomplete_details": {"reason": "content_filter", "anthropic.refusal_category": "cyber"}
```

Categories differ by provider, so the member is under this adapter's
slug rather than a shared one. `pause_turn` resumes the turn
with its own output replayed, up to the configured continuations.
Web search citations become `url_citation` annotations spanning the
cited text block; document citations pass through as
`anthropic.<citation type>` annotations. Usage counts cached and
cache-creation tokens in `input_tokens`, reports cache reads in
`cached_tokens` and thinking in `reasoning_tokens`, and sums across
resumed turns; `model` on the response is the version Claude served.

## Errors

An upstream failure keeps the API's error type as the code, and the
type decides the Open Responses type and status, whether the failure
came as an HTTP status or as an `error` event inside a 200 stream:

| Claude error type | Type | Status |
|---|---|---|
| `invalid_request_error`, `billing_error`, `request_too_large` | `invalid_request` | 400, 402, 413 |
| `authentication_error`, `permission_error` | `invalid_request` | 401, 403 |
| `not_found_error` | `not_found` | 404 |
| `rate_limit_error` | `too_many_requests` | 429 |
| `api_error`, `timeout_error` | `server_error` | 500, 504 |
| `overloaded_error` | `server_error` | 529 |

A refusal of the adapter's own key or permissions is `invalid_request`,
since a retry cannot fix it. An error type not in the table takes its
type from the HTTP status (404, 429, other 4xx, and `server_error` for
the rest), and inside a stream, whose status is 200, it is
`server_error` with no status of its own, so it reads as 500. A 2xx
status is never put on an error.

A context overflow is `invalid_request` with code
`context_length_exceeded` (docs/extensions.md, section 6). Claude has no
code of its own for it: the adapter recognises an
`invalid_request_error` whose message contains `prompt is too long`,
ignoring case, and keeps Claude's message. Output cut short by
`max_tokens` or `model_context_window_exceeded` is not an overflow; that
response ends incomplete, as above.

`Retry-After`, the rate-limit headers and the request id travel with
every error.

## Versioning

The module is tagged `providers/anthropic/vX.Y.Z`, independently of the
root library, and [CHANGELOG.md](CHANGELOG.md) records its changes.
