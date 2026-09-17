package openresponses

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"
)

// Adapter is what a backend implements to be served by [Handler]. Errors
// returned as *Error (or implementing HTTPStatus() int) map to the
// matching envelope; anything else becomes a server_error.
type Adapter interface {
	// Create produces a complete response.
	Create(ctx context.Context, req Request) (*Response, error)
	// CreateStream emits events to sink and returns when the response is
	// terminal. The sink assigns sequence numbers.
	CreateStream(ctx context.Context, req Request, sink EventSink) error
	// Compact produces a compacted conversation. When the handler has a
	// [ResponseStore] it resolves req.PreviousResponseID first and the
	// adapter sees a self-contained input; otherwise resolving it is the
	// adapter's job: look it up in your own store, or return
	// [PreviousResponseNotFound].
	Compact(ctx context.Context, req CompactRequest) (*CompactResponse, error)
}

// EventSink receives streaming events from an adapter.
type EventSink interface {
	// Send delivers one event. It returns an error when the client has
	// gone away or a terminal event was already sent; the adapter should
	// stop.
	Send(ev StreamEvent) error
}

// EventSinkFunc adapts a function to the EventSink interface.
type EventSinkFunc func(ev StreamEvent) error

// Send calls f.
func (f EventSinkFunc) Send(ev StreamEvent) error { return f(ev) }

// Streamer is the subset of [Adapter] needed by [CollectStream].
type Streamer interface {
	CreateStream(ctx context.Context, req Request, sink EventSink) error
}

// CollectStream runs a streaming adapter and folds its events into the
// final response, so an adapter can implement Create as
//
//	func (a *A) Create(ctx context.Context, req Request) (*Response, error) {
//		return openresponses.CollectStream(ctx, a, req)
//	}
func CollectStream(ctx context.Context, s Streamer, req Request) (*Response, error) {
	var acc Accumulator
	err := s.CreateStream(ctx, req, EventSinkFunc(func(ev StreamEvent) error {
		acc.Add(ev)
		return nil
	}))
	if err != nil {
		return nil, err
	}
	resp := acc.Response()
	if resp == nil {
		return nil, errors.New("openresponses: adapter produced no response")
	}
	if resp.Status == ResponseStatusFailed && resp.Error != nil {
		return resp, &Error{Type: resp.Error.Type, Code: resp.Error.Code, Message: resp.Error.Message, Param: resp.Error.Param}
	}
	return resp, nil
}

