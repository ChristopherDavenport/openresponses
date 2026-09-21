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
`thinkingBudget`, in tokens. The adapter reads which from the model ID:

| effort | Gemini 3+ (`thinkingLevel`) | Gemini 2.5 (`thinkingBudget`) |
|---|---|---|
| `none` | rejected — it always thinks | 0 |
| `minimal` | `MINIMAL` | 512 |
| `low` | `LOW` | 4096 |
| `medium` | `MEDIUM` | 8192 |
| `high` | `HIGH` | 24576 |

The budgets are the widest values legal across the 2.5 family, which
Google documents neither a mapping nor per-model ceilings for. `xhigh`
is rejected in both: there is no level above `high`.

A model ID that does not name its generation — a tuned model, a private
endpoint — is assumed to take levels. Override it when that is wrong:

```go
gemini.New(client, gemini.WithThinking(gemini.ThinkingBudget))
```

## What the request mapping rejects and ignores

A request field Gemini has no equivalent for comes back as an
`invalid_request` error with `code: unsupported_parameter` and `param`
naming the field, rather than being dropped: `parallel_tool_calls:
false`, `max_tool_calls`, `safety_identifier`, `prompt_cache_key`,
`truncation: auto`, `text.verbosity`, `file_id` on images and files,
`compaction` and `item_reference` items, tools and items from other
providers' slugs, and the `reasoning.effort` values the model's
generation cannot express (see above). `reasoning.summary` alongside
`reasoning.effort: none` is rejected as contradictory.
`previous_response_id` reaching the adapter means the handler has no
`ResponseStore`, and is `previous_response_not_found`.

Accepted without effect: `strict` on function tools and on
`text.format`, `include`, `stream_options` and `store` (the handler's
concerns). `metadata` becomes Vertex AI labels, which the Gemini API
rejects.

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

`STOP` completes the response. `MAX_TOKENS` ends it incomplete with
`max_output_tokens`; the safety, recitation, blocklist, prohibited
content, SPII, language and image reasons, and a blocked prompt, end
it incomplete with `content_filter`. `MALFORMED_FUNCTION_CALL`,
`UNEXPECTED_TOOL_CALL`, `TOO_MANY_TOOL_CALLS` and `OTHER` fail it with
a `model_error` whose code is the reason in lower case. Grounding
supports become `url_citation` annotations with character offsets into
the message text, and logprobs land on the `output_text` part. Usage
counts thinking tokens in `output_tokens` and reports them in
`reasoning_tokens`; `model` on the response is the version Gemini
served.

Upstream errors keep their HTTP status; the gRPC status in lower case
is the code, and a `RetryInfo` detail on a 429 becomes `Retry-After`.
The exception is 401 and 403, which are the adapter's own credentials
failing rather than anything the caller sent: those become a
`server_error` with a 502, so a caller that never supplied a Google
credential is not told to go and rotate one.

## Versioning

The module is tagged `providers/gemini/vX.Y.Z`, independently of the
root library, and [CHANGELOG.md](CHANGELOG.md) records its changes.
