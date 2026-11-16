# Changelog

## v0.0.22 - 2026-11-16

- Behaviour change: a response Gemini cuts with finish reason
  `CONTINUATION` is continued by the adapter (#84). It sends the same
  request again with the continuation token, and the next request's
  output goes on in the same response, extending the item the cut left
  open, with one `response.created`, one terminal event and output
  indices running on; usage is summed over the requests.
  `WithContinuations` sets how many times (default
  `DefaultContinuations`, 8; zero or less never continues, as in the
  Anthropic adapter). The token is the finishing chunk's or the latest
  checkpoint token before it, and a request that ends on the token it
  was sent with is not sent again. `max_output_tokens` bounds the
  whole response: Gemini counts it across the continued requests, so
  each request sends it unchanged, and once the output has spent it
  the response ends incomplete with `max_output_tokens` without
  another request. A cut that is not continued, at the limit or without a
  token, ends the response incomplete with `max_output_tokens` and
  `incomplete_details["gemini.finish_reason"]: "CONTINUATION"`, the
  open item marked incomplete. Before, the finish failed the response
  with `model_error`, code `continuation`, after one request.
- Dependencies: golang.org/x/net is v0.62.0 (indirect, was v0.60.0)
  and golang.org/x/crypto v0.59.0 (indirect, was v0.57.0), with
  golang.org/x/sys v0.50.0 and golang.org/x/text v0.44.0 following.
  govulncheck reported GO-2026-6866 and GO-2026-6860 (x/net html) and
  GO-2026-5932 (x/crypto openpgp) in the required modules; none was
  reachable from the adapter, and none is reported now. The module
  stays on Go 1.26.

## v0.0.21 - 2026-11-15

- Behaviour change: logprobs are requested from Gemini
  (`ResponseLogprobs`) only when the request's `include` names
  `message.output_text.logprobs`, which is what the specification says
  asks for them; `top_logprobs` then sets `Logprobs`, how many
  alternatives each token carries. Before, the include was ignored and
  `top_logprobs` alone turned logprobs on. This changes behaviour for a
  caller that sets only `top_logprobs`: it now gets no logprobs, and has
  to add the include to keep them; the value is still echoed on the
  response. A request with the include and no `top_logprobs` now gets
  logprobs without alternatives, where before it got none.
- The response's `service_tier` is the tier that served the request, as
  the specification defines it, read from the usage metadata's
  `trafficType`: `ON_DEMAND` is `default`, `ON_DEMAND_PRIORITY` is
  `priority` and `ON_DEMAND_FLEX` is `flex`, the inverse of the request
  mapping. It used to echo the request, so a request for `auto`, or for
  no tier, never said which tier ran it. Vertex AI reports the traffic
  type and the Gemini API does not; without one, and for
  `ON_DEMAND_OFFPEAK` and `PROVISIONED_THROUGHPUT`, which have no Open
  Responses tier, the response keeps the echoed request tier.
- Behaviour change, dependencies: google.golang.org/genai is v1.77.0
  (was v1.72.0), and the go.opentelemetry.io/otel modules are v1.47.0
  (indirect, was v1.44.0), adding go.opentelemetry.io/otel/log; the
  module stays on Go 1.26. From v1.74 the SDK
  continues a response that ends with finish reason `CONTINUATION` on
  its own, by default: it sends the request again with the continuation
  token until the model finishes, with no bound on the number of
  requests, each billed. The adapter turns that off
  (`AutomaticContinuation: false`), so a `CONTINUATION` finish still
  fails the response with `model_error`, code `continuation`, after one
  request, as before; a bounded loop of the adapter's own, like the
  Anthropic adapter's `WithContinuations`, is left for later. With it
  off, the SDK does not resume a dropped stream from a checkpoint
  either. From v1.76, an error event inside a stream and a dropped
  connection surface as errors, which the adapter maps as it maps any
  upstream error.

## v0.0.19 - 2026-11-04

- Behaviour change: a 402 is `invalid_request` with code
  `insufficient_quota` (docs/extensions.md section 6, #73), with its
  status and message kept; the code was `gemini_error` or Gemini's
  status. `RESOURCE_EXHAUSTED` stays `too_many_requests`.

## v0.0.18 - 2026-11-02

- Behaviour change: an input item or content part under another
  provider's slug (`anthropic.`, `chatcompletions.`) is left out of the
  request, wherever it sits, instead of being `invalid_request`
  (docs/extensions.md section 1.6, agentturn#285). A Claude history with
  `anthropic.redacted_thinking` or server tool blocks now reaches Gemini.
  A message left with no content is not sent, and a function output is
  sent without the part. Tools and request members under those slugs
  are still refused.
- An image, file or video whose `image_url`, `file_url`, `file_data` or
  `video_url` is a `sidecar:` reference is `invalid_request` with
  `code: invalid_value` naming the field (section 1.7, agentturn#284).
  The URL forms were sent to Gemini as a file URI.

## v0.0.17 - 2026-10-27

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
