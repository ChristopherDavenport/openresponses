# Changelog

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
