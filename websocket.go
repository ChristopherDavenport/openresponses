package openresponses

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// webSocketConfig holds the server-side WebSocket settings.
type webSocketConfig struct {
	lifetime       time.Duration
	originPatterns []string
	cacheSize      int
}

// webSocketError is the wire form of a WebSocket error frame.
type webSocketError struct {
	Type   string       `json:"type"`
	Status int          `json:"status"`
	Error  ErrorPayload `json:"error"`
}

// webSocketCreate is the client frame that starts a turn. The forbidden
// HTTP-only fields are captured raw so their presence can be rejected.
type webSocketCreate struct {
	Type          string          `json:"type"`
	Stream        json.RawMessage `json:"stream"`
	StreamOptions json.RawMessage `json:"stream_options"`
	Background    json.RawMessage `json:"background"`
}

// serveWebSocket upgrades the connection and runs turns until the client
// disconnects or the lifetime expires.
func (h *Handler) serveWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: h.ws.originPatterns,
	})
	if err != nil {
		// Accept has already written an HTTP error response.
		return
	}
	conn.SetReadLimit(h.maxBodyBytes)
	session := &webSocketSession{
		adapter: h.adapter,
		store:   h.store,
		conn:    conn,
		cache:   NewMemoryStore(h.ws.cacheSize),
	}
	session.run(r.Context(), h.ws.lifetime)
}

// webSocketSession is one server-side connection. cache is the
// connection-local memory the spec asks for; store is the handler's
// shared store, consulted second.
type webSocketSession struct {
	adapter Adapter
	store   ResponseStore
	conn    *websocket.Conn
	cache   *MemoryStore
}

func (s *webSocketSession) run(parent context.Context, lifetime time.Duration) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	defer func() { _ = s.conn.CloseNow() }()

	// coder/websocket closes the connection when a Read context expires,
	// which would drop the lifetime error frame. A separate timer sends
	// the frame first and then cancels the read loop.
	timer := time.AfterFunc(lifetime, func() {
		s.expire(parent)
		cancel()
	})
	defer timer.Stop()

	for {
		typ, data, err := s.conn.Read(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			if err := s.writeError(ctx, InvalidRequest("invalid_message", "expected a text frame", "")); err != nil {
				return
			}
			continue
		}
		if err := s.turn(ctx, data); err != nil {
			return
		}
	}
}

// expire sends the lifetime error and closes the connection.
func (s *webSocketSession) expire(parent context.Context) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
	defer cancel()
	_ = s.writeError(ctx, &Error{
		StatusCode: http.StatusRequestTimeout,
		Type:       ErrorTypeInvalidRequest,
		Code:       CodeWebSocketConnectionLimitReached,
		Message:    "the WebSocket connection reached its maximum lifetime; reconnect to continue",
	})
	_ = s.conn.Close(websocket.StatusNormalClosure, "connection lifetime reached")
}

// turn runs one response.create message. It returns an error only when
// the connection is unusable; protocol errors are reported to the client
// and return nil.
func (s *webSocketSession) turn(ctx context.Context, data []byte) error {
	var head webSocketCreate
	if err := json.Unmarshal(data, &head); err != nil {
		return s.writeError(ctx, InvalidRequest("invalid_json", "message is not valid JSON: "+err.Error(), ""))
	}
	if head.Type != EventWebSocketResponseCreate {
		return s.writeError(ctx, InvalidRequest(CodeInvalidValue, fmt.Sprintf("unknown client event type %q", head.Type), "type"))
	}
	for name, raw := range map[string]json.RawMessage{"stream": head.Stream, "stream_options": head.StreamOptions, "background": head.Background} {
		if len(raw) > 0 && string(raw) != "null" {
			return s.writeError(ctx, InvalidRequest(CodeUnsupportedParameter, name+" must not be sent over WebSocket", name))
		}
	}
	var req Request
	if err := json.Unmarshal(data, &req); err != nil {
		return s.writeError(ctx, InvalidRequest("invalid_json", err.Error(), ""))
	}
	delete(req.Extra, "type")
	if err := req.Validate(); err != nil {
		return s.writeError(ctx, err)
	}

	cont, err := resolveContinuation(ctx, &req, s.cache, s.store)
	if err != nil {
		if cont.resolved {
			_ = s.cache.Delete(ctx, cont.id)
		}
		return s.writeError(ctx, err)
	}
	// A store:false chain lives only in connection memory, and a shared
	// store would have answered for a stored one, so a miss is final.
	if cont.id != "" && !cont.resolved && (!req.Stored() || s.store != nil) {
		return s.writeError(ctx, PreviousResponseNotFound(cont.id))
	}

	sink := &webSocketSink{ctx: ctx, conn: s.conn, previousID: cont.id}
	err = s.adapter.CreateStream(ctx, req, sink)
	if sink.failed != nil {
		return sink.failed
	}
	if err != nil && !sink.terminal {
		if cont.resolved {
			_ = s.cache.Delete(ctx, cont.id)
		}
		return s.writeError(ctx, err)
	}
	resp := sink.acc.Response()
	if !sink.terminal {
		if resp == nil {
			resp = NewResponse(req)
			resp.ID = NewID("resp")
		}
		resp.Complete()
		if err := sink.Send(&ResponseCompletedEvent{Response: resp}); err != nil {
			return err
		}
	}
	if resp == nil || resp.ID == "" {
		return nil
	}
	if resp.Status == ResponseStatusFailed {
		if cont.resolved {
			_ = s.cache.Delete(ctx, cont.id)
		}
		return nil
	}
	_ = s.cache.Save(ctx, resp.ID, history(req, resp))
	if s.store != nil && req.Stored() {
		if err := s.store.Save(ctx, resp.ID, history(req, resp)); err != nil {
			return s.writeError(ctx, fmt.Errorf("save response %q: %w", resp.ID, err))
		}
	}
	return nil
}