// Events runs a streaming adapter in-process and yields its events as
// they arrive, the pull-shaped counterpart of [Client.CreateStream]:
//
//	for ev, err := range openresponses.Events(ctx, adapter, req) {
//		...
//	}
//
// The sequence ends after the terminal event, or with a non-nil error
// when the adapter fails. Breaking out of the loop cancels the adapter,
// whose next Send returns the cancellation. Events are delivered through
// their wire form, so the consumer sees what a client would and later
// mutation by the adapter is invisible.
func Events(ctx context.Context, s Streamer, req Request) iter.Seq2[StreamEvent, error] {
	return func(yield func(StreamEvent, error) bool) {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		events := make(chan StreamEvent)
		done := make(chan error, 1)
		go func() {
			done <- s.CreateStream(ctx, req, EventSinkFunc(func(ev StreamEvent) error {
				data, err := EncodeEvent(ev)
				if err != nil {
					return err
				}
				copy, err := DecodeEvent(data)
				if err != nil {
					return err
				}
				select {
				case events <- copy:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}))
		}()
		for {
			select {
			case ev := <-events:
				if !yield(ev, nil) {
					cancel()
					<-done
					return
				}
			case err := <-done:
				if err != nil {
					yield(nil, err)
				}
				return
			}
		}
	}
}

// UnsupportedStreaming can be embedded by adapters that do not stream.
type UnsupportedStreaming struct{}

// CreateStream returns an invalid_request error.
func (UnsupportedStreaming) CreateStream(context.Context, Request, EventSink) error {
	return InvalidRequest(CodeStreamingNotSupported, "streaming is not supported", "stream")
}

// UnsupportedCompaction can be embedded by adapters that do not compact.
type UnsupportedCompaction struct{}

// Compact returns an invalid_request error.
func (UnsupportedCompaction) Compact(context.Context, CompactRequest) (*CompactResponse, error) {
	return nil, InvalidRequest(CodeCompactionNotSupported, "compaction is not supported", "")
}

// Handler serves an [Adapter] over HTTP. It routes POST .../responses,
// POST .../responses/compact and the WebSocket upgrade on GET
// .../responses, so it can be mounted under any prefix:
//
//	http.Handle("/v1/", openresponses.NewHandler(adapter))
type Handler struct {
	adapter      Adapter
	store        ResponseStore
	maxBodyBytes int64
	ws           webSocketConfig
}

// HandlerOption configures a Handler.
type HandlerOption func(*Handler)

// WithResponseStore makes the handler resolve previous_response_id
// against store over every transport and save the history of every
// stored response into it. See [ResponseStore].
//
// This option changes what clients observe, not just how the server is
// wired. With a store, an unknown previous_response_id is answered with
// previous_response_not_found on every transport. Without one, HTTP
// requests reach the adapter with the field untouched and the adapter
// must resolve or reject it, while WebSocket connections still resolve
// their own recent responses from connection memory as the spec asks.
// Adapters that do not keep history should reject an unresolved
// previous_response_id with [PreviousResponseNotFound] rather than
// assume a store is configured.
func WithResponseStore(store ResponseStore) HandlerOption {
	return func(h *Handler) { h.store = store }
}

// WithMaxBodyBytes caps request bodies and WebSocket frames. The default
// is 16 MiB, room for a request with several base64 file parts; a body
// is held in memory whole and parsed more than once, so raise it only as
// far as the inputs you expect need.
func WithMaxBodyBytes(n int64) HandlerOption {
	return func(h *Handler) { h.maxBodyBytes = n }
}

// WithWebSocketKeepalive sets how often the server pings a WebSocket
// client. A client that does not answer within the next interval is
// disconnected, so a peer that vanished without closing does not hold
// its connection, cache and goroutines until the lifetime expires. The
// default is 30 seconds; zero disables pings. An idle client is not
// affected: pings are answered from inside its read call, which
// [WebSocketConn] keeps running between turns.
func WithWebSocketKeepalive(d time.Duration) HandlerOption {
	return func(h *Handler) { h.ws.keepalive = d }
}

// WithWebSocketLifetime sets the maximum lifetime of a WebSocket
// connection. The spec fixes it at 60 minutes; shorter values are useful
// in tests.
func WithWebSocketLifetime(d time.Duration) HandlerOption {
	return func(h *Handler) { h.ws.lifetime = d }
}

// WithWebSocketOrigins sets the origin patterns accepted for WebSocket
// upgrades, as understood by github.com/coder/websocket. By default only
// same-origin requests and requests without an Origin header are
// accepted.
func WithWebSocketOrigins(patterns ...string) HandlerOption {
	return func(h *Handler) { h.ws.originPatterns = patterns }
}

// WithWebSocketCacheSize sets how many recent responses a WebSocket
// connection remembers for previous_response_id continuation. The
// default is 4.
func WithWebSocketCacheSize(n int) HandlerOption {
	return func(h *Handler) { h.ws.cacheSize = n }
}

// NewHandler returns a handler serving adapter.
func NewHandler(adapter Adapter, opts ...HandlerOption) *Handler {
	h := &Handler{
		adapter:      adapter,
		maxBodyBytes: 16 << 20,
		ws: webSocketConfig{
			lifetime:  60 * time.Minute,
			keepalive: 30 * time.Second,
			cacheSize: 4,
		},
	}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// ServeHTTP routes the request by path suffix.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := path.Clean("/" + r.URL.Path)
	switch {
	case strings.HasSuffix(p, "/responses/compact"):
		h.serveCompact(w, r)
	case strings.HasSuffix(p, "/responses"):
		h.serveResponses(w, r)
	default:
		writeError(w, NotFound("not_found", "no such endpoint", ""))
	}
}

func (h *Handler) serveResponses(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && isWebSocketUpgrade(r) {
		h.serveWebSocket(w, r)
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, &Error{StatusCode: http.StatusMethodNotAllowed, Type: ErrorTypeInvalidRequest, Code: "method_not_allowed", Message: "use POST"})
		return
	}
	var req Request
	if err := h.decodeBody(w, r, &req); err != nil {
		writeError(w, err)
		return
	}
	if err := req.Validate(); err != nil {
		writeError(w, err)
		return
	}
	if req.Background {
		// The handler runs every response to completion before answering,
		// so it cannot honour a request to return immediately; saying so
		// beats returning a finished response labelled background.
		writeError(w, InvalidRequest(CodeUnsupportedParameter, "background responses are not supported", "background"))
		return
	}
	cont, err := h.resolve(r.Context(), &req)
	if err != nil {
		writeError(w, err)
		return
	}
	if req.Stream {
		h.streamResponse(w, r, req, cont)
		return
	}
	resp, err := h.adapter.Create(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	if cont.resolved && resp.PreviousResponseID == nil {
		resp.PreviousResponseID = &cont.id
	}
	if err := h.save(r.Context(), req, resp); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// resolve applies the shared store to a request's previous_response_id.
// With no store the request passes through untouched.
func (h *Handler) resolve(ctx context.Context, req *Request) (continuation, error) {
	if h.store == nil {
		return continuation{id: req.PreviousResponseID}, nil
	}
	cont, err := resolveContinuation(ctx, req, h.store)
	if err != nil {
		return cont, err
	}
	if cont.id != "" && !cont.resolved {
		return cont, PreviousResponseNotFound(cont.id)
	}
	return cont, nil
}

// save records a stored response that reached completed or incomplete in
// the shared store. The save outlives the request's context: a client
// that disconnects after the terminal event still produced a response it
// may continue from.
func (h *Handler) save(ctx context.Context, req Request, resp *Response) error {
	if h.store == nil || resp == nil || resp.ID == "" || !req.Stored() {
		return nil
	}
	if !resp.Status.Terminal() || resp.Status == ResponseStatusFailed {
		return nil
	}
	if err := h.store.Save(context.WithoutCancel(ctx), resp.ID, history(req, resp)); err != nil {
		return fmt.Errorf("save response %q: %w", resp.ID, err)
	}
	return nil
}

func (h *Handler) serveCompact(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, &Error{StatusCode: http.StatusMethodNotAllowed, Type: ErrorTypeInvalidRequest, Code: "method_not_allowed", Message: "use POST"})
		return
	}
	var req CompactRequest
	if err := h.decodeBody(w, r, &req); err != nil {
		writeError(w, err)
		return
	}
	if err := req.Validate(); err != nil {
		writeError(w, err)
		return
	}
	if h.store != nil && req.PreviousResponseID != "" {
		probe := Request{Input: req.Input, PreviousResponseID: req.PreviousResponseID}
		cont, err := resolveContinuation(r.Context(), &probe, h.store)
		if err != nil {
			writeError(w, err)
			return
		}
		if !cont.resolved {
			writeError(w, PreviousResponseNotFound(cont.id))
			return
		}
		req.Input = probe.Input
		req.PreviousResponseID = ""
	}
	resp, err := h.adapter.Compact(r.Context(), req)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// streamResponse runs the adapter against an SSE sink and finishes the
// stream according to the spec: an adapter error after the first frame
// becomes error + response.failed; a missing terminal event is
// synthesized; every stream ends with [DONE].
func (h *Handler) streamResponse(w http.ResponseWriter, r *http.Request, req Request, cont continuation) {
	sink := newSSESink(w, req)
	sink.previousID = cont.id
	// The response is saved before its terminal event is written, so a
	// client that continues from it the moment the event arrives finds
	// it. A store failure cannot be reported inside a stream that is
	// already running, so it is dropped here; a store that can fail
	// should log inside Save.
	sink.beforeTerminal = func(resp *Response) { _ = h.save(r.Context(), req, resp) }
	err := h.adapter.CreateStream(r.Context(), req, sink)
	if err != nil && !sink.started() {
		if r.Context().Err() == nil {
			writeError(w, err)
		}
		return
	}
	sink.finish(err)
}

// sseSink is the EventSink for HTTP streaming.
type sseSink struct {
	w          *sseWriter
	req        Request
	previousID string
	// beforeTerminal runs with the final response just before the
	// terminal event is written.
	beforeTerminal func(*Response)

	mu       sync.Mutex
	seq      int64
	acc      Accumulator
	sent     bool
	terminal bool
	failed   error
}

func newSSESink(w http.ResponseWriter, req Request) *sseSink {
	return &sseSink{w: newSSEWriter(w), req: req}
}

// Send assigns the sequence number, writes the frame and records the
// response state.
func (s *sseSink) Send(ev StreamEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed != nil {
		return s.failed
	}
	if s.terminal {
		return ErrTerminalEventSent
	}
	if err := s.writeLocked(ev); err != nil {
		return err
	}
	return nil
}

func (s *sseSink) writeLocked(ev StreamEvent) error {
	if setter, ok := ev.(SequenceSetter); ok {
		setter.SetSequence(s.seq)
	}
	s.seq++
	stampPreviousID(ev, s.previousID)
	data, err := EncodeEvent(ev)
	if err != nil {
		return fmt.Errorf("openresponses: encode %s: %w", ev.EventType(), err)
	}
	s.acc.Add(ev)
	if _, ok := TerminalResponse(ev); ok {
		s.terminal = true
		if s.beforeTerminal != nil {
			s.beforeTerminal(s.acc.Response())
		}
	}
	if err := s.w.WriteEvent(ev.EventType(), data); err != nil {
		s.failed = fmt.Errorf("openresponses: write event: %w", err)
		return s.failed
	}
	s.sent = true
	return nil
}

func (s *sseSink) started() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sent
}

