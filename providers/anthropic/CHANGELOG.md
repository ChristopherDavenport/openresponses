# Changelog

## Unreleased

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
- The response's `service_tier` is the tier that served the request, as
  the specification defines it, read from `usage.service_tier` on
  `message_start`: `standard` is `default` and `priority` is `priority`.
  It used to echo the request, so a request for `auto`, or for no tier,
  never said whether priority capacity ran it. `batch`, which has no
  Open Responses tier, and a message without the field keep the echoed
  request tier. When a `pause_turn` is resumed, the last turn's tier is
  the response's.

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