func (s *webSocketSession) writeError(ctx context.Context, err error) error {
	e := AsError(err)
	frame := webSocketError{Type: EventError, Status: e.HTTPStatus(), Error: e.Payload()}
	data, mErr := json.Marshal(frame)
	if mErr != nil {
		return mErr
	}
	return s.conn.Write(ctx, websocket.MessageText, data)
}

// webSocketSink writes one JSON text frame per event.
type webSocketSink struct {
	ctx        context.Context
	conn       *websocket.Conn
	previousID string

	mu       sync.Mutex
	seq      int64
	acc      Accumulator
	terminal bool
	failed   error
}

// Send assigns the sequence number and writes the frame.
func (s *webSocketSink) Send(ev StreamEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed != nil {
		return s.failed
	}
	if s.terminal {
		return ErrTerminalEventSent
	}
	if setter, ok := ev.(sequenceSetter); ok {
		setter.SetSequence(s.seq)
	}
	s.seq++
	stampPreviousID(ev, s.previousID)
	data, err := EncodeEvent(ev)
	if err != nil {
		return fmt.Errorf("openresponses: encode %s: %w", ev.EventType(), err)
	}
	if err := s.conn.Write(s.ctx, websocket.MessageText, data); err != nil {
		s.failed = fmt.Errorf("openresponses: write frame: %w", err)
		return s.failed
	}
	s.acc.Add(ev)
	if _, ok := TerminalResponse(ev); ok {
		s.terminal = true
	}
	return nil
}

// WebSocketConn is a client-side WebSocket connection. Turns run one at
// a time: call Send then read events with Next until a terminal event or
// error, or use Turn to do both.
type WebSocketConn struct {
	conn *websocket.Conn

	mu     sync.Mutex
	closed bool
}

// Dial opens a WebSocket connection to the server's /responses endpoint.
func (c *Client) Dial(ctx context.Context) (*WebSocketConn, error) {
	u, err := c.webSocketURL()
	if err != nil {
		return nil, err
	}
	headers := http.Header{}
	c.applyHeaders(headers)
	conn, res, err := websocket.Dial(ctx, u, &websocket.DialOptions{
		HTTPClient: c.http,
		HTTPHeader: headers,
	})
	if err != nil {
		if res != nil {
			defer drainAndClose(res.Body)
			return nil, errorFromHTTP(res)
		}
		return nil, fmt.Errorf("openresponses: dial %s: %w", u, err)
	}
	conn.SetReadLimit(64 << 20)
	return &WebSocketConn{conn: conn}, nil
}

// Send starts a turn. The stream, stream_options and background fields
// are cleared because the protocol forbids them.
func (w *WebSocketConn) Send(ctx context.Context, req Request) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return ErrStreamClosed
	}
	req.Stream = false
	req.StreamOptions = nil
	req.Background = false
	if req.Extra == nil {
		req.Extra = map[string]any{}
	}
	req.Extra["type"] = EventWebSocketResponseCreate
	data, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("openresponses: encode response.create: %w", err)
	}
	return w.conn.Write(ctx, websocket.MessageText, data)
}

// Next reads and decodes the next event. Server error frames decode to
// *ErrorEvent with Status set.
func (w *WebSocketConn) Next(ctx context.Context) (StreamEvent, error) {
	typ, data, err := w.conn.Read(ctx)
	if err != nil {
		return nil, fmt.Errorf("openresponses: read frame: %w", err)
	}
	if typ != websocket.MessageText {
		return nil, fmt.Errorf("openresponses: unexpected %s frame", typ)
	}
	return DecodeEvent(data)
}

// Turn sends req and drains events until the turn ends. It returns the
// final response, or an *Error when the server sent an error frame.
func (w *WebSocketConn) Turn(ctx context.Context, req Request) (*Response, error) {
	if err := w.Send(ctx, req); err != nil {
		return nil, err
	}
	var acc Accumulator
	for {
		ev, err := w.Next(ctx)
		if err != nil {
			return acc.Response(), err
		}
		acc.Add(ev)
		if e, ok := ev.(*ErrorEvent); ok {
			return acc.Response(), e.Err()
		}
		if _, ok := TerminalResponse(ev); ok {
			return acc.Response(), nil
		}
	}
}

// Close closes the connection.
func (w *WebSocketConn) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	return w.conn.Close(websocket.StatusNormalClosure, "")
}
