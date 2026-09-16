package openresponses

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// proxyChain serves upstream through a Handler, then serves a Handler
// backed by a client of that server, and returns a client of the proxy.
func proxyChain(t *testing.T, upstream Adapter, opts ...HandlerOption) *Client {
	t.Helper()
	origin := httptest.NewServer(NewHandler(upstream))
	t.Cleanup(origin.Close)
	proxy := httptest.NewServer(NewHandler(NewClient(origin.URL).AsAdapter(), opts...))
	t.Cleanup(proxy.Close)
	return NewClient(proxy.URL)
}

func TestClientAdapterProxy(t *testing.T) {
	ctx := testContext(t)
	c := proxyChain(t, echoAdapter{})

	resp, err := c.Create(ctx, Request{Model: "m", Input: Items{UserText("hello")}})
	if err != nil || resp.OutputText() != "hello" {
		t.Fatalf("Create: %v %+v", err, resp)
	}

	stream, err := c.CreateStream(ctx, Request{Model: "m", Input: Items{UserText("streamed")}})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	var seq int64
	n := 0
	for ev := range stream.Events() {
		if ev.Sequence() != seq {
			t.Errorf("sequence %d, want %d", ev.Sequence(), seq)
		}
		seq++
		n++
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	if n != 4 || stream.Response().OutputText() != "streamed" {
		t.Errorf("n=%d response=%+v", n, stream.Response())
	}

	compact, err := c.Compact(ctx, CompactRequest{Model: "m", Input: Items{UserText("x")}})
	if err != nil || len(compact.Output) != 1 {
		t.Fatalf("Compact: %v %+v", err, compact)
	}

	// WebSocket downstream over HTTP upstream.
	conn, err := c.Dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	turn, err := conn.Turn(ctx, Request{Model: "m", Input: Items{UserText("ws")}})
	if err != nil || turn.OutputText() != "ws" {
		t.Fatalf("Turn: %v %+v", err, turn)
	}
}

func TestClientAdapterErrorFidelity(t *testing.T) {
	ctx := testContext(t)
	limited := &fakeAdapter{
		create: func(context.Context, Request) (*Response, error) {
			return nil, TooManyRequests("rate_limited", "slow down").WithHeader("Retry-After", "7")
		},
		stream: func(_ context.Context, req Request, sink EventSink) error {
			if err := sink.Send(&ResponseCreatedEvent{Response: NewResponse(req)}); err != nil {
				return err
			}
			return ModelError("upstream_down", "model unavailable")
		},
	}
	c := proxyChain(t, limited)

	_, err := c.Create(ctx, Request{Model: "m", Input: Items{UserText("x")}})
	var e *Error
	if !errors.As(err, &e) || e.StatusCode != 429 || e.Code != "rate_limited" || e.Headers.Get("Retry-After") != "7" {
		t.Fatalf("Create error = %v", err)
	}

	// A mid-stream upstream failure arrives downstream as exactly one
	// error event followed by response.failed.
	stream, err := c.CreateStream(ctx, Request{Model: "m", Input: Items{UserText("x")}})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	var types []string
	for ev := range stream.Events() {
		types = append(types, ev.EventType())
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{EventResponseCreated, EventError, EventResponseFailed}
	if len(types) != len(want) {
		t.Fatalf("events = %v", types)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Errorf("event %d = %s, want %s", i, types[i], want[i])
		}
	}
	if final := stream.Response(); final.Error == nil || final.Error.Code != "upstream_down" || final.Error.Type != ErrorTypeModelError {
		t.Errorf("final = %+v", final)
	}
}

func TestClientAdapterTruncatedUpstream(t *testing.T) {
	ctx := testContext(t)
	// An upstream that emits an error event and drops the connection.
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"r\"}}\n\n")
		_, _ = io.WriteString(w, "event: error\ndata: {\"type\":\"error\",\"sequence_number\":1,\"error\":{\"type\":\"too_many_requests\",\"code\":\"quota\",\"message\":\"out\",\"param\":null}}\n\n")
	}))
	defer origin.Close()
	proxy := httptest.NewServer(NewHandler(NewClient(origin.URL).AsAdapter()))
	defer proxy.Close()

	stream, err := NewClient(proxy.URL).CreateStream(ctx, Request{Model: "m", Input: Items{UserText("x")}})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	final, err := stream.Wait()
	if !errors.Is(err, &Error{Code: "quota"}) {
		t.Fatalf("err = %v", err)
	}
	if final == nil || final.Status != ResponseStatusFailed || final.Error.Code != "quota" {
		t.Errorf("final = %+v", final)
	}
	// The downstream stream is still well-formed: it ends with
	// response.failed and [DONE].
	res, err := http.Post(proxy.URL+"/responses", "application/json", jsonBody(Request{Model: "m", Input: Items{UserText("x")}, Stream: true}))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	frames, done := readFrames(t, string(body))
	if !done || frames[len(frames)-1].Event != EventResponseFailed {
		t.Errorf("frames = %+v done=%v", frames, done)
	}
}

func jsonBody(v any) io.Reader {
	data, _ := json.Marshal(v)
	return bytesReader(data)
}
