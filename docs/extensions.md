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
| [Items under another provider's slug](#16-adapters-and-items-under-a-providers-slug) | input items, content parts | agentturn#285 | general rule |
| [`sidecar:` media references](#17-media-held-in-a-session-store) | image, file and video URLs | agentturn#284 | general rule |
| [A trailing assistant turn](#18-adapters-and-a-trailing-assistant-turn) | the end of `input` | #77 | general rule |
| [`openresponses-experimental.effort`](#3-reasoning-effort-above-xhigh) | request `reasoning` | #51 | in the set |
| [`openresponses-experimental.issuer`](#4-who-issued-a-reasoning-item) | reasoning item | #56 | in the set |
| [`openresponses-experimental.cache_write_tokens`, `openresponses-experimental.cache_write_tokens_by_ttl`](#5-cache-writes) | `usage.input_tokens_details` | #53 | in the set |
| [`context_length_exceeded`](#6-error-codes) | error `code` value | #61 | in the set, as a code |
| [`insufficient_quota`](#6-error-codes) | error `code` value | #73 | in the set, as a code |
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
`tool_choice`, `stream_options`, `incomplete_details`, log
probabilities, every streaming event and the error object. An object it
adds later without one is a defect against this rule.

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
   setting meant for one provider that this one cannot honour. A tool
   under another provider's slug is such a setting too, and is refused
   with `invalid_request` naming its `type`.

Any other unknown top-level member is treated as PLAN rule 8 says. An
input item or content part under a provider's slug is not a request
member; 1.6 says what an adapter does with one.

### 1.6 Adapters and items under a provider's slug

**Gap.** An input item or content part whose `type` carries a
provider's slug (`anthropic.redacted_thinking`,
`gemini.executable_code`) is that provider's record of its own output.
It reaches another provider's adapter whenever a transcript moves
between providers, the case section 4 meets for reasoning. The
specification says that clients should be prepared to meet unknown
item types, and says nothing of what a server that maps onto a
provider does with one. Refusing it fails every request after such a
move, and sending it hands a provider a shape it does not define.

**Rule.** An adapter that receives an input item, or a content part,
whose type carries another provider's slug MUST NOT send it to its
provider and MUST NOT refuse the request for it. It leaves the item
out, and its README says so. Refusal stays the rule for request members
under another provider's slug (1.5).

An item that carries content rather than a signature, such as a code
execution result or a web search result, is part of what the model was
told, and leaving it out changes what the next model is asked. For such
an item an adapter MAY send a text rendering of its content in its
place, as its provider's own text, instead of leaving it out. Its README
says which items it renders and how, and which it leaves out. An item
that carries only a signature or redacted reasoning has nothing to
render and is left out.

This is section 4's second reader rule applied to whole items: there an
adapter leaves out a reasoning item another issuer signed, or sends its
summary as text. The details:

- The rule holds wherever the part sits: in a message of any role and
  in a function call output.
- A message whose every part is left out is not sent. A function call
  output is still sent without the part, since its call needs an
  answer.
- The adapter leaves the item out of what it sends to its provider,
  and not out of the request it was given. The caller's input, a stored
  response and a session record keep the item as 1.3 requires, so a
  later request to the provider that wrote it sends it again.
- Nothing in a response says what was left out or rendered; the
  specification has no member for it. The README is where a caller
  learns which items an adapter leaves out and which it renders.
- An item under the adapter's own slug is its provider's and is sent,
  or refused when it is malformed. An item under a slug that names no
  provider the adapter knows of (`acme.`, a harness's slug) is not
  covered: the adapter cannot tell whether it carries meaning the
  provider must see, and refuses it as PLAN rule 3 says.

**Pass-through adapters.** An adapter that speaks Open Responses to an
upstream has no provider of its own: its provider is whatever stands
behind the upstream, which the request does not say. It learns its
provider's slugs from whoever configures it, who names the slugs the
upstream accepts. An upstream that is an Open Responses server over one
provider's adapter accepts that provider's slug. A server that routes
each model to its own adapter accepts the slug of each, since the
adapter it routes to applies this rule again. An upstream that is a
provider's own API accepts none, and is configured with an empty list.

A pass-through that has not been configured cannot know its provider,
and forwards every item as given. Leaving items out on a guess would
break the setup this library ships for proxies: an Open Responses
server over the Claude adapter, reached through a pass-through, needs
Claude's signed reasoning and its `anthropic.` blocks back (section 4).

A configured pass-through:

- MUST leave out an input item or content part under a provider's slug
  it was not given, as above, an annotation on `output_text` under such
  a slug (Claude's citation locations, which another upstream refuses as
  an unknown type), keeping the part, and a reasoning item whose
  `openresponses-experimental.issuer` names such a provider, as section
  4's second reader rule says. It compares the issuer's `<issuer>`
  part, and leaves the comparison of the whole string to the adapter
  behind the upstream.
- MUST refuse a request member or tool under a provider's slug it was
  not given, as 1.5 says, with `param` naming the member or the tool's
  `type`, before anything reaches the upstream. Unconfigured, it
  forwards them.
- Forwards unchanged an item under a slug that names no provider, a
  shared one, and a reasoning item that names no issuer. Refusing those
  would be deciding for an upstream it cannot see: the adapter that maps
  onto a provider decides, as above, and an upstream that does not stamp
  its reasoning items needs its own back.

In this library the provider slugs are those of its three adapters:
`anthropic.`, `gemini.` and `chatcompletions.`. The pass-through is
`ClientAdapter`, and `WithProviderSlugs` names the slugs its upstream
accepts.

### 1.7 Media held in a session store

**Gap.** A session store may keep an item's media apart from the item
and write a reference in its place: a URL of the form
`sidecar:sha256:<hex>`, naming the blob by its hash (agentsession RFC
0001, "File"). The reference means something only to that store. No
provider can fetch it, and one handed it as an image or file URL tries
and fails, in its own words.

**Writers.** A `sidecar:` URL is a store reference that the writer
resolves before sending. Whoever builds a request from a record MUST
replace each `sidecar:` URL with the media's bytes, as a data URL or as
`file_data`, before sending the request, and MUST NOT send a `sidecar:`
URL to a server or an adapter.

**Adapters.** An adapter that receives a `sidecar:` URL in an image,
file or video field (`image_url`, `file_url`, `file_data`,
`video_url`), in a message or in a function call output, MUST refuse
the request with `invalid_request`, code `invalid_value` and `param`
naming the field. It MUST NOT fetch or resolve the reference, and MUST
NOT pass it to its provider as a URL to fetch. The scheme is matched
as a URL parser reads it: without regard to case, and after leading and
trailing spaces and control characters are removed. A padded reference
is refused as a reference, not as a value that is not a URL. An adapter that refuses the
part's type outright, as one whose provider takes no video does, has
already refused it.

Refusal here, where 1.6 leaves an item out, is deliberate. Media is
part of what the user or a tool said; leaving it out would change what
the model is asked without anyone seeing it, while the refusal names
the writer's mistake where it can be fixed.

### 1.8 Adapters and a trailing assistant turn

**Gap.** In Open Responses an input that ends on an assistant-side
item (an assistant message, a function call, reasoning, or a
provider's item of its own output) is context: the model responds
after it. Some providers read a request that ends on an assistant turn
as a prefix for the model to continue instead (prefill), and some of
their models refuse such a request outright, as Claude's newer models
do with a 400. The specification says nothing of how the end of the
input maps onto a provider that makes that distinction.

**Rule.** An adapter MUST NOT send an input that its provider reads
with a different meaning than Open Responses gives it. In particular,
a request MUST NOT end on an assistant turn the provider would
continue as a prefix. The adapter MUST refuse such an input before
sending, with `invalid_request`, code `invalid_value` and `param`
naming the item the request would end on.

The details:

- Two inputs end that way: the last item is on the assistant side, or
  the items after the last assistant-side item send the provider
  nothing (every part left out as 1.6 says), so the assistant turn
  before them comes last. `param` names the assistant-side item in
  both.
- The adapter MUST NOT drop or invent items to make the request pass:
  that changes what the model is asked without anyone seeing it.
- A prefix a caller wants the model to continue is a provider
  parameter, an item or member under that provider's slug, defined in
  its adapter's README; none is defined yet.
- A turn its provider paused is not prefill. An adapter's own
  continuation, which sends the paused output back for the provider to
  continue, is not an input it was given and is not covered. An input
  that ends in such output, a caller resuming the turn itself, is read
  alike by the provider and by Open Responses, as a turn to carry on,
  and is sent; the adapter's README says which items end a paused
  turn.

### 1.9 Writing an extension down

Every section in 3–7 says the same things in the same order: the gap in
the specification, where the member goes, its shape, who writes it, who
reads it, and the rules. A section that cannot say all of that does not
belong in this document.

## 2. Producers and consumers in this repository

| Extension | Written by | Read by |
| --- | --- | --- |
| `openresponses-experimental.effort` | clients (agentturn, dax) | `providers/anthropic` |
| `openresponses-experimental.issuer` | `providers/anthropic`, `providers/gemini` | both, and `ClientAdapter`, on input; agentturn (#286, #242) |
| `openresponses-experimental.cache_write_tokens`, `openresponses-experimental.cache_write_tokens_by_ttl` | `providers/anthropic` | agenteval (#68), any price table |
| `context_length_exceeded` | all three adapters | agentturn's compaction |
| `insufficient_quota` | all three adapters; `Client` reads a 402 as it | agentturn's retry decision (agentturn#308) |
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
   text, and its README states which. Section 1.6 applies the same rule
   to items and content parts under another provider's slug.
3. **No member:** the item is of unknown origin. Sessions recorded
   before this extension carry no member. Each adapter states in its
   README whether it replays such items as its own or treats them as
   foreign; either way it never sends an empty `encrypted_content`,
   which is never a signature. The format rarely settles it: Claude's
   `signature` and the base64 of Gemini's `thoughtSignature` are both
   base64 text, so it can only rule out content that is not in the
   adapter's own format at all.

The Claude adapter replays an item with no member as its own: most
sessions recorded before this extension are Claude's, and Claude needs
its thinking blocks within a tool loop. It treats an item as foreign
when it carries another issuer, any value other than exactly
`anthropic`, or when its `encrypted_content` is empty, and leaves it
out. The Gemini adapter treats an item with no member as foreign; its
README says what it sends in its place. A pass-through adapter, which
forwards to an Open Responses upstream, applies rule 2 by the provider
slugs it is configured with, and forwards an item with no member, as
1.6 says.

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
today it must match three providers' messages. The next is a request
the account cannot pay for: Claude says `invalid_request_error` with a
400 ("Your credit balance is too low", or a usage limit reached) or
`billing_error` with a 402, OpenRouter a bare 402, and OpenAI
`insufficient_quota` with a 429, the status of a rate limit. A client
that decides by status retries it.

**Placement.** Values of an error's `code`. No new member is defined. A
value already used by an established upstream for the same condition is
adopted unprefixed, so a client that already handles that upstream needs
no change. A condition no upstream names would take an
`openresponses-experimental.`-prefixed value.

| Code | Type | Meaning |
| --- | --- | --- |
| `context_length_exceeded` | `invalid_request` | The request's input, with its instructions and tools, exceeds the model's context window. Retrying the same request cannot succeed. The client must shorten it. OpenAI already sends this code. |
| `insufficient_quota` | `invalid_request` | The account's or key's credit or spend limit cannot cover the request. Retrying the same request cannot succeed. OpenAI already sends this code. |

**Writers.** An adapter MUST report an overflow with code
`context_length_exceeded` and type `invalid_request`, whatever the
provider's own code. Each adapter's README states how it
recognises one: Claude sends `invalid_request_error` with "prompt is too
long", and Gemini sends `INVALID_ARGUMENT` with "input token count …
exceeds". A response cut short by the output token limit is not an
overflow. It ends `incomplete` with `max_output_tokens`, Claude's
`model_context_window_exceeded` stop included.

An adapter MUST report a request refused because the account's or
key's credit or spend limit cannot cover it with code
`insufficient_quota` and type `invalid_request`, whatever the
provider's own code, keeping the provider's message. It SHOULD keep
the HTTP status the provider sent, so the code, not the status, is
what says the request will not succeed. Each adapter's README states
how it recognises one: Claude sends `invalid_request_error` with a 400
and a message saying the credit balance is too low or a usage limit is
reached, or `billing_error` with a 402; OpenAI sends this code with a
429; and OpenRouter sends a 402.

**Readers.** A client MAY compact and retry on `context_length_exceeded`.
It MUST NOT retry the same request unchanged.

A client MUST NOT retry a request refused with `insufficient_quota`
unchanged, whatever HTTP status it arrived with, and MAY retry it
shortened or with a lower `max_output_tokens`. A client reading an HTTP
402 with no registered code, that is, no code in this section's table,
SHOULD take it as `insufficient_quota`, and SHOULD take an error typed
`insufficient_quota` with no code, as OpenAI may send one, as this
code too.
The code comes before the status: a client that decides retries by
status alone reads OpenAI's 429 as a rate limit and retries a request
that cannot succeed.

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
| `efforts` | each level whose `capabilities.effort.<level>.supported` is true, when `capabilities.effort.supported` is true, after `none` unless the adapter refuses `none` for a model that cannot turn thinking off | `["none"]` when not thinking; otherwise not said | `reasoning.supported_efforts`, plus `none` unless `reasoning.mandatory` | `["none"]` when not thinking; otherwise not said |
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
