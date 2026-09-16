package openresponses

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeAdapter is a scriptable adapter for handler tests.
type fakeAdapter struct {
	UnsupportedCompaction
	create func(ctx context.Context, req Request) (*Response, error)
	stream func(ctx context.Context, req Request, sink EventSink) error
}

func (f *fakeAdapter) Create(ctx context.Context, req Request) (*Response, error) {
	if f.create == nil {
		return CollectStream(ctx, f, req)
	}
	return f.create(ctx, req)
}

func (f *fakeAdapter) CreateStream(ctx context.Context, req Request, sink EventSink) error {
	return f.stream(ctx, req, sink)
}

// helloStream is a well-formed streaming adapter body.
func helloStream(_ context.Context, req Request, sink EventSink) error {
	resp := NewResponse(req)
	resp.ID = "resp_hello"
	if err := sink.Send(&ResponseCreatedEvent{Response: resp}); err != nil {
		return err
	}
	msg := &Message{ID: "msg_1", Status: StatusInProgress, Role: RoleAssistant}
	if err := sink.Send(&OutputItemAddedEvent{Item: msg}); err != nil {
		return err
	}
	if err := sink.Send(&ContentPartAddedEvent{ItemID: msg.ID, Part: &OutputText{}}); err != nil {
		return err
	}
	if err := sink.Send(&OutputTextDeltaEvent{ItemID: msg.ID, Delta: "hello"}); err != nil {
		return err
	}
	msg.Content = Contents{&OutputText{Text: "hello"}}
	msg.Status = StatusCompleted
	if err := sink.Send(&OutputItemDoneEvent{Item: msg}); err != nil {
		return err
	}
	resp.Output = Items{msg}
	resp.Status = ResponseStatusCompleted
	return sink.Send(&ResponseCompletedEvent{Response: resp})
}

func postJSON(t *testing.T, h http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeErrorEnvelope(t *testing.T, rec *httptest.ResponseRecorder) ErrorPayload {
	t.Helper()
	var env errorEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("body %q is not an error envelope: %v", rec.Body.String(), err)
	}
	return env.Error
}

func TestHandlerCreate(t *testing.T) {
	h := NewHandler(&fakeAdapter{stream: helloStream})
	rec := postJSON(t, h, "/v1/responses", `{"model":"m","input":"hi"}`)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	var resp Response
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.OutputText() != "hello" || resp.Status != ResponseStatusCompleted {
		t.Errorf("resp = %+v", resp)
	}
	assertSchemaBytes(t, "ResponseResource", rec.Body.Bytes())
}

func TestHandlerRouting(t *testing.T) {
	h := NewHandler(&fakeAdapter{stream: helloStream})
	tests := []struct {
		name   string
		method string
		path   string
		body   string
		status int
		code   string
	}{
		{"unknown path", http.MethodPost, "/v1/nope", `{}`, 404, "not_found"},
		{"get responses without upgrade", http.MethodGet, "/v1/responses", ``, 405, "method_not_allowed"},
		{"get compact", http.MethodGet, "/v1/responses/compact", ``, 405, "method_not_allowed"},
		{"invalid json", http.MethodPost, "/v1/responses", `{"model":`, 400, "invalid_json"},
		{"empty body", http.MethodPost, "/v1/responses", ``, 400, "invalid_json"},
		{"missing model", http.MethodPost, "/v1/responses", `{"input":"hi"}`, 400, CodeMissingRequiredParameter},
		{"compact missing model", http.MethodPost, "/v1/responses/compact", `{"input":"hi"}`, 400, CodeMissingRequiredParameter},
		{"compaction unsupported", http.MethodPost, "/v1/responses/compact", `{"model":"m","input":"hi"}`, 400, CodeCompactionNotSupported},
		{"bad content pairing", http.MethodPost, "/responses", `{"model":"m","input":[{"type":"message","role":"user","content":[{"type":"output_text","text":"x"}]}]}`, 400, CodeInvalidValue},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tt.status {
				t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
			}
			payload := decodeErrorEnvelope(t, rec)
			if payload.Code != tt.code {
				t.Errorf("code = %q, want %q (message %q)", payload.Code, tt.code, payload.Message)
			}
		})
	}
}

func TestHandlerBodyLimit(t *testing.T) {
	h := NewHandler(&fakeAdapter{stream: helloStream}, WithMaxBodyBytes(32))
	rec := postJSON(t, h, "/responses", `{"model":"m","input":"`+strings.Repeat("x", 100)+`"}`)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d", rec.Code)
	}
}

