// Package websocket is the WebSocket transport of the Open Responses
// specification, kept apart from the root package so that code which
// only needs the wire vocabulary does not build a WebSocket library.
//
// On the server, [Handler] wraps an [openresponses.Handler] and adds the
// upgrade on GET .../responses:
//
//	h := openresponses.NewHandler(adapter, openresponses.WithResponseStore(store))
//	http.Handle("/v1/", websocket.Handler(h, websocket.WithOrigins("app.example")))
//
// On the client, [Dial] opens a connection through an
// [openresponses.Client] and returns a [Conn] that runs turns one at a
// time:
//
//	conn, err := websocket.Dial(ctx, client)
//	resp, err := conn.Turn(ctx, req)
//
// The transport uses github.com/coder/websocket underneath.
package websocket

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	ws "github.com/coder/websocket"

	"github.com/ChristopherDavenport/openresponses"
)

// config holds the server-side settings.
type config struct {
	lifetime       time.Duration
	keepalive      time.Duration
	originPatterns []string
	cacheSize      int
}

// Option configures [Handler].
type Option func(*config)

// WithKeepalive sets how often the server pings a client. A client that
// does not answer within the next interval is disconnected, so a peer
// that vanished without closing does not hold its connection, cache and
// goroutines until the lifetime expires. The default is 30 seconds;
// zero disables pings. An idle client is not affected: pings are
// answered from inside its read call, which [Conn] keeps running
// between turns.
func WithKeepalive(d time.Duration) Option {
	return func(c *config) { c.keepalive = d }
}

// WithLifetime sets the maximum lifetime of a connection. The spec fixes
// it at 60 minutes; shorter values are useful in tests.
func WithLifetime(d time.Duration) Option {
	return func(c *config) { c.lifetime = d }
}

// WithOrigins sets the origin patterns accepted for upgrades, as
// understood by github.com/coder/websocket. By default only same-origin
// requests and requests without an Origin header are accepted.
func WithOrigins(patterns ...string) Option {
	return func(c *config) { c.originPatterns = patterns }
}

// WithCacheSize sets how many recent responses a connection remembers
// for previous_response_id continuation. The default is 4.
func WithCacheSize(n int) Option {
	return func(c *config) { c.cacheSize = n }
}

// Handler serves h over HTTP and, on GET .../responses with an Upgrade
// header, over WebSocket. The WebSocket session runs turns against the
// handler's adapter, resolves previous_response_id against connection
// memory first and the handler's [openresponses.ResponseStore] second,
// and caps frames at the handler's body limit. Every other request is
// passed to h unchanged, so the result mounts wherever h would:
//
//	http.Handle("/v1/", websocket.Handler(openresponses.NewHandler(adapter)))
func Handler(h *openresponses.Handler, opts ...Option) http.Handler {
	cfg := config{
		lifetime:  60 * time.Minute,
		keepalive: 30 * time.Second,
		cacheSize: 4,
	}
	for _, opt := range opts {
		opt(&cfg)
	}
	return &handler{next: h, cfg: cfg}
}

type handler struct {
	next *openresponses.Handler
	cfg  config
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && isUpgrade(r) && strings.HasSuffix(path.Clean("/"+r.URL.Path), "/responses") {
		h.serve(w, r)
		return
	}
	h.next.ServeHTTP(w, r)
}

func isUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

// errorFrame is the wire form of an error frame.
type errorFrame struct {
	Type   string                     `json:"type"`
	Status int                        `json:"status"`
	Error  openresponses.ErrorPayload `json:"error"`
}

// createFrame is the client frame that starts a turn. The forbidden
// HTTP-only fields are captured raw so their presence can be rejected.
type createFrame struct {
	Type          string          `json:"type"`
	Stream        json.RawMessage `json:"stream"`
	StreamOptions json.RawMessage `json:"stream_options"`
	Background    json.RawMessage `json:"background"`
}

// forbidden lists the fields a response.create frame must not carry, in
// the order they are reported.
func (c createFrame) forbidden() []struct {
	name string
	raw  json.RawMessage
} {
	return []struct {
		name string
		raw  json.RawMessage
	}{
		{"stream", c.Stream},
		{"stream_options", c.StreamOptions},
		{"background", c.Background},
	}
}

