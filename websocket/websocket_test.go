package websocket

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	ws "github.com/coder/websocket"

	"github.com/ChristopherDavenport/openresponses"
	"github.com/ChristopherDavenport/openresponses/echo"
)

func server(t *testing.T, opts ...Option) (*httptest.Server, *openresponses.Client) {
	t.Helper()
	return serverWith(t, nil, opts...)
}

func serverWith(t *testing.T, store openresponses.ResponseStore, opts ...Option) (*httptest.Server, *openresponses.Client) {
	t.Helper()
	var hopts []openresponses.HandlerOption
	if store != nil {
		hopts = append(hopts, openresponses.WithResponseStore(store))
	}
	srv := httptest.NewServer(Handler(openresponses.NewHandler(&echo.Adapter{}, hopts...), opts...))
	t.Cleanup(srv.Close)
	return srv, openresponses.NewClient(srv.URL+"/v1", openresponses.WithAPIKey("k"))
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func user(text string) openresponses.Items { return openresponses.Items{openresponses.UserText(text)} }

func TestTurns(t *testing.T) {
	_, c := server(t)
	ctx := testContext(t)
	conn, err := Dial(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	store := false
	first, err := conn.Turn(ctx, openresponses.Request{Model: "m", Store: &store, Input: user("first")})
	if err != nil {
		t.Fatal(err)
	}
	if first.OutputText() != "first" || first.ID == "" {
		t.Errorf("first = %+v", first)
	}
	second, err := conn.Turn(ctx, openresponses.Request{Model: "m", Store: &store, Input: user("second")})
	if err != nil {
		t.Fatal(err)
	}
	if second.OutputText() != "second" {
		t.Errorf("second = %+v", second)
	}

	// Continuation with store:false resolves through the connection cache
	// and reports previous_response_id.
	cont, err := conn.Turn(ctx, openresponses.Request{Model: "m", Store: &store, PreviousResponseID: first.ID, Input: openresponses.Items{
		&openresponses.FunctionCall{CallID: "c1", Name: "f", Arguments: "{}"},
		openresponses.NewFunctionCallOutput("c1", "ok"),
		openresponses.UserText("continued"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if cont.OutputText() != "continued" || cont.PreviousResponseID == nil || *cont.PreviousResponseID != first.ID {
		t.Errorf("continuation = %+v", cont)
	}
}

func TestPreviousResponseNotFound(t *testing.T) {
	_, c := server(t)
	ctx := testContext(t)
	conn, err := Dial(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	store := false
	_, err = conn.Turn(ctx, openresponses.Request{Model: "m", Store: &store, PreviousResponseID: "resp_missing", Input: user("x")})
	var e *openresponses.Error
	if !errors.As(err, &e) || e.Code != openresponses.CodePreviousResponseNotFound || e.StatusCode != 404 {
		t.Fatalf("err = %v", err)
	}
	// The connection is still usable afterwards.
	if _, err := conn.Turn(ctx, openresponses.Request{Model: "m", Store: &store, Input: user("recover")}); err != nil {
		t.Fatalf("recovery turn: %v", err)
	}
}

func TestReconnectLosesCache(t *testing.T) {
	_, c := server(t)
	ctx := testContext(t)
	store := false
	conn, err := Dial(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	first, err := conn.Turn(ctx, openresponses.Request{Model: "m", Store: &store, Input: user("x")})
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()

	conn2, err := Dial(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	defer conn2.Close()
	_, err = conn2.Turn(ctx, openresponses.Request{Model: "m", Store: &store, PreviousResponseID: first.ID, Input: user("y")})
	if !errors.Is(err, &openresponses.Error{Code: openresponses.CodePreviousResponseNotFound}) {
		t.Fatalf("err = %v", err)
	}
}

func TestSharedStoreAcrossConnections(t *testing.T) {
	store := openresponses.NewMemoryStore(8)
	_, c := serverWith(t, store)
	ctx := testContext(t)

	conn, err := Dial(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	first, err := conn.Turn(ctx, openresponses.Request{Model: "m", Input: user("x")})
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()

	// A stored response is reachable from a new connection through the
	// shared store; a store:false one is not.
	conn2, err := Dial(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	defer conn2.Close()
	second, err := conn2.Turn(ctx, openresponses.Request{Model: "m", PreviousResponseID: first.ID, Input: user("y")})
	if err != nil || second.OutputText() != "y" {
		t.Fatalf("continuation via store: %v %+v", err, second)
	}
	off := false
	local, err := conn2.Turn(ctx, openresponses.Request{Model: "m", Store: &off, Input: user("z")})
	if err != nil {
		t.Fatal(err)
	}
	_ = conn2.Close()
	conn3, err := Dial(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	defer conn3.Close()
	_, err = conn3.Turn(ctx, openresponses.Request{Model: "m", PreviousResponseID: local.ID, Input: user("w")})
	if !errors.Is(err, &openresponses.Error{Code: openresponses.CodePreviousResponseNotFound}) {
		t.Fatalf("store:false response leaked into the shared store: %v", err)
	}

	// The stored response is also reachable over HTTP.
	if _, err := c.Create(ctx, openresponses.Request{Model: "m", PreviousResponseID: first.ID, Input: user("http")}); err != nil {
		t.Fatalf("continuation over HTTP: %v", err)
	}
}

func TestFailedContinuationEvicts(t *testing.T) {
	_, c := server(t)
	ctx := testContext(t)
	conn, err := Dial(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	store := false
	first, err := conn.Turn(ctx, openresponses.Request{Model: "m", Store: &store, Input: user("x")})
	if err != nil {
		t.Fatal(err)
	}
	_, err = conn.Turn(ctx, openresponses.Request{Model: "m", Store: &store, PreviousResponseID: first.ID, Input: openresponses.Items{openresponses.NewFunctionCallOutput("call_missing", "no such call")}})
	if !openresponses.IsInvalidRequest(err) {
		t.Fatalf("expected invalid_request, got %v", err)
	}
	_, err = conn.Turn(ctx, openresponses.Request{Model: "m", Store: &store, PreviousResponseID: first.ID, Input: user("stale")})
	if !errors.Is(err, &openresponses.Error{Code: openresponses.CodePreviousResponseNotFound}) {
		t.Fatalf("err = %v", err)
	}
}

func TestRejectsForbiddenFields(t *testing.T) {
	srv, _ := server(t)
	ctx := testContext(t)
	raw, _, err := ws.Dial(ctx, "ws"+srv.URL[len("http"):]+"/v1/responses", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.CloseNow()
	conn := newConn(raw)
	for _, body := range []string{
		`{"type":"response.create","model":"m","input":"x","stream":true}`,
		`{"type":"response.create","model":"m","input":"x","background":false}`,
		`{"type":"response.create","model":"m","input":"x","stream_options":{}}`,
		`{"type":"other","model":"m","input":"x"}`,
		`{"model":"m","input":"x"}`,
		`not json`,
	} {
		if err := raw.Write(ctx, ws.MessageText, []byte(body)); err != nil {
			t.Fatal(err)
		}
		ev, err := conn.Next(ctx)
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		e, ok := ev.(*openresponses.ErrorEvent)
		if !ok || e.Status != 400 || e.Error.Type != openresponses.ErrorTypeInvalidRequest {
			t.Errorf("%s: got %#v", body, ev)
		}
	}
}

func TestLifetime(t *testing.T) {
	_, c := server(t, WithLifetime(150*time.Millisecond))
	ctx := testContext(t)
	conn, err := Dial(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ev, err := conn.Next(ctx)
	if err != nil {
		t.Fatal(err)
	}
	e, ok := ev.(*openresponses.ErrorEvent)
	if !ok || e.Error.Code != openresponses.CodeWebSocketConnectionLimitReached {
		t.Fatalf("got %#v", ev)
	}
	if _, err := conn.Next(ctx); err == nil {
		t.Error("expected the connection to be closed")
	}
}

func TestOriginCheck(t *testing.T) {
	srv, _ := server(t)
	ctx := testContext(t)
	_, res, err := ws.Dial(ctx, "ws"+srv.URL[len("http"):]+"/v1/responses", &ws.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{"https://evil.example"}},
	})
	if err == nil {
		t.Fatal("expected cross-origin dial to fail")
	}
	if res == nil || res.StatusCode != http.StatusForbidden {
		t.Errorf("response = %+v", res)
	}
	srv2, _ := server(t, WithOrigins("*"))
	raw, _, err := ws.Dial(ctx, "ws"+srv2.URL[len("http"):]+"/responses", &ws.DialOptions{
		HTTPHeader: http.Header{"Origin": []string{"https://evil.example"}},
	})
	if err != nil {
		t.Fatalf("wildcard origin refused: %v", err)
	}
	_ = raw.CloseNow()
}

func TestDialError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error","code":"unauthorized","message":"no"}}`))
	}))
	defer srv.Close()
	_, err := Dial(testContext(t), openresponses.NewClient(srv.URL))
	var e *openresponses.Error
	if !errors.As(err, &e) || e.StatusCode != 401 || e.Code != "unauthorized" {
		t.Fatalf("err = %v", err)
	}
}

func TestDialSendsClientHeaders(t *testing.T) {
	var got http.Header
	inner := openresponses.NewHandler(&echo.Adapter{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		Handler(inner).ServeHTTP(w, r)
	}))
	defer srv.Close()
	c := openresponses.NewClient(srv.URL, openresponses.WithAPIKey("secret"), openresponses.WithHeader("X-Tenant", "t1"))
	conn, err := Dial(testContext(t), c)
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if got.Get("Authorization") != "Bearer secret" || got.Get("X-Tenant") != "t1" || !strings.HasPrefix(got.Get("User-Agent"), "openresponses-go/") {
		t.Errorf("handshake headers = %v", got)
	}
}

func TestHandlerPassesHTTPThrough(t *testing.T) {
	srv, c := server(t)
	ctx := testContext(t)
	resp, err := c.Create(ctx, openresponses.Request{Model: "m", Input: user("over http")})
	if err != nil || resp.OutputText() != "over http" {
		t.Fatalf("Create through the wrapper: %v %+v", err, resp)
	}
	// A GET upgrade on another path is not intercepted.
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/v1/other", nil)
	req.Header.Set("Upgrade", "websocket")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404 from the wrapped handler", res.StatusCode)
	}
}

func TestUnwrappedHandlerRefusesUpgrade(t *testing.T) {
	srv := httptest.NewServer(openresponses.NewHandler(&echo.Adapter{}))
	defer srv.Close()
	_, err := Dial(testContext(t), openresponses.NewClient(srv.URL))
	var e *openresponses.Error
	if !errors.As(err, &e) || e.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("err = %v, want method_not_allowed from the bare handler", err)
	}
}

func TestSendDoesNotMutateRequest(t *testing.T) {
	_, c := server(t)
	ctx := testContext(t)
	conn, err := Dial(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	req := openresponses.Request{Model: "m", Input: user("hi"), Extra: map[string]any{"x": 1}}
	if _, err := conn.Turn(ctx, req); err != nil {
		t.Fatal(err)
	}
	if _, ok := req.Extra["type"]; ok || len(req.Extra) != 1 {
		t.Errorf("caller's Extra was modified: %v", req.Extra)
	}
}

func TestKeepaliveDropsSilentPeer(t *testing.T) {
	srv, _ := server(t, WithKeepalive(20*time.Millisecond))
	ctx := testContext(t)
	raw, _, err := ws.Dial(ctx, "ws"+srv.URL[len("http"):]+"/v1/responses", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer raw.CloseNow()
	// A peer that never reads never answers pings.
	time.Sleep(200 * time.Millisecond)
	readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, _, err = raw.Read(readCtx)
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected the server to have closed the connection, got %v", err)
	}
}

func TestKeepaliveSparesIdleClient(t *testing.T) {
	_, c := server(t, WithKeepalive(20*time.Millisecond))
	ctx := testContext(t)
	conn, err := Dial(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// Idle for many keepalive intervals; the background reader answers.
	time.Sleep(200 * time.Millisecond)
	resp, err := conn.Turn(ctx, openresponses.Request{Model: "m", Input: user("still here")})
	if err != nil {
		t.Fatal(err)
	}
	if resp.OutputText() != "still here" {
		t.Errorf("got %q", resp.OutputText())
	}
}

func TestProxyChain(t *testing.T) {
	// WebSocket downstream over an HTTP upstream through ClientAdapter.
	origin := httptest.NewServer(openresponses.NewHandler(&echo.Adapter{}))
	defer origin.Close()
	proxy := httptest.NewServer(Handler(openresponses.NewHandler(openresponses.NewClient(origin.URL).AsAdapter())))
	defer proxy.Close()
	ctx := testContext(t)
	conn, err := Dial(ctx, openresponses.NewClient(proxy.URL))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	turn, err := conn.Turn(ctx, openresponses.Request{Model: "m", Input: user("ws")})
	if err != nil || turn.OutputText() != "ws" {
		t.Fatalf("Turn: %v %+v", err, turn)
	}
}
