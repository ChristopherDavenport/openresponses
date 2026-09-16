package openresponses

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// echoAdapter is a small in-package adapter that echoes the last user
// message and honours function_call_output items, enough to exercise
// continuation.
type echoAdapter struct{ UnsupportedCompaction }

func (echoAdapter) Create(ctx context.Context, req Request) (*Response, error) {
	return CollectStream(ctx, echoAdapter{}, req)
}

func (echoAdapter) CreateStream(_ context.Context, req Request, sink EventSink) error {
	resp := NewResponse(req)
	resp.ID = NewID("resp")
	if err := sink.Send(&ResponseCreatedEvent{Response: resp}); err != nil {
		return err
	}
	text := "Hello!"
	for i := len(req.Input) - 1; i >= 0; i-- {
		if m, ok := req.Input[i].(*Message); ok && m.Role == RoleUser {
			text = m.Text()
			break
		}
	}
	msg := &Message{ID: NewID("msg"), Status: StatusCompleted, Role: RoleAssistant, Content: Contents{&OutputText{Text: text}}}
	if err := sink.Send(&OutputItemAddedEvent{Item: msg}); err != nil {
		return err
	}
	if err := sink.Send(&OutputItemDoneEvent{Item: msg}); err != nil {
		return err
	}
	resp.Output = Items{msg}
	resp.Status = ResponseStatusCompleted
	return sink.Send(&ResponseCompletedEvent{Response: resp})
}