// serve upgrades the connection and runs turns until the client
// disconnects or the lifetime expires.
func (h *handler) serve(w http.ResponseWriter, r *http.Request) {
	conn, err := ws.Accept(w, r, &ws.AcceptOptions{
		OriginPatterns: h.cfg.originPatterns,
	})
	if err != nil {
		// Accept has already written an HTTP error response.
		return
	}
	conn.SetReadLimit(h.next.MaxBodyBytes())
	session := &session{
		adapter: h.next.Adapter(),
		store:   h.next.Store(),
		conn:    conn,
		cache:   openresponses.NewMemoryStore(h.cfg.cacheSize),
	}
	session.run(r.Context(), h.cfg)
}

// session is one server-side connection. cache is the connection-local
// memory the spec asks for; store is the handler's shared store,
// consulted second.
type session struct {
	adapter openresponses.Adapter
	store   openresponses.ResponseStore
	conn    *ws.Conn
	cache   *openresponses.MemoryStore
}

// frame is one message read from a connection.
type frame struct {
	typ  ws.MessageType
	data []byte
}

func (s *session) run(parent context.Context, cfg config) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	defer func() { _ = s.conn.CloseNow() }()

	// coder/websocket closes the connection when a Read context expires,
	// which would drop the lifetime error frame. A separate timer sends
	// the frame first and then cancels the read loop.
	timer := time.AfterFunc(cfg.lifetime, func() {
		s.expire(parent)
		cancel()
	})
	defer timer.Stop()

	// A dedicated reader keeps control frames flowing while a turn runs:
	// coder/websocket answers pings and collects pongs only inside Read,
	// and the keepalive below waits for a pong. The channel is unbuffered,
	// so at most one client frame is read ahead of the turn that consumes
	// it and turns stay sequential.
	frames := make(chan frame)
	go func() {
		defer close(frames)
		for {
			typ, data, err := s.conn.Read(ctx)
			if err != nil {
				return
			}
			select {
			case frames <- frame{typ: typ, data: data}:
			case <-ctx.Done():
				return
			}
		}
	}()
	if cfg.keepalive > 0 {
		go s.keepalive(ctx, cancel, cfg.keepalive)
	}

	for f := range frames {
		if f.typ != ws.MessageText {
			if err := s.writeError(ctx, openresponses.InvalidRequest("invalid_message", "expected a text frame", "")); err != nil {
				return
			}
			continue
		}
		if err := s.turn(ctx, f.data); err != nil {
			return
		}
	}
}

// keepalive pings the client every interval and ends the session when
// the pong does not arrive within the next interval. See
// [WithKeepalive].
func (s *session) keepalive(ctx context.Context, cancel context.CancelFunc, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pingCtx, done := context.WithTimeout(ctx, interval)
			err := s.conn.Ping(pingCtx)
			done()
			if err != nil {
				cancel()
				return
			}
		}
	}
}

// expire sends the lifetime error and closes the connection.
func (s *session) expire(parent context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
	defer cancel()
	_ = s.writeError(ctx, &openresponses.Error{
		StatusCode: http.StatusRequestTimeout,
		Type:       openresponses.ErrorTypeInvalidRequest,
		Code:       openresponses.CodeWebSocketConnectionLimitReached,
		Message:    "the WebSocket connection reached its maximum lifetime; reconnect to continue",
	})
	_ = s.conn.Close(ws.StatusNormalClosure, "connection lifetime reached")
}