func TestHandlerAdapterErrorMapping(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		typ    ErrorType
	}{
		{"spec error", PreviousResponseNotFound("resp_gone"), 404, ErrorTypeNotFound},
		{"status override", &Error{StatusCode: 422, Type: ErrorTypeInvalidRequest, Code: "x", Message: "y"}, 422, ErrorTypeInvalidRequest},
		{"http status interface", statusErr(429), 429, ErrorTypeTooManyRequests},
		{"plain error", errors.New("kaboom"), 500, ErrorTypeServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := NewHandler(&fakeAdapter{create: func(context.Context, Request) (*Response, error) { return nil, tt.err }})
			rec := postJSON(t, h, "/responses", `{"model":"m","input":"hi"}`)
			if rec.Code != tt.status {
				t.Fatalf("status = %d", rec.Code)
			}
			if payload := decodeErrorEnvelope(t, rec); payload.Type != tt.typ {
				t.Errorf("type = %s", payload.Type)
			}
		})
	}
}

func TestHandlerErrorHeaders(t *testing.T) {
	h := NewHandler(&fakeAdapter{
		create: func(context.Context, Request) (*Response, error) {
			return nil, TooManyRequests("rate_limited", "slow down").WithHeader("Retry-After", "30")
		},
		stream: func(_ context.Context, req Request, sink EventSink) error {
			resp := NewResponse(req)
			if err := sink.Send(&ResponseCreatedEvent{Response: resp}); err != nil {
				return err
			}
			return TooManyRequests("rate_limited", "slow down").WithHeader("Retry-After", "30")
		},
	})
	t.Run("json", func(t *testing.T) {
		rec := postJSON(t, h, "/responses", `{"model":"m","input":"hi"}`)
		if rec.Code != 429 || rec.Header().Get("Retry-After") != "30" {
			t.Errorf("status %d headers %v", rec.Code, rec.Header())
		}
		if p := decodeErrorEnvelope(t, rec); p.Headers["Retry-After"] != "30" {
			t.Errorf("payload = %+v", p)
		}
	})
	t.Run("stream", func(t *testing.T) {
		rec := postJSON(t, h, "/responses", `{"model":"m","input":"hi","stream":true}`)
		frames, _ := readFrames(t, rec.Body.String())
		var ev ErrorEvent
		if err := json.Unmarshal([]byte(frames[1].Data), &ev); err != nil {
			t.Fatal(err)
		}
		if ev.Err().Headers.Get("Retry-After") != "30" {
			t.Errorf("error event = %+v", ev)
		}
	})
	t.Run("client", func(t *testing.T) {
		srv := httptest.NewServer(h)
		defer srv.Close()
		_, err := NewClient(srv.URL).Create(context.Background(), Request{Model: "m", Input: Items{UserText("hi")}})
		var e *Error
		if !errors.As(err, &e) || e.Headers.Get("Retry-After") != "30" || !IsRateLimited(err) {
			t.Errorf("err = %v", err)
		}
	})
}

type statusErr int

func (s statusErr) Error() string   { return "status" }
func (s statusErr) HTTPStatus() int { return int(s) }

// readFrames parses an SSE body into (event, data) frames plus whether
// [DONE] was seen.
func readFrames(t *testing.T, body string) ([]sseFrame, bool) {
	t.Helper()
	sc := newSSEScanner(strings.NewReader(body))
	var frames []sseFrame
	done := false
	for {
		f, ok := sc.Next()
		if !ok {
			break
		}
		if f.Data == sseDone {
			done = true
			continue
		}
		frames = append(frames, f)
	}
	return frames, done
}

func TestHandlerStream(t *testing.T) {
	h := NewHandler(&fakeAdapter{stream: helloStream})
	rec := postJSON(t, h, "/v1/responses", `{"model":"m","input":"hi","stream":true}`)
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q", ct)
	}
	if rec.Header().Get("Cache-Control") != "no-cache" || rec.Header().Get("X-Accel-Buffering") != "no" {
		t.Errorf("headers = %v", rec.Header())
	}
	frames, done := readFrames(t, rec.Body.String())
	if !done {
		t.Error("missing [DONE]")
	}
	if len(frames) != 6 || frames[0].Event != EventResponseCreated || frames[5].Event != EventResponseCompleted {
		t.Fatalf("frames = %+v", frames)
	}
	for i, f := range frames {
		var probe struct {
			Type string `json:"type"`
			Seq  int64  `json:"sequence_number"`
		}
		if err := json.Unmarshal([]byte(f.Data), &probe); err != nil {
			t.Fatal(err)
		}
		if probe.Type != f.Event {
			t.Errorf("frame %d event %q != data.type %q", i, f.Event, probe.Type)
		}
		if probe.Seq != int64(i) {
			t.Errorf("frame %d sequence %d", i, probe.Seq)
		}
	}
}

