package openresponses

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// ClientAdapter serves a remote Open Responses server as an [Adapter], so
// a [Client] composes with local backends: behind a [Handler] it is a
// proxy, inside a wrapper it is a fallback, a recorder's upstream or one
// leg of a fan-out.
//
// Create and Compact forward unchanged, including *Error values, so an
// upstream 429 reaches the downstream caller as a 429 with its headers.
// CreateStream pumps the upstream event stream into the sink:
//
//   - events are forwarded as they arrive, including error and
//     response.failed, so an upstream failure is reported once and in
//     the upstream's own words;
//   - sequence numbers are reassigned by the sink, because they are
//     per-stream and the downstream stream is a new one;
//   - the adapter returns an error only when the upstream transport
//     fails or the stream ends without a terminal event, in which case
//     the handler closes the downstream stream with error and
//     response.failed.
//
// The WebSocket protocol composes for free: the local Handler runs each
// turn through CreateStream, so a proxy serves WebSocket downstream over
// HTTP upstream. When the local Handler has a [ResponseStore] it inlines
// stored history and clears previous_response_id before this adapter
// runs, so the upstream server's own store is bypassed and the proxy
// owns the conversation it proxies.
//
// Two rules of docs/extensions.md apply to the input before it is
// forwarded, as they do in an adapter that maps onto a provider. Media
// whose URL is a sidecar: reference is always refused (section 1.7).
// Once [WithProviderSlugs] names the providers the upstream accepts, an
// input item or content part under any other provider's slug is left
// out, as is a reasoning item such a provider issued (sections 1.6 and
// 4), and a request member or tool under such a slug is refused (section
// 1.5). Without it the adapter cannot know what stands behind the
// upstream and forwards every item, member and tool as given. Items under a slug that names no
// provider, and shared ones, are forwarded unchanged either way. The
// request the caller passed is never changed.
type ClientAdapter struct {
	Client *Client

	// configured is whether WithProviderSlugs was given, and providers
	// are the slugs it named, without the dot.
	configured bool
	providers  []string
}

var _ Adapter = (*ClientAdapter)(nil)

// AdapterOption configures a [ClientAdapter].
type AdapterOption func(*ClientAdapter)

// WithProviderSlugs names the providers whose extension items the
// upstream accepts, by slug ("anthropic", or "anthropic." with the dot):
// an Open Responses server over the Claude adapter takes "anthropic",
// and a server that routes each model to its own adapter takes the slug
// of each, since the adapter it routes to applies docs/extensions.md
// section 1.6 itself. Items, content parts and reasoning under any other
// provider's slug are left out. With no slugs the upstream accepts none,
// and every provider's are left out, which is right for an upstream that
// is a provider's own API. Without this option nothing is left out: an
// upstream that is an Open Responses server over the Claude adapter
// needs Claude's signed reasoning back, and the adapter cannot tell.
func WithProviderSlugs(slugs ...string) AdapterOption {
	return func(a *ClientAdapter) {
		a.configured = true
		for _, s := range slugs {
			a.providers = append(a.providers, strings.TrimSuffix(s, "."))
		}
	}
}

// providerSlugs are the providers this library has adapters for, the
// slugs docs/extensions.md section 1.6 calls provider slugs.
var providerSlugs = []string{"anthropic", "gemini", "chatcompletions"}

// foreign reports whether name, a slug without its dot, names a provider
// other than those the upstream accepts. Nothing is foreign until
// [WithProviderSlugs] says what the upstream accepts.
func (a *ClientAdapter) foreign(name string) bool {
	return a.configured && slices.Contains(providerSlugs, name) && !slices.Contains(a.providers, name)
}

// foreignType reports whether an item or content part type carries the
// slug of a provider the upstream does not accept.
func (a *ClientAdapter) foreignType(typ string) bool {
	name, _, ok := strings.Cut(typ, ".")
	return ok && a.foreign(name)
}

// foreignIssuer reports whether a reasoning item was issued by a
// provider the upstream does not accept: <issuer> or <issuer>/<variant>
// (section 4). An item with no issuer is of unknown origin and is
// forwarded, as an upstream that does not stamp its items needs.
func (a *ClientAdapter) foreignIssuer(issuer string) bool {
	name, _, _ := strings.Cut(issuer, "/")
	return issuer != "" && a.foreign(name)
}

// checkMembers refuses a request member or tool under the slug of a
// provider the upstream does not accept, as docs/extensions.md section
// 1.5 refuses a setting meant for another provider. Members in sorted
// order, so the same request is refused for the same member.
func (a *ClientAdapter) checkMembers(extra map[string]any, tools Tools) error {
	for _, key := range slices.Sorted(maps.Keys(extra)) {
		if a.foreignType(key) {
			return InvalidRequest(CodeUnsupportedParameter,
				key+" is a setting for a provider the upstream does not accept", key)
		}
	}
	for i, t := range tools {
		if a.foreignType(t.ToolType()) {
			param := fmt.Sprintf("tools[%d].type", i)
			return InvalidRequest(CodeUnsupportedParameter,
				t.ToolType()+" is a tool of a provider the upstream does not accept", param)
		}
	}
	return nil
}

