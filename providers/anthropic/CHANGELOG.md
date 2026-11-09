# Changelog

## Unreleased

- Behaviour change: an assistant message's `refusal` parts are no
  longer sent back to Claude, and an assistant message with nothing else
  is not sent (#77). The part renders a refusal's
  `stop_details.explanation`, which a Messages request has no form for;
  Anthropic's guidance is to discard a refused response's output; and
  replayed as text after a `tool_use`, it made Claude refuse a later
  request with a 400 about assistant prefill, though the request ended
  on a user turn. Refusal parts any provider wrote are left out, so
  `[user, refusal-only assistant]`, refused as prefill before, is sent
  as the user turn alone. The caller's input keeps the part.
- Behaviour change: an input that would end the Messages request on an
  assistant turn is refused before sending, as `invalid_request` with
  code `invalid_value` and `param` naming the item it would end on
  (docs/extensions.md section 1.8, #77). Claude reads such a request as
  a prefix to continue (prefill), which Anthropic documents models from
  Claude 4.6 on refuse with a 400, where Open Responses reads a
  trailing assistant item as context.
  Both shapes are refused: an assistant message, function call, sent
  reasoning or other `anthropic.` item last, and an assistant turn left last
  because every item after it sends Claude nothing. A paused turn is
  not refused: the adapter's own `pause_turn` continuation is unchanged,
  and an input whose last sent item is a server-side tool block
  (`anthropic.server_tool_use`, `anthropic.mcp_tool_use` or any
  `anthropic.<kind>_tool_result`), as a caller resuming a paused turn
  sends, is sent as before. A paused turn that ends on text or thinking
  cannot be told from a prefix and is refused.

## v0.0.19 - 2026-11-04

- Behaviour change: a low credit balance or a reached usage limit is
  `invalid_request` with code `insufficient_quota` (docs/extensions.md
  section 6, #73), whether Claude says it as a 400
  `invalid_request_error` ("Your credit balance is too low…", "…API
  usage limits…", matched ignoring case) or as `billing_error`, and so is
  a 402 with no Claude error type; the code was `invalid_request_error`,
  `billing_error` or `api_error`. Claude's status and message are kept.

## v0.0.18 - 2026-11-02

- Behaviour change: an input item or content part under another
  provider's slug (`gemini.`, `chatcompletions.`) is left out of the
  request, wherever it sits, instead of being `invalid_request`
  (docs/extensions.md section 1.6, agentturn#285). A message left with no
  content is not sent, and a function output is sent without the part.
  Tools and request members under those slugs are still refused.
- An image or file whose `image_url`, `file_url` or `file_data` is a
  `sidecar:` reference is `invalid_request` with `code: invalid_value`
  naming the field (section 1.7, agentturn#284). The URL forms were sent
  to Claude as a URL source to fetch.

## v0.0.17 - 2026-10-27

- `reasoning.effort` is encoded the way the model takes it, picked from
  the model ID by the table in the README, and `WithThinking` fixes the
  encoding for an ID that does not name its model (#32, #33). Claude
  before 4.6 is sent `thinking: enabled` with a `budget_tokens` per
  effort instead of adaptive thinking, which it refused at every effort.
  Sonnet 5.5 is sent `thinking: between_tools` for `none` instead of
  `disabled`, which it refuses. Opus 5.5, Fable and Mythos cannot turn
  thinking off, so `none` for them is `invalid_request` naming the model
  instead of a request the API refuses.
- An effort now also sends `thinking: adaptive` on Claude 4.6 and later,
  with `display: omitted` when no summary is requested. Before, an
  effort without `reasoning.summary` sent only `output_config.effort`,
  and Claude 4.6 to 4.8 did not think at all.
  Claude refuses thinking with a forced tool, a temperature other than
  1, a `top_p` below 0.95 or `anthropic.top_k`. With one of those, an
  effort is sent alone on Claude 4.6 and later, and is `invalid_request`
  naming the parameter before 4.6, where an effort is only a thinking
  budget; a `reasoning.summary` is `invalid_request`, since there is no
  thinking to summarise. The README has the table.
- The shared member `openresponses-experimental.effort: max` on
  `reasoning` is sent as `output_config.effort: max` (#51). `xhigh`
  stays `xhigh`.
- An `error` event inside a stream maps as the same error type does as
  an HTTP status, by the table in the README, and never keeps the
  stream's 200 (#54). An `overloaded_error` mid-stream was a
  `server_error` with status 200, which a retry policy deciding by
  status gave up on; it is now 529, as on a non-streamed response.
  An error body without Claude's envelope (Vertex AI, Bedrock, a proxy)
  still takes its type from the HTTP status.
- A context overflow, Claude's `invalid_request_error` saying "prompt is
  too long", carries the code `context_length_exceeded` (#61).
- Reasoning items carry `openresponses-experimental.issuer: anthropic`,
  and on input a reasoning item another issuer stamped, or one with no
  `encrypted_content`, is left out instead of being sent as a thinking
  block Claude refuses (#56). Items with no issuer are still sent, so
  sessions recorded before keep working.
- Only the system and developer messages before every other input item
  go to `system` (#55). A later one stays where it is, as user text,
  instead of being moved into the system prompt, which broke Claude's
  prompt cache for the rest of the conversation. This changes behaviour
  for a host that appends a standing instruction at the end of the
  input: it is now read as part of the turn, not as system prompt; put
  it in `instructions` or at the front.
  Within a user turn every `tool_result` comes first and the text
  after, so a note between two function outputs no longer splits the
  results, which Claude refused.
- A `call_id` outside Claude's `tool_use` id pattern, such as
  `functions.read:0` from an OpenAI-compatible server, is sent as `call_`
  and the first 24 hex digits of its SHA-256, the same on the call and
  its output (#60). Claude refused it with a 400.
- A content part `{"type": "anthropic.<block type>", "block": <block>}`
  is sent as the block it carries, in a function call output and in
  message content (#63). It was refused, so a session holding a
  ToolSearch result's `tool_reference` blocks could not be sent again.
- A `refusal` stop ends the response incomplete with `content_filter`
  instead of completing it, and its category is
  `incomplete_details["anthropic.refusal_category"]` (#59). Claude's
  explanation is still the refusal part. When Claude sends none, the
  adapter no longer writes a refusal of its own ("The request was
  refused (cyber).").
- Cache writes are reported as
  `openresponses-experimental.cache_write_tokens` with the split by TTL,
  `openresponses-experimental.cache_write_tokens_by_ttl`, in
  `input_tokens_details`, still counted in `input_tokens` (#53). A price
  table can now charge Claude's 5-minute and 1-hour write rates instead
  of the input rate.
- Unknown top-level request members are read by kind (PLAN rule 8):
  `anthropic.<field>` sends the Messages API field `<field>`, a
  `gemini.` or `chatcompletions.` member is `invalid_request` with
  `param` set, and anything else is dropped unless the new `WithExtra`
  forwards it. They were all dropped silently.
- `WithModels` and `Describe`: the adapter implements
  `openresponses.Describer` from Claude's models endpoint, mapped as
  docs/extensions.md section 7 says (#51).

## v0.0.16 - 2026-10-26

- `WithCacheTTL` sets the time-to-live of the prompt cache for requests
  that carry `prompt_cache_key`, sent as `cache_control.ttl`. Unset, the
  request encodes as before and the API keeps its five-minute default;
  `sdk.CacheControlEphemeralTTLTTL1h` keeps the prefix for an hour. A
  one-hour write costs twice the base input price against 1.25 times
  for five minutes, so it is opt-in. A request without
  `prompt_cache_key` still sends no `cache_control`.
- A `tool_use` or `thinking` block that `max_tokens` or
  `model_context_window_exceeded` cut off now ends `incomplete`. Claude
  sends a block's `content_block_stop` before the `message_delta` that
  carries the stop reason, and the adapter closed the item at
  `content_block_stop`, so `output_item.done` reported a function call
  with partial, unparseable arguments, or a truncated thinking block, as
  `completed`. Those items now close when the next block starts or the
  response ends, so the one the limit cut off is still open when the
  response ends incomplete. The events a consumer sees, and their order,
  are unchanged; only the status on the cut-off item differs.

## v0.0.12 - 2026-09-23

- Requires the root at the version this module is released at, rather
  than at the previous release, and carries a `replace` of the root
  pointing at the tree. Taking this module alone now resolves the root
  commit it was built and tested against.

## v0.0.11 - 2026-09-23

- Retracts v0.0.1. That tag was cut from a commit whose `go.mod` still
  required `openresponses` v0.0.9, so anything selecting it pulled the
  root module backwards from v0.0.10. The code in v0.0.1 is otherwise
  the code in v0.0.10; only the root requirement differed. Nothing in
  this adapter changed, so v0.0.10 and v0.0.11 are the same adapter.

## v0.0.10 - 2026-09-21

- `New` over the SDK's `MessageService`, `Create` over `CreateStream`,
  `WithMaxTokens` and `WithContinuations`; compaction is unsupported.
- Request encoding: instructions and system messages into the system
  prompt, items folded into alternating messages, tool use and results,
  thinking blocks with signatures, images and documents including Files
  API ids, function tools, `anthropic.*` server tools, tool choice,
  sampling, effort and thinking display, structured output and prompt
  caching. Fields with no Messages API equivalent are rejected with
  `unsupported_parameter`.
- Response streaming: text, thinking, tool use, extension blocks,
  citations, usage, stop reasons including refusals and resumed
  `pause_turn` turns, and upstream errors with their headers.