func TestHandlerStreamErrorBeforeFirstFrame(t *testing.T) {
	h := NewHandler(&fakeAdapter{stream: func(context.Context, Request, EventSink) error {
		return InvalidRequest("bad", "nope", "input")
	}})
	rec := postJSON(t, h, "/responses", `{"model":"m","input":"hi","stream":true}`)
	if rec.Code != 400 || !strings.Contains(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("status %d, ct %s", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestHandlerStreamErrorMidStream(t *testing.T) {
	h := NewHandler(&fakeAdapter{stream: func(_ context.Context, req Request, sink EventSink) error {
		resp := NewResponse(req)
		resp.ID = "resp_x"
		if err := sink.Send(&ResponseCreatedEvent{Response: resp}); err != nil {
			return err
		}
		return &Error{Type: ErrorTypeModelError, Code: "model_down", Message: "the model is down"}
	}})
	rec := postJSON(t, h, "/responses", `{"model":"m","input":"hi","stream":true}`)
	if rec.Code != 200 {
		t.Fatalf("status %d", rec.Code)
	}
	frames, done := readFrames(t, rec.Body.String())
	if !done {
		t.Error("missing [DONE]")
	}
	if len(frames) != 3 || frames[1].Event != EventError || frames[2].Event != EventResponseFailed {
		t.Fatalf("frames = %+v", frames)
	}
	assertSchemaBytes(t, "ErrorStreamingEvent", []byte(frames[1].Data))
	assertSchemaBytes(t, "ResponseFailedStreamingEvent", []byte(frames[2].Data))
	var failed ResponseFailedEvent
	if err := json.Unmarshal([]byte(frames[2].Data), &failed); err != nil {
		t.Fatal(err)
	}
	if failed.Response.Status != ResponseStatusFailed || failed.Response.Error.Code != "model_down" || failed.Response.ID != "resp_x" {
		t.Errorf("failed = %+v", failed.Response)
	}
}

func TestHandlerStreamSynthesizesTerminal(t *testing.T) {
	h := NewHandler(&fakeAdapter{stream: func(_ context.Context, req Request, sink EventSink) error {
		resp := NewResponse(req)
		resp.ID = "resp_y"
		return sink.Send(&ResponseCreatedEvent{Response: resp})
	}})
	rec := postJSON(t, h, "/responses", `{"model":"m","input":"hi","stream":true}`)
	frames, done := readFrames(t, rec.Body.String())
	if !done || len(frames) != 2 || frames[1].Event != EventResponseCompleted {
		t.Fatalf("frames = %+v done=%v", frames, done)
	}
}

func TestHandlerSinkRejectsAfterTerminal(t *testing.T) {
	var sendErr error
	h := NewHandler(&fakeAdapter{stream: func(ctx context.Context, req Request, sink EventSink) error {
		if err := helloStream(ctx, req, sink); err != nil {
			return err
		}
		sendErr = sink.Send(&OutputTextDeltaEvent{Delta: "late"})
		return nil
	}})
	postJSON(t, h, "/responses", `{"model":"m","input":"hi","stream":true}`)
	if !errors.Is(sendErr, ErrTerminalEventSent) {
		t.Errorf("err = %v", sendErr)
	}
}

func TestHandlerStreamClientDisconnect(t *testing.T) {
	cancelled := make(chan struct{})
	h := NewHandler(&fakeAdapter{stream: func(ctx context.Context, req Request, sink EventSink) error {
		resp := NewResponse(req)
		resp.ID = "resp_z"
		if err := sink.Send(&ResponseCreatedEvent{Response: resp}); err != nil {
			return err
		}
		<-ctx.Done()
		close(cancelled)
		return ctx.Err()
	}})
	srv := httptest.NewServer(h)
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := NewClient(srv.URL).CreateStream(ctx, Request{Model: "m", Input: Items{UserText("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	if !stream.Next() {
		t.Fatal("no first event")
	}
	cancel()
	_ = stream.Close()
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("adapter context was not cancelled after the client disconnected")
	}
}

func TestHandlerUnsupportedStreaming(t *testing.T) {
	type noStream struct {
		UnsupportedStreaming
		UnsupportedCompaction
	}
	adapter := struct {
		noStream
		createFn func(context.Context, Request) (*Response, error)
	}{createFn: func(context.Context, Request) (*Response, error) { return sampleResponse(), nil }}
	h := NewHandler(adapterFunc{adapter.createFn, adapter.noStream})
	rec := postJSON(t, h, "/responses", `{"model":"m","input":"hi","stream":true}`)
	if rec.Code != 400 || decodeErrorEnvelope(t, rec).Code != CodeStreamingNotSupported {
		t.Errorf("status %d body %s", rec.Code, rec.Body.String())
	}
	_ = io.Discard
}

type adapterFunc struct {
	create func(context.Context, Request) (*Response, error)
	rest   interface {
		CreateStream(context.Context, Request, EventSink) error
		Compact(context.Context, CompactRequest) (*CompactResponse, error)
	}
}

func (a adapterFunc) Create(ctx context.Context, req Request) (*Response, error) {
	return a.create(ctx, req)
}
func (a adapterFunc) CreateStream(ctx context.Context, req Request, sink EventSink) error {
	return a.rest.CreateStream(ctx, req, sink)
}
func (a adapterFunc) Compact(ctx context.Context, req CompactRequest) (*CompactResponse, error) {
	return a.rest.Compact(ctx, req)
}
