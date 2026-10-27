# Changelog

## Unreleased

- An upstream 408 is `server_error`, which a retry may fix, rather than
  `invalid_request`.
- `prompt_cache_key` is accepted without effect instead of being
  refused. It is a caching hint, and Gemini caches implicitly (#57).
- Behaviour change: an upstream 401 `UNAUTHENTICATED` or 403
  `PERMISSION_DENIED` is `invalid_request` with that status and the
  code `unauthenticated` or `permission_denied`. It was a 502
  `server_error`, which a client retries although no retry can fix it
  (#58).
- A request whose input exceeds the model's context window is
  `invalid_request` with the shared code `context_length_exceeded`,
  recognised from the `INVALID_ARGUMENT` message: the Gemini API's "The
  input token count (N) exceeds …" or Vertex AI's "the input token count
  is N but model only supports up to M", with N greater than M. It was
  `invalid_argument` (#61).
- An error envelope inside a stream maps as the same envelope on a
  non-2xx response does. One whose `code` is missing or a 2xx maps as
  the HTTP status its gRPC `status` stands for (`PERMISSION_DENIED`
  403, `RESOURCE_EXHAUSTED` 429, …), and one with neither takes its
  status from its type, so an error never carries a 2xx status (#54).
- Reasoning items carry `"openresponses-experimental.issuer":
  "gemini"`. On input only those go back to Gemini. A reasoning item
  another issuer wrote, or one with no issuer, is left out whole,
  summary included: Claude's signatures are base64 too, so an
  unattributed item's origin cannot be told. On Gemini 3 its step gets
  the placeholder signature instead (#56).
- Behaviour change: a system or developer message after any other
  input item stays in place as user text instead of moving into the
  system instruction. Only those that precede every other item, with
  `instructions`, form the system instruction. A host that appends a
  standing instruction at the end of the input will see it reach the
  model as part of the last user turn (#55).
- The README states the usage mapping in full, and that Gemini reports
  no cache writes, so `openresponses-experimental.cache_write_tokens` is
  never set (#53).
- `reasoning.effort: none` on a model that takes thinking levels is
  sent as `thinkingBudget: 0` instead of being refused by the adapter.
  Whether the model can stop thinking is for Gemini to say (PLAN.md
  rule 1). The README gives the model ID table the encoding is chosen
  by.
- `reasoning.effort: xhigh` is the highest setting the model takes:
  `HIGH` on a level-encoded model, and on a budget-encoded one that
  model's maximum budget from Google's table in the README (2.5 Pro
  32768, 2.5 Flash and Flash-Lite 24576; 24576 for a model the table
  does not know). It was `invalid_request`.
- `openresponses-experimental.effort` is ignored: `xhigh` already maps
  to Gemini's highest setting, so `reasoning.effort` is mapped as if
  the member were absent, and `max` gets that setting. A `reasoning` object holding only unknown members no longer
  sends an empty thinking config (#51).
- `Adapter` implements `openresponses.Describer`: `Describe` reads
  Gemini's `models.get` into the model information of docs/extensions.md
  section 7, the context window, output limit and whether the model
  thinks. On Vertex AI it returns the source alone, since that model
  resource publishes none of them (#51).
- Unknown top-level request members follow PLAN.md rule 8: one under
  `anthropic.`, `chatcompletions.` or `gemini.` (no Gemini request
  parameter is defined yet) is `invalid_request` naming the member,
  shared members are ignored, and any other is dropped. `WithExtra`
  forwards those others into the `GenerateContent` body without
  overriding a mapped key in either its snake_case or camelCase
  spelling. Off by default.
- On a model whose ID the README's table puts in the levels
  generation, whatever `WithThinking` sets, the first function call of
  a model step gets Google's documented placeholder,
  `skip_thought_signature_validator`, when it has no thought signature
  of its own, so Gemini 3 accepts a transcript switched from another
  provider.

## v0.0.16 - 2026-10-26

- Security: golang.org/x/net is v0.60.0, which fixes GO-2026-6617,
  GO-2026-6612, GO-2026-6611 and GO-2026-6603. x/net v0.60.0 needs Go
  1.26, so this module now requires Go 1.26; the root module still
  requires Go 1.25.

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