// turn runs one response.create message. It returns an error only when
// the connection is unusable; protocol errors are reported to the client
// and return nil.
func (s *session) turn(ctx context.Context, data []byte) error {
	var head createFrame
	if err := json.Unmarshal(data, &head); err != nil {
		return s.writeError(ctx, openresponses.InvalidRequest("invalid_json", "message is not valid JSON: "+err.Error(), ""))
	}
	if head.Type != openresponses.EventWebSocketResponseCreate {
		return s.writeError(ctx, openresponses.InvalidRequest(openresponses.CodeInvalidValue, fmt.Sprintf("unknown client event type %q", head.Type), "type"))
	}
	for _, f := range head.forbidden() {
		if len(f.raw) > 0 && string(f.raw) != "null" {
			return s.writeError(ctx, openresponses.InvalidRequest(openresponses.CodeUnsupportedParameter, f.name+" must not be sent over WebSocket", f.name))
		}
	}
	var req openresponses.Request
	if err := json.Unmarshal(data, &req); err != nil {
		return s.writeError(ctx, openresponses.InvalidRequest("invalid_json", err.Error(), ""))
	}
	delete(req.Extra, "type")
	if err := req.Validate(); err != nil {
		return s.writeError(ctx, err)
	}

	cont, err := openresponses.ResolveContinuation(ctx, &req, s.cache, s.store)
	if err != nil {
		if cont.Resolved {
			_ = s.cache.Delete(ctx, cont.ID)
		}
		return s.writeError(ctx, err)
	}
	// A store:false chain lives only in connection memory, and a shared
	// store would have answered for a stored one, so a miss is final.
	if cont.ID != "" && !cont.Resolved && (!req.Stored() || s.store != nil) {
		return s.writeError(ctx, openresponses.PreviousResponseNotFound(cont.ID))
	}

	sink := &sink{ctx: ctx, conn: s.conn, previousID: cont.ID}
	err = s.adapter.CreateStream(ctx, req, sink)
	if sink.failed != nil {
		return sink.failed
	}
	if err != nil && !sink.terminal {
		if cont.Resolved {
			_ = s.cache.Delete(ctx, cont.ID)
		}
		return s.writeError(ctx, err)
	}
	resp := sink.acc.Response()
	if !sink.terminal {
		if resp == nil {
			resp = openresponses.NewResponse(req)
			resp.ID = openresponses.NewID("resp")
		}
		resp.Complete()
		if err := sink.Send(&openresponses.ResponseCompletedEvent{Response: resp}); err != nil {
			return err
		}
	}
	if resp == nil || resp.ID == "" {
		return nil
	}
	if resp.Status == openresponses.ResponseStatusFailed {
		if cont.Resolved {
			_ = s.cache.Delete(ctx, cont.ID)
		}
		return nil
	}
	// The response exists once its terminal event went out; a client
	// that drops the connection right after may still continue from it.
	saveCtx := context.WithoutCancel(ctx)
	_ = s.cache.Save(saveCtx, resp.ID, openresponses.History(req, resp))
	if s.store != nil && req.Stored() {
		if err := s.store.Save(saveCtx, resp.ID, openresponses.History(req, resp)); err != nil {
			return s.writeError(ctx, fmt.Errorf("save response %q: %w", resp.ID, err))
		}
	}
	return nil
}

func (s *session) writeError(ctx context.Context, err error) error {
	e := openresponses.AsError(err)
	frame := errorFrame{Type: openresponses.EventError, Status: e.HTTPStatus(), Error: e.Payload()}
	data, mErr := json.Marshal(frame)
	if mErr != nil {
		return mErr
	}
	return s.conn.Write(ctx, ws.MessageText, data)
}

// sink writes one JSON text frame per event.
type sink struct {
	ctx        context.Context
	conn       *ws.Conn
	previousID string

	mu       sync.Mutex
	seq      int64
	acc      openresponses.Accumulator
	terminal bool
	failed   error
}

// Send assigns the sequence number and writes the frame.
func (s *sink) Send(ev openresponses.StreamEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed != nil {
		return s.failed
	}
	if s.terminal {
		return openresponses.ErrTerminalEventSent
	}
	if setter, ok := ev.(openresponses.SequenceSetter); ok {
		setter.SetSequence(s.seq)
	}
	s.seq++
	openresponses.StampPreviousID(ev, s.previousID)
	data, err := openresponses.EncodeEvent(ev)
	if err != nil {
		return fmt.Errorf("openresponses: encode %s: %w", ev.EventType(), err)
	}
	if err := s.conn.Write(s.ctx, ws.MessageText, data); err != nil {
		s.failed = fmt.Errorf("openresponses: write frame: %w", err)
		return s.failed
	}
	s.acc.Add(ev)
	if _, ok := openresponses.TerminalResponse(ev); ok {
		s.terminal = true
	}
	return nil
}

// Conn is a client-side connection. Turns run one at a time: call Send
// then read events with Next until a terminal event or error, or use
// Turn to do both. The connection is read continuously in the
// background, so server pings are answered between turns and a server
// that closes the connection is noticed at the next Next.
type Conn struct {
	conn   *ws.Conn
	frames chan result
	stop   context.CancelFunc

	mu     sync.Mutex
	closed bool
}

// result is one frame or the error that ended reading.
type result struct {
	data []byte
	err  error
}