// prepare returns the input to forward: the input with what [foreignType]
// and [foreignIssuer] report left out, and a message left with no
// content left out with it. It refuses a sidecar: reference first. The
// items it changes are copies; input is not changed.
func (a *ClientAdapter) prepare(input Items) (Items, error) {
	for i, item := range input {
		if err := sidecarItem(item, fmt.Sprintf("input[%d]", i)); err != nil {
			return nil, err
		}
	}
	var out Items
	for _, item := range input {
		switch it := item.(type) {
		case *UnknownItem:
			if a.foreignType(it.Type) {
				continue
			}
		case *ReasoningItem:
			if a.foreignIssuer(it.Issuer()) {
				continue
			}
		case *Message:
			if kept, changed := a.keep(it.Content); changed {
				if len(kept) == 0 {
					continue
				}
				cp := *it
				cp.Content = kept
				item = &cp
			}
		case *FunctionCallOutput:
			if kept, changed := a.keep(it.Output.Parts); changed {
				// The output is sent without the part: its call needs an
				// answer.
				cp := *it
				cp.Output.Parts = kept
				item = &cp
			}
		}
		out = append(out, item)
	}
	return out, nil
}

// keep returns contents without the parts [foreignType] reports, and
// whether any was left out.
func (a *ClientAdapter) keep(contents Contents) (Contents, bool) {
	kept := Contents{}
	for _, c := range contents {
		if u, ok := c.(*UnknownContent); ok && a.foreignType(u.Type) {
			continue
		}
		kept = append(kept, c)
	}
	return kept, len(kept) != len(contents)
}

// sidecarItem refuses a sidecar: reference in the media of a message or
// a function output (docs/extensions.md section 1.7). The reference
// names a blob in a session store, which the writer resolves before it
// sends; no upstream can fetch it.
func sidecarItem(item Item, p string) error {
	var contents Contents
	switch it := item.(type) {
	case *Message:
		contents, p = it.Content, p+".content"
	case *FunctionCallOutput:
		contents, p = it.Output.Parts, p+".output"
	}
	for j, c := range contents {
		pp := fmt.Sprintf("%s[%d]", p, j)
		var fields [][2]string
		switch c := c.(type) {
		case *InputImage:
			fields = [][2]string{{"image_url", c.ImageURL}}
		case *InputFile:
			fields = [][2]string{{"file_url", c.FileURL}, {"file_data", c.FileData}}
		case *InputVideo:
			fields = [][2]string{{"video_url", c.VideoURL}}
		}
		for _, f := range fields {
			if len(f[1]) >= len("sidecar:") && strings.EqualFold(f[1][:len("sidecar:")], "sidecar:") {
				param := pp + "." + f[0]
				return InvalidRequest(CodeInvalidValue, param+" is a sidecar: reference to a session store's blob; the writer resolves it to the media's bytes before sending, and the adapter never forwards it", param)
			}
		}
	}
	return nil
}

// AsAdapter returns the client as an [Adapter]. See [ClientAdapter].
func (c *Client) AsAdapter(opts ...AdapterOption) *ClientAdapter {
	a := &ClientAdapter{Client: c}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// Create forwards the request.
func (a *ClientAdapter) Create(ctx context.Context, req Request) (*Response, error) {
	if err := a.checkMembers(req.Extra, req.Tools); err != nil {
		return nil, err
	}
	input, err := a.prepare(req.Input)
	if err != nil {
		return nil, err
	}
	req.Input = input
	return a.Client.Create(ctx, req)
}

// Compact forwards the request.
func (a *ClientAdapter) Compact(ctx context.Context, req CompactRequest) (*CompactResponse, error) {
	if err := a.checkMembers(req.Extra, nil); err != nil {
		return nil, err
	}
	input, err := a.prepare(req.Input)
	if err != nil {
		return nil, err
	}
	req.Input = input
	return a.Client.Compact(ctx, req)
}

// CreateStream forwards the request and pumps the upstream events into
// sink. An error event is forwarded with the header filter every error
// from a peer gets (see [Error.Headers]): its payload keeps only the
// headers that describe a failure, and no other spelling of headers.
func (a *ClientAdapter) CreateStream(ctx context.Context, req Request, sink EventSink) error {
	if err := a.checkMembers(req.Extra, req.Tools); err != nil {
		return err
	}
	input, err := a.prepare(req.Input)
	if err != nil {
		return err
	}
	req.Input = input
	stream, err := a.Client.CreateStream(ctx, req)
	if err != nil {
		return err
	}
	defer stream.Close()
	var lastError *ErrorEvent
	for ev := range stream.Events() {
		if e, ok := ev.(*ErrorEvent); ok {
			cp := *e
			cp.Error = e.Error.forwardable()
			cp.Extra = cloneExtra(e.Extra)
			lastError = &cp
			ev = &cp
		}
		if err := sink.Send(ev); err != nil {
			return err
		}
	}
	err = stream.Err()
	if err == nil {
		return nil
	}
	// An upstream that sent an error event and then dropped the stream
	// has already said why; surface that reason rather than the
	// truncation so the downstream caller sees the real cause.
	if lastError != nil && err == ErrTruncatedStream {
		return lastError.Err()
	}
	return err
}
