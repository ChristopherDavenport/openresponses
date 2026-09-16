// Package openresponses implements the Open Responses specification
// (https://www.openresponses.org) on the wire, in both directions.
//
// The package models the request and response envelopes, every item,
// content part, tool and streaming event defined by the specification,
// and the three transports: JSON over HTTP, Server-Sent Events, and
// WebSocket. It is deliberately small: there is no agent loop, no tool
// runner and no retry policy. Those are consumers of this package.
//
// # Clients
//
// A [Client] talks to any Open Responses server:
//
//	c := openresponses.NewClient("https://api.openai.com/v1", openresponses.WithAPIKey(key))
//	resp, err := c.Create(ctx, openresponses.Request{
//		Model: "gpt-5",
//		Input: openresponses.Input{openresponses.UserText("Hello")},
//	})
//	fmt.Println(resp.OutputText())
//
// [Client.CreateStream] returns an [EventStream] that yields decoded
// [StreamEvent] values, and [Client.Dial] opens a WebSocket connection.
//
// # Servers
//
// A [Handler] exposes an [Adapter] as a spec-conformant endpoint:
//
//	http.Handle("/v1/", openresponses.NewHandler(myAdapter))
//
// The handler routes POST /responses, POST /responses/compact and the
// WebSocket upgrade on GET /responses, validates requests, assigns
// sequence numbers, frames SSE and maps errors to the spec envelope.
//
// # Extensions
//
// Item, content, tool, annotation and event types outside the spec are
// slug-prefixed (for example "acme:search_result"). They decode to
// [UnknownItem], [UnknownContent], [UnknownTool], [UnknownAnnotation] and
// [UnknownEvent], which retain the original bytes and re-marshal them
// verbatim, so a proxy built on this package never drops data. Packages
// may register their own types with [RegisterItem], [RegisterContent],
// [RegisterTool], [RegisterAnnotation] and [RegisterEvent].
package openresponses

// SpecVersion is the Open Responses specification date this package
// targets. The OpenAPI document for that version is embedded in the test
// data and drives the conformance tests.
const SpecVersion = "2026-04-24"