// Dial opens a connection to the /responses endpoint under the client's
// base URL, sending the client's headers and using its HTTP client for
// the handshake. A handshake the server refuses with a spec error
// envelope returns that error as an *[openresponses.Error].
func Dial(ctx context.Context, c *openresponses.Client) (*Conn, error) {
	u, err := endpoint(c.BaseURL())
	if err != nil {
		return nil, err
	}
	conn, res, err := ws.Dial(ctx, u, &ws.DialOptions{
		HTTPClient: c.HTTPClient(),
		HTTPHeader: c.RequestHeaders(),
	})
	if err != nil {
		if res != nil {
			defer res.Body.Close()
			return nil, openresponses.ErrorFromResponse(res)
		}
		return nil, fmt.Errorf("openresponses: dial %s: %w", u, err)
	}
	conn.SetReadLimit(c.MaxResponseBytes())
	return newConn(conn), nil
}

// endpoint converts the base URL to the ws(s) scheme and appends
// /responses.
func endpoint(baseURL string) (string, error) {
	u, err := url.Parse(baseURL + "/responses")
	if err != nil {
		return "", fmt.Errorf("openresponses: parse base URL: %w", err)
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("openresponses: unsupported scheme %q for WebSocket", u.Scheme)
	}
	return u.String(), nil
}

// newConn wraps an open connection and starts its reader.
func newConn(conn *ws.Conn) *Conn {
	readCtx, stop := context.WithCancel(context.Background())
	w := &Conn{conn: conn, frames: make(chan result), stop: stop}
	go w.read(readCtx)
	return w
}

// read delivers frames to Next for the life of the connection and ends
// with the error that stopped it.
func (w *Conn) read(ctx context.Context) {
	defer close(w.frames)
	for {
		typ, data, err := w.conn.Read(ctx)
		if err != nil {
			w.deliver(ctx, result{err: fmt.Errorf("openresponses: read frame: %w", err)})
			return
		}
		if typ != ws.MessageText {
			w.deliver(ctx, result{err: fmt.Errorf("openresponses: unexpected %s frame", typ)})
			return
		}
		if !w.deliver(ctx, result{data: data}) {
			return
		}
	}
}

func (w *Conn) deliver(ctx context.Context, r result) bool {
	select {
	case w.frames <- r:
		return true
	case <-ctx.Done():
		return false
	}
}

// Send starts a turn. The stream, stream_options and background fields
// are cleared because the protocol forbids them. req is not modified.
func (w *Conn) Send(ctx context.Context, req openresponses.Request) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return openresponses.ErrStreamClosed
	}
	req.Stream = false
	req.StreamOptions = nil
	req.Background = false
	extra := make(map[string]any, len(req.Extra)+1)
	for k, v := range req.Extra {
		extra[k] = v
	}
	extra["type"] = openresponses.EventWebSocketResponseCreate
	req.Extra = extra
	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("openresponses: encode response.create: %w", err)
	}
	return w.conn.Write(ctx, ws.MessageText, data)
}

// Next reads and decodes the next event. Server error frames decode to
// *[openresponses.ErrorEvent] with Status set. Once reading has failed,
// or after Close, Next returns the failure and then
// [openresponses.ErrStreamClosed].
func (w *Conn) Next(ctx context.Context) (openresponses.StreamEvent, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r, ok := <-w.frames:
		if !ok {
			return nil, openresponses.ErrStreamClosed
		}
		if r.err != nil {
			return nil, r.err
		}
		return openresponses.DecodeEvent(r.data)
	}
}

// Turn sends req and drains events until the turn ends. It returns the
// final response, or an *[openresponses.Error] when the server sent an
// error frame.
func (w *Conn) Turn(ctx context.Context, req openresponses.Request) (*openresponses.Response, error) {
	if err := w.Send(ctx, req); err != nil {
		return nil, err
	}
	var acc openresponses.Accumulator
	for {
		ev, err := w.Next(ctx)
		if err != nil {
			return acc.Response(), err
		}
		acc.Add(ev)
		if e, ok := ev.(*openresponses.ErrorEvent); ok {
			return acc.Response(), e.Err()
		}
		if _, ok := openresponses.TerminalResponse(ev); ok {
			return acc.Response(), nil
		}
	}
}

// Close closes the connection.
func (w *Conn) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	err := w.conn.Close(ws.StatusNormalClosure, "")
	w.stop()
	return err
}