func wsServer(t *testing.T, opts ...HandlerOption) (*httptest.Server, *Client) {
	t.Helper()
	srv := httptest.NewServer(NewHandler(echoAdapter{}, opts...))
	t.Cleanup(srv.Close)
	return srv, NewClient(srv.URL+"/v1", WithAPIKey("k"))
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestWebSocketTurns(t *testing.T) {
	_, c := wsServer(t)
	ctx := testContext(t)
	conn, err := c.Dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	store := false
	first, err := conn.Turn(ctx, Request{Model: "m", Store: &store, Input: Items{UserText("first")}})
	if err != nil {
		t.Fatal(err)
	}
	if first.OutputText() != "first" || first.ID == "" {
		t.Errorf("first = %+v", first)
	}
	second, err := conn.Turn(ctx, Request{Model: "m", Store: &store, Input: Items{UserText("second")}})
	if err != nil {
		t.Fatal(err)
	}
	if second.OutputText() != "second" {
		t.Errorf("second = %+v", second)
	}

	// Continuation with store:false resolves through the connection cache
	// and reports previous_response_id.
	cont, err := conn.Turn(ctx, Request{Model: "m", Store: &store, PreviousResponseID: first.ID, Input: Items{&FunctionCall{CallID: "c1", Name: "f", Arguments: "{}"}, NewFunctionCallOutput("c1", "ok"), UserText("continued")}})
	if err != nil {
		t.Fatal(err)
	}
	if cont.OutputText() != "continued" || cont.PreviousResponseID == nil || *cont.PreviousResponseID != first.ID {
		t.Errorf("continuation = %+v", cont)
	}
}

func TestWebSocketPreviousResponseNotFound(t *testing.T) {
	_, c := wsServer(t)
	ctx := testContext(t)
	conn, err := c.Dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	store := false
	_, err = conn.Turn(ctx, Request{Model: "m", Store: &store, PreviousResponseID: "resp_missing", Input: Items{UserText("x")}})
	var e *Error
	if !errors.As(err, &e) || e.Code != CodePreviousResponseNotFound || e.StatusCode != 404 {
		t.Fatalf("err = %v", err)
	}
	// The connection is still usable afterwards.
	if _, err := conn.Turn(ctx, Request{Model: "m", Store: &store, Input: Items{UserText("recover")}}); err != nil {
		t.Fatalf("recovery turn: %v", err)
	}
}

func TestWebSocketReconnectLosesCache(t *testing.T) {
	_, c := wsServer(t)
	ctx := testContext(t)
	store := false
	conn, err := c.Dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	first, err := conn.Turn(ctx, Request{Model: "m", Store: &store, Input: Items{UserText("x")}})
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()

	conn2, err := c.Dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn2.Close()
	_, err = conn2.Turn(ctx, Request{Model: "m", Store: &store, PreviousResponseID: first.ID, Input: Items{UserText("y")}})
	if !errors.Is(err, &Error{Code: CodePreviousResponseNotFound}) {
		t.Fatalf("err = %v", err)
	}
}

func TestWebSocketFailedContinuationEvicts(t *testing.T) {
	_, c := wsServer(t)
	ctx := testContext(t)
	conn, err := c.Dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	store := false
	first, err := conn.Turn(ctx, Request{Model: "m", Store: &store, Input: Items{UserText("x")}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.Turn(ctx, Request{Model: "m", Store: &store, PreviousResponseID: first.ID, Input: Items{NewFunctionCallOutput("call_missing", "no such call")}})
	if !IsInvalidRequest(err) {
		t.Fatalf("expected invalid_request, got %v", err)
	}
	_, err = conn.Turn(ctx, Request{Model: "m", Store: &store, PreviousResponseID: first.ID, Input: Items{UserText("stale")}})
	if !errors.Is(err, &Error{Code: CodePreviousResponseNotFound}) {
		t.Fatalf("err = %v", err)
	}
}

func TestWebSocketRejectsForbiddenFields(t *testing.T) {
	srv, _ := wsServer(t)
	ctx := testContext(t)
	raw, _, err := websocket.Dial(ctx, "ws"+srv.URL[len("http"):]+"/v1/responses", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.CloseNow()
	conn := &WebSocketConn{conn: raw}
	for _, body := range []string{
		`{"type":"response.create","model":"m","input":"x","stream":true}`,
		`{"type":"response.create","model":"m","input":"x","background":false}`,
		`{"type":"response.create","model":"m","input":"x","stream_options":{}}`,
		`{"type":"other","model":"m","input":"x"}`,
		`{"model":"m","input":"x"}`,
		`not json`,
	} {
		if err := raw.Write(ctx, websocket.MessageText, []byte(body)); err != nil {
			t.Fatal(err)
		}
		ev, err := conn.Next(ctx)
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		e, ok := ev.(*ErrorEvent)
		if !ok || e.Status != 400 || e.Error.Type != ErrorTypeInvalidRequest {
			t.Errorf("%s: got %#v", body, ev)
		}
	}
}

func TestWebSocketLifetime(t *testing.T) {
	_, c := wsServer(t, WithWebSocketLifetime(150*time.Millisecond))
	ctx := testContext(t)
	conn, err := c.Dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ev, err := conn.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := ev.(*ErrorEvent)
	if !ok || e.Error.Code != CodeWebSocketConnectionLimitReached {
		t.Fatalf("got %#v", ev)
	}
	if _, err := conn.Next(ctx); err == nil {
		t.Error("expected the connection to be closed")
	}
}

func TestWebSocketOriginCheck(t *testing.T) {
	srv, _ := wsServer(t)
	ctx := testContext(t)
	_, res, err := websocket.Dial(ctx, "ws"+srv.URL[len("http"):]+"/v1/responses", &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{"https://evil.example"}},
	})
	if err == nil {
		t.Fatal("expected cross-origin dial to fail")
	}
	if res == nil || res.StatusCode != http.StatusForbidden {
		t.Errorf("response = %+v", res)
	}
	srv2 := httptest.NewServer(NewHandler(echoAdapter{}, WithWebSocketOrigins("*")))
	defer srv2.Close()
	raw, _, err := websocket.Dial(ctx, "ws"+srv2.URL[len("http"):]+"/responses", &websocket.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{"https://evil.example"}},
	})
	if err != nil {
		t.Fatalf("wildcard origin refused: %v", err)
	}
	_ = raw.CloseNow()
}

func TestWebSocketDialError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, &Error{StatusCode: 401, Type: ErrorTypeInvalidRequest, Code: "unauthorized", Message: "no"})
	}))
	defer srv.Close()
	_, err := NewClient(srv.URL).Dial(testContext(t))
	var e *Error
	if !errors.As(err, &e) || e.StatusCode != 401 || e.Code != "unauthorized" {
		t.Fatalf("err = %v", err)
	}
}

func TestTurnCache(t *testing.T) {
	c := newTurnCache(2)
	c.put("a", Items{UserText("a")})
	c.put("b", Items{UserText("b")})
	c.put("c", Items{UserText("c")})
	if _, ok := c.get("a"); ok {
		t.Error("a should have been evicted")
	}
	if _, ok := c.get("c"); !ok {
		t.Error("c missing")
	}
	c.evict("b")
	if _, ok := c.get("b"); ok {
		t.Error("b should be gone")
	}
	c.put("d", nil)
	c.put("e", nil)
	if len(c.order) != 2 || len(c.items) != 2 {
		t.Errorf("order=%v items=%d", c.order, len(c.items))
	}
}
