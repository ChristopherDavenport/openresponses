# gemini

An [openresponses](https://github.com/ChristopherDavenport/openresponses)
adapter for Google's Gemini models, over the
[google.golang.org/genai](https://pkg.go.dev/google.golang.org/genai)
SDK. It is a separate module so the SDK stays out of the root
library's dependency graph:

```sh
go get github.com/ChristopherDavenport/openresponses/providers/gemini
```

Requests map onto one `GenerateContent` call and its stream maps back
onto Open Responses items. Compaction is not supported.
`providers/PLAN.md` at the root of the repository has the full mapping.

## Client construction

The adapter takes a constructed `*genai.Client` and knows nothing about
endpoints or credentials. The Gemini API and Vertex AI differ only in
how the client is built:

```go
client, err := genai.NewClient(ctx, &genai.ClientConfig{APIKey: key})                                              // Gemini API
client, err := genai.NewClient(ctx, &genai.ClientConfig{Backend: genai.BackendVertexAI, Project: p, Location: l}) // Vertex AI

http.Handle("/v1/", openresponses.NewHandler(gemini.New(client)))
```

The Vertex form uses Application Default Credentials
(`gcloud auth application-default login`, `GOOGLE_APPLICATION_CREDENTIALS`,
or the metadata server). The SDK reads `GOOGLE_API_KEY` or
`GEMINI_API_KEY`, `GOOGLE_CLOUD_PROJECT`, `GOOGLE_CLOUD_LOCATION` and
`GOOGLE_GENAI_USE_VERTEXAI` for any field left empty, so
`genai.NewClient(ctx, &genai.ClientConfig{})` works from the environment
alone.

## Reasoning effort

`reasoning.effort` reaches Gemini as one of two mutually exclusive
fields, and sending the wrong one is a 400. Gemini 3 and later take
`thinkingLevel`; Gemini 2.5 and earlier reject it and take
`thinkingBudget`, in tokens. The adapter reads which from the model ID,
taking the part after its last `/` so `publishers/google/models/…`
names read the same:

| model ID | encoding |
|---|---|
| starts with `gemini-1.` or `gemini-2.` | `thinkingBudget` |
| anything else, including an ID that names no generation | `thinkingLevel` |

An ID the table does not recognise gets the newest encoding, levels.
Each effort maps as follows:

| effort | `thinkingLevel` | `thinkingBudget` |
|---|---|---|
| `none` | `thinkingBudget: 0` | 0 |
| `minimal` | `MINIMAL` | 512 |
| `low` | `LOW` | 4096 |
| `medium` | `MEDIUM` | 8192 |
| `high` | `HIGH` | 24576 |

`thinkingLevel` has no value for `none`, and the generations that take
levels still accept a budget, so `none` is a zero budget in both
encodings. A model that cannot stop thinking refuses it upstream, and
the request fails with Gemini's message as `invalid_request`; the
adapter does not decide that on the model's behalf. The budgets are the
widest values legal across the 2.5 family, which Google documents
neither a mapping nor per-model ceilings for. `xhigh` is
`invalid_request` in both: neither field has a value above `high`.

`openresponses-experimental.effort` in `reasoning` (docs/extensions.md
section 3) is ignored, as a shared member this adapter does not
implement: Gemini has no setting above `high`. The adapter maps
`reasoning.effort` as if the member were absent, so `max`, whose writer
sets `reasoning.effort` to `xhigh` beside it, is refused as `xhigh` is.

A model ID that does not name its generation, such as a tuned model or
a private endpoint, is assumed to take levels. Override it when that is
wrong; the option fixes the encoding and changes nothing else:

```go
gemini.New(client, gemini.WithThinking(gemini.ThinkingBudget))
```

## Input order

`instructions` and the system and developer messages that precede
every other input item form the system instruction, in that order. A
system or developer message after any other item stays where it is: its
text, unchanged, becomes text parts of the user turn at that point,
joining a user turn already open or opening one after a model turn.
Moving it into the system instruction would change what it applies to.
Only text is accepted in a system or developer message.

## What the request mapping rejects and ignores

A request field Gemini has no equivalent for comes back as an
`invalid_request` error with `code: unsupported_parameter` and `param`
naming the field, rather than being dropped: `parallel_tool_calls:
false`, `max_tool_calls`, `safety_identifier`, `truncation: auto`,
`text.verbosity`, `file_id` on images and files, `compaction` and
`item_reference` items, and tools and items from other providers'
slugs. `reasoning.effort: xhigh` is `invalid_request` with `code:
invalid_value` (see above), and so is `reasoning.summary` alongside
`reasoning.effort: none`, which is contradictory.
`previous_response_id` reaching the adapter means the handler has no
`ResponseStore`, and is `previous_response_not_found`.

Accepted without effect: `strict` on function tools and on
`text.format`, `include`, `stream_options` and `store` (the handler's
concerns), and `prompt_cache_key`, which is a caching hint: Gemini
caches implicitly, so the key has nothing to name. `metadata` becomes
Vertex AI labels, which the Gemini API rejects.

Unknown top-level request members are read by kind (PLAN.md rule 8).
A member under another provider's slug, `anthropic.` or
`chatcompletions.`, is a setting meant for a provider this adapter
cannot honour, and is `invalid_request` with `code:
unsupported_parameter` and `param` naming the member. A member under
`gemini.` is a provider parameter; none is defined yet, so each is
refused the same way. A shared `openresponses-experimental.` member the
adapter does not implement is ignored, since each defines a fallback the
specification's own fields carry. Any other member is dropped, unless
the adapter is built with `WithExtra`:

```go
gemini.New(client, gemini.WithExtra())
```

which forwards those other members into the `GenerateContent` body, for
Gemini request fields the mapping does not cover, such as
`cachedContent`. A key the mapping sets is never overridden. Every
member a client sends then reaches Gemini, so a server that does not
trust its clients should filter `Extra` first.

Extension types use the `gemini.` slug. A tool `{"type":
"gemini.google_search"}` is Google Search; its remaining keys are the
tool's configuration in the SDK's wire form (`excludeDomains`, and so
on), and `gemini.url_context` and `gemini.code_execution` work the
same way.

## Responses

Gemini's parts become items in order: text opens or extends an
assistant message, thoughts open or extend a reasoning item, a function
call arrives whole and is emitted as one `function_call` (the client
sees a single `function_call_arguments.delta` with the complete JSON),
and executable code, code execution results, inline data and file data
become `gemini.executable_code`, `gemini.code_execution_result`,
`gemini.inline_data` and `gemini.file_data` items whose `part` key is
the part in the SDK's wire form. Send those items back in `input` and
the part is replayed.

Thought signatures round-trip through `reasoning` items. Gemini puts
the signature on the first part after the thoughts, usually the
function call; the adapter emits a reasoning item carrying it in
`encrypted_content` just before that part, and when the conversation
comes back it puts the signature on that part again. Keep reasoning
items in the input you replay, or Gemini 3 rejects the next function
call turn.

Every reasoning item the adapter emits names its issuer,
`"openresponses-experimental.issuer": "gemini"` (docs/extensions.md
section 4), so a transcript that moves between providers does not send
one provider's signature to another. On input a reasoning item is
treated by its issuer, compared as a whole string:

| issuer | what is sent |
|---|---|
| `gemini` | the summary as a thought part and `encrypted_content`, base64-decoded, as the signature; content that is not base64 is `invalid_request` on `encrypted_content` |
| absent, `encrypted_content` empty | the summary as a thought part, no signature |
| absent, `encrypted_content` base64 | as `gemini`: items recorded before the member existed keep working |
| absent, `encrypted_content` not base64 | nothing: the content is another provider's signature, so the item is left out |
| any other value, `gemini/<variant>` included | nothing: the item is left out whole, summary included, since its summary is another model's reasoning rather than thoughts Gemini had |

Gemini 3 refuses a model step that calls a function without a thought
signature, and a transcript another provider wrote, or one whose
reasoning items were left out above, has only such steps. Google
documents a placeholder for this case: "You can set
`thought_signature` to `skip_thought_signature_validator`, but, this
should be a last resort as it will negatively impact model
performance"
([thought signatures](https://docs.cloud.google.com/vertex-ai/generative-ai/docs/thought-signatures)).
On a model that takes thinking levels (see the model ID table above),
each model step that has a function call and no signature on any of
its parts gets that placeholder on its first function call, the only
call Gemini signs in a parallel step. A step with a signature of its
own is left as Gemini wrote it, and the generations that take budgets
do not validate signatures, so nothing is added there. The string is
the field's JSON form, which is base64: the adapter sends the bytes it
decodes to, in the URL alphabet its underscores belong to.

`STOP` completes the response. `MAX_TOKENS` ends it incomplete with
`max_output_tokens`; the safety, recitation, blocklist, prohibited
content, SPII, language and image reasons, and a blocked prompt, end
it incomplete with `content_filter`. `MALFORMED_FUNCTION_CALL`,
`UNEXPECTED_TOOL_CALL`, `TOO_MANY_TOOL_CALLS` and `OTHER` fail it with
a `model_error` whose code is the reason in lower case. Grounding
supports become `url_citation` annotations with character offsets into
the message text, and logprobs land on the `output_text` part. `model` on
the response is the version Gemini served.

Usage: `input_tokens` is `promptTokenCount` plus
`toolUsePromptTokenCount`, and `cached_tokens` is
`cachedContentTokenCount`, the part of the input read from a cache.
`output_tokens` is `candidatesTokenCount` plus `thoughtsTokenCount`, and
`reasoning_tokens` is `thoughtsTokenCount`. Gemini reports no cache
writes, so the adapter never sets
`openresponses-experimental.cache_write_tokens` (docs/extensions.md
section 5): implicit caching charges nothing to write, and an explicit
cache is a resource created and billed outside any request.

Upstream errors keep their HTTP status; the gRPC status in lower case
is the code, and a `RetryInfo` detail on a 429 becomes `Retry-After`.
The type follows the status: 400 and the other 4xx statuses are
`invalid_request`, 404 is `not_found`, 429 is `too_many_requests` and
5xx is `server_error`. A 401 `UNAUTHENTICATED` or 403
`PERMISSION_DENIED` refuses the adapter's own credentials. No retry
can fix that, so it is `invalid_request` with the 401 or 403 and the
code `unauthenticated` or `permission_denied`, never `server_error`.

An error envelope that arrives inside the stream, after the 200 has
gone out, is the same failure and maps exactly as the same envelope on
a non-2xx response: its `code` is the status. One with no `code`, or a
2xx `code`, takes its status from its type, so an error never carries
a 2xx. The SDK (v1.72.0) reads an envelope that arrives as a line of
its own; one sent as an SSE `data:` event is dropped by the SDK's
response converter before the adapter sees it. A stream that ends
without a finish reason, that case included, fails with a 500
`server_error` whose code is `truncated_stream`.
A request whose input exceeds the model's context window is
`invalid_request` with the code `context_length_exceeded`
(docs/extensions.md section 6) and the 400. Gemini has no code of its
own for it: the adapter recognises an `INVALID_ARGUMENT` (or any 400)
whose message contains both "input token count" and "exceeds", case
ignored, as in "The input token count (1200000) exceeds the maximum
number of tokens allowed (1048576)." Output cut short by
`max_output_tokens` is not an overflow; it ends the response incomplete,
as above.

## Model information

The adapter implements `openresponses.Describer`. `Describe` calls
Gemini's `models.get` and returns the
`openresponses-experimental.model_info` value (docs/extensions.md
section 7), filled only from what that endpoint publishes:

| Member | From `models.get` on the Gemini API |
|---|---|
| `source` | `gemini` |
| `context_window` | `inputTokenLimit` |
| `max_output_tokens` | `outputTokenLimit` |
| `reasoning` | `thinking`, which the API leaves out when false |
| `efforts` | `["none"]` when not thinking; otherwise not said |
| `default_effort`, `input_modalities` | not said |

On Vertex AI the model resource carries none of these and the SDK maps
none of them, so `Describe` returns `source` alone: an absent `thinking`
there must not read as false. A model Gemini does not know is a
`not_found` error.

## Versioning

The module is tagged `providers/gemini/vX.Y.Z`, independently of the
root library, and [CHANGELOG.md](CHANGELOG.md) records its changes.
