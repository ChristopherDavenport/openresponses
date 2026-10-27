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
function calls, reasoning, `anthropic.` items) one assistant message. Within a user turn every
`tool_result` block comes first and the other blocks after them, each in
input order, as Claude requires: a developer note or user message that
arrived between two function outputs follows both results.

### Request members outside the specification

Unknown top-level members of a request (`Request.Extra`) are read by
kind, as PLAN rule 8 and docs/extensions.md section 1.5 say:

- `anthropic.<field>` is a Messages API parameter: the request is sent
  with `<field>` set to the member's value, as given, so
  `{"anthropic.top_k": 5}` sends `top_k: 5` and
  `{"anthropic.inference_geo": "us"}` sends `inference_geo: "us"`. A
  member naming a field the adapter already sets from the request
  (`anthropic.max_tokens`, `anthropic.thinking` when an effort is set,
  ...) is `invalid_request` with `code: invalid_value` and `param` the
  member's name, rather than overriding the mapping. Whether Claude
  takes the field is the API's to say.
- A member under another adapter's slug, `gemini.` or
  `chatcompletions.`, is `invalid_request` with `code:
  unsupported_parameter` and `param` the member's name: it is a setting
  for a provider this one cannot honour.
- A shared `openresponses-experimental.` member at the top level is not
  one this adapter implements, so it is ignored.
- Any other member is dropped. `WithExtra` forwards it into the request
  as it is, never overriding a field the adapter set; off by default.

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

