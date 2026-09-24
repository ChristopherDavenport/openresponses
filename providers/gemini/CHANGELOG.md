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

- `New`, `Adapter`, `Create` over `CreateStream`; compaction is
  unsupported.
- Response streaming: text, thoughts, function calls, extension parts,
  grounding citations, logprobs, usage, finish reasons and upstream
  errors, with thought signatures carried on reasoning items so a
  conversation replays in Gemini's own layout. An upstream 401 or 403
  is the adapter's own credentials, so it becomes a `server_error`
  with a 502 rather than being passed through to the caller.
- Request encoding: instructions and system messages into the system
  instruction, items folded into alternating turns, function calls and
  outputs, reasoning items with thought signatures, inline and
  referenced media, function tools, `gemini.*` server tools, tool
  choice, sampling and structured output. Fields with no Gemini
  equivalent are rejected with `unsupported_parameter`.
- `reasoning.effort` is sent as `thinkingLevel` on Gemini 3 and later
  and as `thinkingBudget` on Gemini 2.5 and earlier, chosen from the
  model ID, since each generation rejects the other field.
  `WithThinking` fixes the encoding for a model ID that does not name
  its generation.
