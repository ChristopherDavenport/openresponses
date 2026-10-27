# Shared extensions

Status: draft 0.1

This document defines the extensions this library uses where the Open
Responses specification (version 2026-04-24) has no member for something
more than one provider needs. Each one is written so that it could
become a section of the specification without changing: where the
member goes, its JSON shape, the rules for writing and reading it, who
writes it and who reads it, and what is missing from the specification
that it supplies. Proposing any of them upstream is a later decision;
this document is where each one is checked first.

The key words MUST, MUST NOT, SHOULD, SHOULD NOT and MAY are to be read
as in RFC 2119.

| Extension | Placement | Issues | Verdict |
| --- | --- | --- | --- |
| [Unknown members are kept](#13-unknown-members-are-kept) | every object | #53, #56 | general rule |
| [`openresponses-experimental.effort`](#3-reasoning-effort-above-xhigh) | request `reasoning` | #51 | in the set |
| [`openresponses-experimental.issuer`](#4-who-issued-a-reasoning-item) | reasoning item | #56 | in the set |
| [`openresponses-experimental.cache_write_tokens`, `openresponses-experimental.cache_write_tokens_by_ttl`](#5-cache-writes) | `usage.input_tokens_details` | #53 | in the set |
| [`context_length_exceeded`](#6-error-codes) | error `code` value | #61 | in the set, as a code |
| [`openresponses-experimental.model_info`](#7-model-information) | `GET /models/{model}` model object | #51 | in the set |
| [Retryability](#8-not-in-the-set) | none | #58 | not needed |
| [Refusal category](#8-not-in-the-set) | `incomplete_details` | #59 | provider slug instead |
| [Claude content parts](#8-not-in-the-set) | content part | #63 | provider slug instead |

## 1. General rules

### 1.1 What the specification allows

The specification lets an implementor add fields to existing schemas,
provided that "extensions MUST NOT alter required core behavior", that
optional fields are preferred "so that portable clients can ignore them
safely", and that each field's types, allowed values and failure modes
are documented. New item types and streaming event types MUST carry the
implementor's slug, and clients SHOULD be prepared to meet unknown item
types and MUST be able to ignore unknown event types. The specification
also asks implementors to "avoid assuming other APIs will honor" an
extension and to propose patterns in common use through its TSC
process. This document follows all of that. Every member below is
optional, every member has a meaning that a reader who ignores it does
not get wrong, and every member is a candidate for that process.

### 1.2 Slugs

A slug names whoever defines the extension. Each one in this document is
named with the slug `openresponses-experimental.`, and the sibling
libraries name theirs the same way after their repository
(`agentsession.`, `agentturn.`). The specification does not define what
characters a slug may contain, and it does not mention hyphens, so
`openresponses-experimental` is a valid slug. Its examples join slug and
name with a colon (`acme:trace_event`). This library has joined them
with a dot since its first extension (`anthropic.`, `gemini.`), and this
set does the same.

"Experimental" marks each member of the set as a candidate for the
specification, not as unstable. A member the specification adopts
becomes its unprefixed specification form. From the release that writes
the specification form, writers in this library write only that form,
and readers accept both forms for two minor releases, so that sessions
and stored responses written before still read. When the specification
adopts a member with a different shape or meaning, the section here
states the mapping between the two forms for that period.

A **shared** extension is one whose meaning does not depend on the
provider: any adapter may write it and any reader may act on it. Only
shared extensions belong in this document. A shape that only one
provider has, such as a Claude content block or a Gemini server tool,
stays under that adapter's slug (`anthropic.`, `gemini.`) and is
defined in that adapter's README.

Field names are slug-prefixed even though the specification requires
the prefix only on types. A prefixed member cannot collide with a member
the specification adds later, and it names its definer wherever the JSON
is copied.

### 1.3 Unknown members are kept

An implementation that decodes and re-encodes a specification object
MUST keep every member it does not know, with its value unchanged. This
applies to every object at every depth, not only the top-level request
and response. Without this rule, a member below the top level is lost
the first time a proxy, a store or a session recorder reads and writes
the object. This library's root package keeps unknown members in an
`Extra` field on the request and response, every item, content part,
annotation and function tool, the `usage` object and both its details
objects, `reasoning`, `text` and its `format`, the object forms of
`tool_choice`, `stream_options`, `incomplete_details` and log
probabilities. It does not yet keep them on the streaming event
envelopes or on the error object; each of those is a defect against this
rule.

### 1.4 Readers

A reader that does not implement a shared member MUST ignore it, and
MUST still keep it as 1.3 requires. Every shared member is defined so
that ignoring it gives the behaviour the specification already defines.
A reader that implements a member MUST follow its rules.

### 1.5 Adapters and request members

An adapter that maps Open Responses onto a provider's API reads three
kinds of extension member on a request:

1. **Shared members it implements:** it acts on them as this document
   says.
2. **Shared members it does not implement:** it ignores them. This is
   not dropping a field silently in the sense of
   [PLAN rule 3](../providers/PLAN.md), because each shared member
   defines a fallback that the specification's own fields carry.
3. **Members under a provider's slug:** members under the adapter's own
   slug are provider parameters, defined in its README. A member under
   another provider's slug is refused with `invalid_request`, with
   `param` set to the member's name. That is the case rule 3 is for: a
   setting meant for one provider that this one cannot honour.

Any other unknown top-level member is treated as PLAN rule 8 says.

### 1.6 Writing an extension down

Every section in 3–7 says the same things in the same order: the gap in
the specification, where the member goes, its shape, who writes it, who
reads it, and the rules. A section that cannot say all of that does not
belong in this document.

## 2. Producers and consumers in this repository

| Extension | Written by | Read by |
| --- | --- | --- |
| `openresponses-experimental.effort` | clients (agentturn, dax) | `providers/anthropic` |
| `openresponses-experimental.issuer` | `providers/anthropic`, `providers/gemini` | both, on input; agentturn (#286, #242) |
| `openresponses-experimental.cache_write_tokens`, `openresponses-experimental.cache_write_tokens_by_ttl` | `providers/anthropic` | agenteval (#68), any price table |
| `context_length_exceeded` | all three adapters | agentturn's compaction |
| `openresponses-experimental.model_info` | the handler (from an adapter), the Responses client's vendor mappings | dax, agentturn's compaction budget, agentkit's effort floor, effort fitting |

## 3. Reasoning effort above `xhigh`

**Gap.** `ReasoningEffortEnum` runs `none`, `low`, `medium`, `high`,
`xhigh`, and it describes `xhigh` as "the maximum reasoning effort
available". Some providers have a higher setting that they offer
alongside their own `xhigh`. Claude has both `xhigh` and `max`, so
mapping the specification's `xhigh` to Claude's `max` would make
Claude's `xhigh` unreachable. A client has no way to ask for the higher
setting.

**Placement.** A member of the request's `reasoning` object.

```json
"reasoning": {
  "effort": "xhigh",
  "openresponses-experimental.effort": "max"
}
```

**Values.** One value is defined:

- `max`: the most reasoning the model offers. That is above `xhigh`
  where the provider distinguishes the two, and the same as `xhigh`
  where it does not.

Values not listed here MUST NOT be sent. A reader that receives one it
does not know MUST treat the member as absent.

**Writers.** A client that sets the member MUST also set
`reasoning.effort` to the specification value nearest to it, which for
`max` is `xhigh`. A server or adapter that ignores the member then does
the closest thing the specification can express.

**Readers.** An adapter whose provider has the setting sends it in place
of `reasoning.effort`; the Claude adapter sends `output_config.effort:
max`. An adapter whose provider does not have it ignores the member and
maps `reasoning.effort`. A response MUST NOT echo the member unless the
provider honoured it.

**Replaces** the root constant `ReasoningEffortMax` proposed in #51,
which added a value the specification's enum does not have, and the
interim `anthropic.effort` request member. A provider-independent
meaning belongs under the shared slug.

## 4. Who issued a reasoning item

**Gap.** A reasoning item's `encrypted_content` can be verified only by
the provider that issued it: Claude's thinking `signature` and Gemini's
`thoughtSignature` both travel in that field (PLAN rule 5). The
specification has no member that says who issued it. A transcript that
moves between providers therefore sends one provider's signature to
another, which refuses the request. #56 reproduced this live: an
unsigned reasoning item sent to Claude comes back as a 400.

**Placement.** A member of a `reasoning` output item.

```json
{
  "type": "reasoning",
  "id": "rs_…",
  "summary": [ … ],
  "encrypted_content": "…",
  "openresponses-experimental.issuer": "anthropic"
}
```

**Shape.** A string, `<issuer>` or `<issuer>/<variant>`:

- `<issuer>` is the slug under which the issuing provider's extensions
  are defined (`anthropic`, `gemini`).
- `<variant>` is defined by that issuer, for when its own formats
  differ. Readers compare the whole string.

OpenRouter's reasoning `format` (`anthropic-claude-v1`) shows that this
need exists outside this library.

**Writers.** An adapter that emits a reasoning item with
`encrypted_content` MUST set the member to its own issuer. A client that
knows which provider stands behind a Responses API upstream MAY set it
on items from that upstream. Proxies, stores and session recorders keep
it as 1.3 requires.

**Readers.** An adapter that receives a reasoning item:

1. **Its own issuer:** it MUST send `encrypted_content` back to its
   provider, as PLAN rule 5 says.
2. **Another issuer:** it MUST NOT send `encrypted_content` to its
   provider. It leaves the item out, or sends the item's summary as
   text, and its README states which.
3. **No member:** the issuer is unknown. Sessions recorded before this
   extension carry no member, and Claude requires the thinking block on
   a tool-use turn, so leaving every such item out would break those
   sessions. The adapter MAY therefore send `encrypted_content` as its
   own. It SHOULD leave the item out when it can tell the content is not
   its own: Gemini's `thoughtSignature` is base64 and Claude's
   `signature` is not, and an empty `encrypted_content` is never a
   signature.

**Replaces** the per-adapter stamps proposed in the review of #56
(`anthropic.signature: true`, `gemini.signature: true`), which said the
same thing in two provider namespaces.

## 5. Cache writes

**Gap.** `input_tokens_details` has `cached_tokens`, the input read from
a provider's prompt cache. It has nothing for input written to the cache.
Claude prices a cache write at 1.25 times the input rate for a 5-minute
entry and 2 times for a 1-hour entry, so a cost computed from the
specification's usage prices every write as plain input. OpenRouter
reports the same quantity as an unprefixed `cache_write_tokens`.

**Placement.** Members of `usage.input_tokens_details`.

```json
"usage": {
  "input_tokens": 12000,
  "input_tokens_details": {
    "cached_tokens": 9600,
    "openresponses-experimental.cache_write_tokens": 2100,
    "openresponses-experimental.cache_write_tokens_by_ttl": { "5m": 100, "1h": 2000 }
  },
  "output_tokens": 300,
  "output_tokens_details": { "reasoning_tokens": 0 },
  "total_tokens": 12300
}
```

**Shape.**

- `openresponses-experimental.cache_write_tokens`: a non-negative integer, the input
  tokens written to the prompt cache by this response. They are counted
  in `input_tokens` and are disjoint from `cached_tokens`. A token read
  from the cache is not also written.
- `openresponses-experimental.cache_write_tokens_by_ttl`: an object that splits that
  count by how long the provider keeps the entry.
  - Each key is a duration: a decimal integer followed by `s`, `m` or
    `h`.
  - Each value is a non-negative integer.
  - The values sum to `openresponses-experimental.cache_write_tokens`.
  - It is present only when the provider reports the split.
  - A split with a key or value of another form is invalid as a whole,
    and a reader treats it as absent rather than reading part of it.

**Writers.** An adapter whose provider reports cache writes MUST set
`openresponses-experimental.cache_write_tokens`, and sets the split when the provider
reports it. Across the turns of one response (for example a resumed
pause), the counts are summed. The Claude adapter maps `cache_creation`:
`ephemeral_5m_input_tokens` becomes the `5m` entry and
`ephemeral_1h_input_tokens` the `1h` entry.

**Readers.** A price table that has a cache-write rate SHOULD price
these tokens at it, and the remaining
`input_tokens − cached_tokens − cache_write_tokens` at the input rate.
A reader MAY treat OpenRouter's unprefixed `cache_write_tokens` as the
same quantity when the shared member is absent. This library's reader
does, so usage that reaches it through OpenRouter is priced the same
way. Neither adapter writes the unprefixed name.

**Replaces** the unprefixed `cache_write_tokens` proposed in #53 (a
field the specification does not have) and a raw
`anthropic.cache_creation` object. The quantity does not depend on the
provider; only the TTL values do.

## 6. Error codes

**Gap.** The error object's `code` is a free string. The specification
names only a few codes (`previous_response_not_found`,
`websocket_connection_limit_reached`), so each provider's way of saying
the same thing reaches the client under a different code. The case that
matters most is a request whose input is larger than the model's context
window. A harness that compacts on overflow has to recognise it, and
today it must match three providers' messages.

**Placement.** Values of an error's `code`. No new member is defined. A
value already used by an established upstream for the same condition is
adopted unprefixed, so a client that already handles that upstream needs
no change. A condition no upstream names would take an
`openresponses-experimental.`-prefixed value.

| Code | Type | Meaning |
| --- | --- | --- |
| `context_length_exceeded` | `invalid_request` | The request's input, with its instructions and tools, exceeds the model's context window. Retrying the same request cannot succeed. The client must shorten it. OpenAI already sends this code. |

**Writers.** An adapter MUST report an overflow with this code and type,
whatever the provider's own code. Each adapter's README states how it
recognises one: Claude sends `invalid_request_error` with "prompt is too
long", and Gemini sends `INVALID_ARGUMENT` with "input token count …
exceeds". A response cut short by the output token limit is not an
overflow. It ends `incomplete` with `max_output_tokens`, Claude's
`model_context_window_exceeded` stop included.

**Readers.** A client MAY compact and retry on this code. It MUST NOT
retry the same request unchanged.

The codes the library already uses for refusing a request
(`invalid_value`, `missing_required_parameter`,
`unsupported_parameter`) are OpenAI's and keep their OpenAI meaning.
They belong in this registry when a section is written for them.

## 7. Model information

**Gap.** Nothing in the specification says what a model accepts, so
every layer above a model guesses:

- A harness compacts on a fixed token budget, because nothing gives it
  the context window.
- A product cannot choose a summary request's effort, because it does
  not know whether the model refuses `none`.
- An adapter sends an effort the model does not take, and the request
  fails.

Provider APIs publish this information (Claude's and Gemini's model
endpoints, OpenRouter's catalogue, Ollama's `/api/show`), each in its own
shape. The specification has no models resource to carry it.

**Placement.** A member of the model object served at `GET
/models/{model}`, under the same base URL as `/responses`. OpenAI and
the servers compatible with it (OpenRouter, Ollama, vLLM) already serve
that endpoint, with `id`, `object: "model"`, `created` and `owned_by`.
The member is added to that object, and every other member keeps its
meaning. A server that has no such endpoint MAY serve one that holds
only `id`, `object` and this member. It is not a member of a response:
a client needs the information before its first request, and repeating
it on every response would add bytes to each.

```json
{
  "id": "example-model",
  "object": "model",
  "openresponses-experimental.model_info": {
    "source": "anthropic",
    "context_window": 200000,
    "max_output_tokens": 64000,
    "reasoning": true,
    "efforts": ["none", "low", "medium", "high", "xhigh", "max"],
    "default_effort": "high",
    "input_modalities": ["text", "image", "file"]
  }
}
```

**Shape.** An object. Every member except `source` is optional, and an
absent member means the source did not say. It never means "no" or
zero.

| Member | Type | Meaning |
| --- | --- | --- |
| `source` | string | Who answered: the provider's slug (`anthropic`, `gemini`, `openrouter`, `ollama`) or the serving implementation's name. Required. |
| `context_window` | integer | The most input tokens one request may carry, with its instructions and tools. |
| `max_output_tokens` | integer | The most output tokens one response may carry, reasoning included. |
| `reasoning` | boolean | Whether the model reasons at all. |
| `efforts` | array of strings | The reasoning efforts the model accepts, ordered from least to most. The values are the specification's `ReasoningEffortEnum`, `minimal`, and `max` as defined in section 3. `none` is listed when reasoning can be turned off. A model that cannot reason lists only `none`. |
| `default_effort` | string | The effort the model runs at when the request sends none. |
| `input_modalities` | array of strings | The input content the model accepts: `text`, `image`, `file`, `audio`, `video`. |

Prices are not in the set. They vary by account, route and date, and
they change without the model changing. A price table belongs to its
reader (agenteval keeps one), keyed by `id`.

**Writers.** A writer fills a member only from what the provider
publishes, and leaves out what the provider does not say. It never
fills a member from a table of models it keeps, because such a table
falls behind every model release. The sources the provider adapters and
the Responses client map from:

| Member | Claude `GET /v1/models/{id}` | Gemini `models.get` | OpenRouter catalogue | Ollama `/api/show` |
| --- | --- | --- | --- | --- |
| `context_window` | `max_input_tokens` | `inputTokenLimit` | `context_length` | (not said) |
| `max_output_tokens` | `max_tokens` | `outputTokenLimit` | `top_provider.max_completion_tokens` | (not said) |
| `reasoning` | `capabilities.thinking.supported` | `thinking` | `reasoning` present, or `supported_parameters` has `reasoning` | `capabilities` has `thinking` |
| `efforts` | `none`, plus each level whose `capabilities.effort.<level>.supported` is true when `capabilities.effort.supported` is true | `["none"]` when not thinking; otherwise not said | `reasoning.supported_efforts`, plus `none` unless `reasoning.mandatory` | `["none"]` when not thinking; otherwise not said |
| `default_effort` | (not said) | (not said) | `reasoning.default_effort` | (not said) |
| `input_modalities` | (not said) | (not said) | `architecture.input_modalities` | `capabilities` has `vision` → `image` |

In this library, the handler is to serve the endpoint when its adapter
can describe a model, and the Claude and Gemini adapters are to describe
models from their providers' model endpoints. None of this is
implemented yet. The Responses client reads the member from
an upstream that serves it, and otherwise maps the OpenRouter and Ollama
sources above.

**Readers.** The information is a hint taken before a request. The
response is the authority: a request the model refuses still fails as
the provider says, as PLAN rule 1 requires. With the information, a
reader:

- MAY derive a compaction budget from `context_window`.
- MAY fit a requested effort to `efforts`. The fit is the requested
  effort when it is listed, and otherwise the listed effort nearest to
  it in the order above, the lower of two equally near. A reader MUST
  NOT fit an effort other than `none` to `none` while the model lists
  any other effort.

Without it, or with a member absent, a reader MUST send the request as
asked, which is the specification's behaviour, and uses its own
defaults (for compaction, a fixed budget).

**Go binding.** #51 proposed `Info` and `Describer` types in the root
package with no wire form. They stay as this section's Go binding:
`Info` is the member's value, and `Describer` is an optional interface
beside `Streamer` that an adapter or the Responses client implements
when it can describe a model. dax's `internal/modelinfo` has been trying
out this shape since dax v0.0.1. With this section, its fields gain a
wire form that any implementation can serve or read.

## 8. Not in the set

**Retryability (#58).** Not needed. The specification already says
which error types can be retried (`server_error`: "Retrying later may
succeed"; `too_many_requests`: "retry after a delay"). Every adapter
maps a provider's refusal of the adapter's own credentials or
permissions to `invalid_request`. agentturn's default retry rule is to
decide by type rather than status; that change is filed against
agentturn.

**Refusal category (#59).** Claude reports a category with a `refusal`
stop, and Gemini reports safety ratings with its own categories. The
vocabularies differ by provider. Claude's category is therefore an
`anthropic.` member on `incomplete_details`, defined in that adapter's
README. The adapter MUST NOT write refusal text that Claude did not
send.

**Claude content parts (#63).** A Claude block with no counterpart in
the specification, such as `tool_reference`, is a content part
`{"type": "anthropic.<block type>", "block": <block>}`. It is defined in
the Claude adapter's README. The general rule that it applies, an
extension content part's `type` carries the definer's slug as an
extension item's does, is in 1.2.