Claude models differ in the thinking types they take, and each refuses
the others with a 400. The adapter picks the encoding from the model ID
by the table below, which follows Anthropic's per-model table in
[Troubleshooting thinking](https://platform.claude.com/docs/en/build-with-claude/thinking-troubleshooting#thinking-support-defaults-and-rejected-configurations)
and [Extended thinking](https://platform.claude.com/docs/en/build-with-claude/extended-thinking)
(as read 2026-10-27). `WithThinking` fixes the encoding.

| Models | Encoding | `none` sends |
|---|---|---|
| Claude before 4.6 (Opus 4.5, Sonnet 4.5, Haiku 4.5, Claude 4, 3.x) | `ThinkingBudget` | `thinking: disabled` |
| Opus 4.6 to 4.8, Sonnet 4.6, Opus 5, Sonnet 5, Haiku 5.5; an ID that names no version | `ThinkingAdaptive` | `thinking: disabled` |
| Sonnet 5.5 and later Sonnets | `ThinkingBetweenTools` | `thinking: between_tools` |
| Opus 5.5 and later Opuses, every Fable and Mythos | `ThinkingAlwaysOn` | nothing: `invalid_request` |

The documents say Opus 5.5, Fable 5 and 5.1, Mythos 5 and 5.1 and
Mythos Preview are "Always on" and "cannot turn thinking off": they
refuse `disabled`, and "only Claude Sonnet 5.5 accepts
`between_tools`". A request with `none` for one of them would fail, so
the adapter refuses it itself, with `invalid_request`, `param`
`reasoning.effort` and a message naming the model, instead of sending
it. That is the one place the adapter refuses a value on a model's
behalf: the API states the refusal for every such model. `low` is the
least thinking those models take.

The other efforts:

| Effort | `ThinkingBudget` | the adaptive encodings |
|---|---|---|
| `minimal`, `low` | `thinking: enabled`, `budget_tokens: 1024` | `output_config.effort: low` |
| `medium` | `budget_tokens: 4096` | `output_config.effort: medium` |
| `high` | `budget_tokens: 16384` | `output_config.effort: high` |
| `xhigh` | `budget_tokens: 24576` | `output_config.effort: xhigh` |
| `openresponses-experimental.effort: max` | `budget_tokens: 32000` | `output_config.effort: max` |
| no effort, a summary | `budget_tokens: 16384` (the API's default effort, `high`) | `thinking: adaptive`, no `output_config.effort` |

- Under the adaptive encodings, every effort except `none` also sends
  `thinking: adaptive`, with `display: summarized` when
  `reasoning.summary` is set and `display: omitted` when it is not.
  Opus 4.6 to 4.8 and Sonnet 4.6 do not think without it; with
  `omitted` the thinking text is empty and the signature still comes
  back, so the reasoning item round-trips.
- Claude refuses thinking with a forced tool or modified sampling. When
  the request has `tool_choice` `required` or a named function, a
  `temperature` other than 1, or `anthropic.top_k`, the adaptive
  encodings send `output_config.effort` alone, without `thinking`, and a
  summary has nothing to show. On Opus 4.6 to 4.8 and Sonnet 4.6 the
  model then does not think. `ThinkingBudget` has no effort without
  thinking, so it still sends the budget, and the API refuses the
  combination.
- Under `ThinkingBudget`, `output_config.effort` is not sent, and any
  summary setting leaves the display to the model. The budget must be
  below `max_tokens`: when it is not, it is lowered to `max_tokens - 1`,
  but never below the API's minimum of 1024, so a `max_output_tokens`
  of 1024 or less with an effort is the API's to refuse.
- `none` turns thinking off and ignores `reasoning.summary`. It sends no
  `output_config.effort`, so the API's limits on `disabled` and
  `between_tools` at `xhigh` and `max` do not arise.
- The shared member `openresponses-experimental.effort: max` on
  `reasoning` (docs/extensions.md, section 3) takes the place of
  `reasoning.effort`. The specification's `xhigh` is never sent as
  `max`, because Claude has both. The response does not echo the
  member.
- The model is read from the ID. The family is the first of `opus`,
  `sonnet`, `haiku`, `fable` and `mythos` after `claude-`; the version is
  the first number after `claude-`, and the one after it when that is a
  one- or two-digit minor version. So `claude-opus-4-5`,
  `claude-3-7-sonnet-20250219`, `claude-sonnet-4@20250514` (Vertex) and
  `us.anthropic.claude-haiku-4-5-20251001-v1:0` (Bedrock) all read. A
  Fable or Mythos ID is `ThinkingAlwaysOn` whatever its version. An ID
  with no version after `claude-`, such as an inference profile ARN, is
  `ThinkingAdaptive`, the encoding most current models take.

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

Cache writes are reported in `input_tokens_details` as the shared
members of docs/extensions.md section 5, and stay counted in
`input_tokens`. `openresponses-experimental.cache_write_tokens` is
`cache_creation_input_tokens`, present when it is above zero.
`openresponses-experimental.cache_write_tokens_by_ttl` maps
`cache_creation.ephemeral_5m_input_tokens` to `5m` and
`ephemeral_1h_input_tokens` to `1h`, leaving out an entry of zero. It is
present only when `message_start` carries `cache_creation` and its two
counts sum to `cache_creation_input_tokens`; `message_delta` has no
split, so the one from `message_start` stands for the turn while it
still sums. Across resumed turns both are summed, and the split is
dropped when a turn with writes had none.

```json
"input_tokens_details": {
  "cached_tokens": 4,
  "openresponses-experimental.cache_write_tokens": 2100,
  "openresponses-experimental.cache_write_tokens_by_ttl": {"5m": 100, "1h": 2000}
}
```

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
since a retry cannot fix it. A body without Claude's error envelope, as
Vertex AI (`RESOURCE_EXHAUSTED`), Bedrock and proxies send, or with an
error type not in the table, takes its type from the HTTP status: 404
`not_found`, 408 `server_error`, 429 `too_many_requests`, any other 4xx
`invalid_request`, and `server_error` for the rest, keeping the status
and with code `api_error` when there is no type. Inside a stream,
whose status is 200, an unknown type is `server_error` with no status
of its own, so it reads as 500. A 2xx status is never put on an error.

A context overflow is `invalid_request` with code
`context_length_exceeded` (docs/extensions.md, section 6). Claude has no
code of its own for it: the adapter recognises an
`invalid_request_error` whose message contains `prompt is too long`,
ignoring case, and keeps Claude's message. Output cut short by
`max_tokens` or `model_context_window_exceeded` is not an overflow; that
response ends incomplete, as above.

`Retry-After`, the rate-limit headers and the request id travel with
every error.

## Model information

With `WithModels(client.Models)`, the adapter implements
`openresponses.Describer`: `Describe` reads `GET /v1/models/{model}` and
returns the shared model information of docs/extensions.md section 7,
with `source: "anthropic"`. `context_window` is `max_input_tokens` and
`max_output_tokens` is `max_tokens`, each when above zero; `reasoning`
is `capabilities.thinking.supported`; when
`capabilities.effort.supported` is true, `efforts` is `none` followed by
each of `low`, `medium`, `high`, `xhigh` and `max` whose
`capabilities.effort.<level>.supported` is true. Claude does not say a
default effort or input modalities, so those are left out, and nothing
is filled from a table of models. A model Claude does not know is
`not_found`. Without `WithModels`, which a Vertex AI client has no
endpoint for, `Describe` returns `not_found` for every model, and a
client sends its requests as asked.

## Versioning

The module is tagged `providers/anthropic/vX.Y.Z`, independently of the
root library, and [CHANGELOG.md](CHANGELOG.md) records its changes.
