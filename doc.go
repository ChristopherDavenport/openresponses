// Package openresponses implements the Open Responses specification
// (https://www.openresponses.org) on the wire, in both directions.
//
// The package models the request and response envelopes, every item,
// content part, tool and streaming event defined by the specification,
// and the HTTP transports: JSON over HTTP and Server-Sent Events. The
// WebSocket transport is the websocket subpackage, so this package
// builds from the standard library alone and can be the shared
// vocabulary of tool libraries that never open a socket. It is
// deliberately small: there is no agent loop, no tool runner and no
// retry policy. Those are consumers of this package.
//
// # Clients
//
// A [Client] talks to any Open Responses server:
//
//	c := openresponses.NewClient("https://api.openai.com/v1", openresponses.WithAPIKey(key))
//	resp, err := c.Create(ctx, openresponses.Request{
//		Model: "gpt-5",
//		Input: openresponses.Items{openresponses.UserText("Hello")},
//	})
//	fmt.Println(resp.OutputText())
//
// [Client.CreateStream] returns an [EventStream] that yields decoded
// [StreamEvent] values, and websocket.Dial opens a WebSocket connection
// through the same client.
//
// # Servers
//
// A [Handler] exposes an [Adapter] as a spec-conformant endpoint:
//
//	http.Handle("/v1/", openresponses.NewHandler(myAdapter))
//
// The handler routes POST /responses and POST /responses/compact,
// validates requests, assigns sequence numbers, frames SSE and maps
// errors to the spec envelope. websocket.Handler wraps it to add the
// WebSocket upgrade on GET /responses.
//
// # Extensions
//
// Item, content, tool, annotation and event types outside the spec are
// slug-prefixed (for example "acme:search_result"). They decode to
// [UnknownItem], [UnknownContent], [UnknownTool], [UnknownAnnotation] and
// [UnknownEvent], which retain the original bytes and re-marshal them
// verbatim, and a decoded [Response] re-encodes without the keys its
// source left out, so a proxy built on this package neither drops nor
// invents data. Packages may register their own types with
// [RegisterItem], [RegisterContent], [RegisterTool], [RegisterAnnotation]
// and [RegisterEvent].
//
// # Streaming lifecycle
//
// This section is the normative statement of the event ordering this
// package produces and expects; [Emitter] produces it, [Accumulator]
// folds it, and streamtest.Validate checks it. A stream:
//
//   - begins with response.created and ends with exactly one of
//     response.completed, response.incomplete or response.failed, with
//     nothing after it;
//   - numbers events from zero, increasing by one;
//   - follows an error event immediately with response.failed over SSE,
//     while over WebSocket the error frame alone ends the turn;
//   - adds output items at consecutive output_index values, one open at a
//     time, and closes each with output_item.done carrying the same id and
//     type before the next is added;
//   - inside a message, adds content parts at consecutive content_index
//     values, one open at a time, streams deltas that name the item id and
//     both indices, sends output_text.done or refusal.done, then
//     content_part.done, before the item is closed;
//   - inside a reasoning item, does the same with summary_index for
//     summary parts and reasoning.done for reasoning text;
//   - inside a function call, sends function_call_arguments.done after the
//     deltas and before the item is closed;
//   - carries, in the terminal response, exactly the items that were
//     added, in order, in their final form.
//
// # Beyond the wire
//
// Nothing in this section is required for conformance. It exists so that
// adapters do not each reimplement the same protocol mechanics, and the
// wire types above stand alone without it.
//
//   - [Emitter] streams a response on an adapter's behalf, owning the
//     lifecycle bookends, indices, item IDs and the response snapshot.
//   - [Accumulator] folds a stream of events back into a [Response]; the
//     client, [CollectStream] and the handler use it.
//   - [CollectStream] derives a non-streaming Create from a streaming
//     adapter, and [Events] exposes a streaming adapter as an in-process
//     iterator, the pull-shaped counterpart of [Client.CreateStream].
//   - [ClientAdapter], from [Client.AsAdapter], serves a remote server as
//     an [Adapter], so every participant, local backend, remote server or
//     a wrapper over either, composes through the same interface.
//   - [ResponseStore], with [MemoryStore] as the bounded default, lets the
//     [Handler] resolve previous_response_id over every transport through
//     [WithResponseStore]. Without it, HTTP requests reach the adapter
//     with the field untouched while WebSocket connections resolve their
//     own recent responses regardless, so whether a stale ID yields
//     previous_response_not_found or reaches the adapter depends on this
//     option and the transport. [ResolveContinuation], [History] and
//     [StampPreviousID] are the pieces a transport outside this package
//     uses to behave the same way.
//   - [NewResponse], [NewID], the Response.Complete, Incomplete and Fail
//     methods and the error constructors ([InvalidRequest],
//     [PreviousResponseNotFound], [TooManyRequests], [ModelError], ...)
//     build spec-shaped values.
//   - The streamtest package records and validates an adapter's stream
//     in a unit test, and the echo package is a deterministic adapter for
//     tests and the compliance run.
package openresponses

// SpecVersion is the Open Responses specification date this package
// targets. The OpenAPI document for that version is embedded in the test
// data and drives the conformance tests.
const SpecVersion = "2026-04-24"
