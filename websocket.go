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
		conn:    conn,
		cache:   newTurnCache(h.ws.cacheSize),
	}
	session.run(r.Context(), h.ws.lifetime)
}

// webSocketSession is one server-side connection.
type webSocketSession struct {
	adapter Adapter
	conn    *websocket.Conn
	cache   *turnCache
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

	previousID := req.PreviousResponseID
	if previousID != "" {
		if prior, ok := s.cache.get(previousID); ok {
			merged := make(Items, 0, len(prior)+len(req.Input))
			merged = append(merged, prior...)
			merged = append(merged, req.Input...)
			if err := checkFunctionCallOutputs(merged); err != nil {
				s.cache.evict(previousID)
				return s.writeError(ctx, err)
			}
			req.Input = merged
			req.PreviousResponseID = ""
		} else if !req.Stored() {
			return s.writeError(ctx, PreviousResponseNotFound(previousID))
		}
	}

	sink := &webSocketSink{ctx: ctx, conn: s.conn, previousID: previousID}
	err := s.adapter.CreateStream(ctx, req, sink)
	if sink.failed != nil {
		return sink.failed
	}
	if err != nil && !sink.terminal {
		if previousID != "" {
			s.cache.evict(previousID)
		}
		return s.writeError(ctx, err)
	}
	resp := sink.acc.Response()
	if !sink.terminal {
		if resp == nil {
			resp = NewResponse(req)
			resp.ID = NewID("resp")
		}
		resp.Status = ResponseStatusCompleted
		now := time.Now().Unix()
		resp.CompletedAt = &now
		if err := sink.Send(&ResponseCompletedEvent{Response: resp}); err != nil {
			return err
		}
	}
	if resp != nil && resp.ID != "" {
		if resp.Status == ResponseStatusFailed {
			if previousID != "" {
				s.cache.evict(previousID)
			}
		} else {
			history := make(Items, 0, len(req.Input)+len(resp.Output))
			history = append(history, req.Input...)
			history = append(history, resp.Output...)
			s.cache.put(resp.ID, history)
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
	s.stampPreviousID(ev)
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

// stampPreviousID restores previous_response_id on response snapshots
// when the connection resolved the continuation itself.
func (s *webSocketSink) stampPreviousID(ev StreamEvent) {
	if s.previousID == "" {
		return
	}
	var resp *Response
	switch e := ev.(type) {
	case *ResponseCreatedEvent:
		resp = e.Response
	case *ResponseQueuedEvent:
		resp = e.Response
	case *ResponseInProgressEvent:
		resp = e.Response
	case *ResponseCompletedEvent:
		resp = e.Response
	case *ResponseFailedEvent:
		resp = e.Response
	case *ResponseIncompleteEvent:
		resp = e.Response
	}
	if resp != nil && resp.PreviousResponseID == nil {
		id := s.previousID
		resp.PreviousResponseID = &id
	}
}

// checkFunctionCallOutputs verifies that every function_call_output
// refers to a function_call earlier in the conversation.
func checkFunctionCallOutputs(items []Item) error {
	calls := map[string]bool{}
	for i, item := range items {
		switch v := item.(type) {
		case *FunctionCall:
			calls[v.CallID] = true
		case *FunctionCallOutput:
			if !calls[v.CallID] {
				return InvalidRequest(CodeInvalidValue,
					fmt.Sprintf("no function_call with call_id %q precedes this function_call_output", v.CallID),
					fmt.Sprintf("input[%d].call_id", i))
			}
		}
	}
	return nil
}

// turnCache remembers the conversation context of recent responses on a
// connection, keyed by response ID, so previous_response_id works with
// store:false. It keeps the newest size entries.
type turnCache struct {
	mu    sync.Mutex
	size  int
	order []string
	items map[string]Items
}

func newTurnCache(size int) *turnCache {
	if size < 1 {
		size = 1
	}
	return &turnCache{size: size, items: make(map[string]Items, size)}
}

func (c *turnCache) get(id string) (Items, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	items, ok := c.items[id]
	return items, ok
}

func (c *turnCache) put(id string, items Items) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.items[id]; !exists {
		c.order = append(c.order, id)
	}
	c.items[id] = items
	for len(c.order) > c.size {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.items, oldest)
	}
}

func (c *turnCache) evict(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, id)
	for i, v := range c.order {
		if v == id {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
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