// finish closes out the stream after the adapter returns: an adapter
// error becomes error + response.failed, a missing terminal event is
// synthesized, and [DONE] ends the stream. When a write already failed
// the stream is left as it is.
func (s *sseSink) finish(adapterErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed != nil {
		return
	}
	if adapterErr != nil && !s.terminal {
		e := AsError(adapterErr)
		payload := e.Payload()
		_ = s.writeLocked(&ErrorEvent{Error: payload})
		resp := s.acc.Response()
		if resp == nil {
			resp = NewResponse(s.req)
			resp.ID = NewID("resp")
		}
		resp.Fail(e)
		_ = s.writeLocked(&ResponseFailedEvent{Response: resp})
	} else if !s.terminal {
		resp := s.acc.Response()
		if resp == nil {
			resp = NewResponse(s.req)
			resp.ID = NewID("resp")
		}
		resp.Complete()
		_ = s.writeLocked(&ResponseCompletedEvent{Response: resp})
	}
	_ = s.w.WriteDone()
}

// decodeBody reads a JSON body into v, returning an invalid_request
// error for malformed input.
func (h *Handler) decodeBody(w http.ResponseWriter, r *http.Request, v any) error {
	body := http.MaxBytesReader(w, r.Body, h.maxBodyBytes)
	defer func() { _ = body.Close() }()
	data, err := io.ReadAll(body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return &Error{StatusCode: http.StatusRequestEntityTooLarge, Type: ErrorTypeInvalidRequest, Code: "request_too_large", Message: "request body too large"}
		}
		return InvalidRequest("invalid_body", "could not read request body", "")
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		return InvalidRequest("invalid_json", "request body is empty", "")
	}
	if err := json.Unmarshal(data, v); err != nil {
		return InvalidRequest("invalid_json", "request body is not valid JSON: "+err.Error(), "")
	}
	return nil
}

// writeError writes the spec error envelope with the status derived from
// err, plus any headers the error carries.
func writeError(w http.ResponseWriter, err error) {
	e := AsError(err)
	for k, vs := range e.Headers {
		switch http.CanonicalHeaderKey(k) {
		case "Content-Length", "Content-Type", "Transfer-Encoding", "Connection":
			// The envelope written below owns these.
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	writeJSON(w, e.HTTPStatus(), errorEnvelope{Error: e.Payload()})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		e := &Error{Type: ErrorTypeServerError, Code: "encode_failed", Message: err.Error()}
		data, _ = json.Marshal(errorEnvelope{Error: e.Payload()})
		status = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(data)
}

func isWebSocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

// NewID returns a random identifier with the given prefix, such as
// "resp_3f9a...". Adapters may use it for response and item IDs.
func NewID(prefix string) string {
	var b [12]byte
	rand.Read(b[:]) // never fails since Go 1.24
	return prefix + "_" + hex.EncodeToString(b[:])
}
