package openresponses

import "context"

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
type ClientAdapter struct {
	Client *Client
}

var _ Adapter = (*ClientAdapter)(nil)

// AsAdapter returns the client as an [Adapter]. See [ClientAdapter].
func (c *Client) AsAdapter() *ClientAdapter {
	return &ClientAdapter{Client: c}
}

// Create forwards the request.
func (a *ClientAdapter) Create(ctx context.Context, req Request) (*Response, error) {
	return a.Client.Create(ctx, req)
}

// Compact forwards the request.
func (a *ClientAdapter) Compact(ctx context.Context, req CompactRequest) (*CompactResponse, error) {
	return a.Client.Compact(ctx, req)
}

// CreateStream forwards the request and pumps the upstream events into
// sink.
func (a *ClientAdapter) CreateStream(ctx context.Context, req Request, sink EventSink) error {
	stream, err := a.Client.CreateStream(ctx, req)
	if err != nil {
		return err
	}
	defer stream.Close()
	var lastError *ErrorEvent
	for ev := range stream.Events() {
		if e, ok := ev.(*ErrorEvent); ok {
			lastError = e
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
