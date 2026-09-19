package openresponses

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
)

func TestMemoryStore(t *testing.T) {
	ctx := context.Background()
	m := NewMemoryStore(2)
	for _, id := range []string{"a", "b", "c"} {
		if err := m.Save(ctx, id, Items{UserText(id)}); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok, _ := m.Load(ctx, "a"); ok {
		t.Error("a should have been evicted")
	}
	if items, ok, _ := m.Load(ctx, "c"); !ok || items[0].(*Message).Text() != "c" {
		t.Error("c missing")
	}
	if err := m.Delete(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := m.Load(ctx, "b"); ok {
		t.Error("b should be gone")
	}
	_ = m.Save(ctx, "d", nil)
	_ = m.Save(ctx, "e", nil)
	if m.Len() != 2 {
		t.Errorf("len = %d", m.Len())
	}
	if err := m.Delete(ctx, "missing"); err != nil {
		t.Errorf("delete unknown: %v", err)
	}
}

// storeServer serves the echo adapter with a shared store.
func storeServer(t *testing.T, store ResponseStore) (*httptest.Server, *Client) {
	t.Helper()
	srv := httptest.NewServer(NewHandler(echoAdapter{}, WithResponseStore(store)))
	t.Cleanup(srv.Close)
	return srv, NewClient(srv.URL)
}

func TestHandlerStoreContinuationHTTP(t *testing.T) {
	store := NewMemoryStore(8)
	_, c := storeServer(t, store)
	ctx := testContext(t)

	first, err := c.Create(ctx, Request{Model: "m", Input: Items{UserText("first")}})
	if err != nil {
		t.Fatal(err)
	}
	if store.Len() != 1 {
		t.Fatalf("store has %d entries after a stored response", store.Len())
	}
	second, err := c.Create(ctx, Request{Model: "m", PreviousResponseID: first.ID, Input: Items{
		&FunctionCall{CallID: "c1", Name: "f", Arguments: "{}"},
		NewFunctionCallOutput("c1", "ok"),
		UserText("second"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if second.OutputText() != "second" || second.PreviousResponseID == nil || *second.PreviousResponseID != first.ID {
		t.Errorf("second = %+v", second)
	}
	// The saved history for the second response is the whole chain.
	hist, ok, _ := store.Load(ctx, second.ID)
	if !ok || len(hist) != 6 {
		t.Errorf("history = %d items, ok=%v", len(hist), ok)
	}
	// Streaming continuation resolves and stamps too.
	stream, err := c.CreateStream(ctx, Request{Model: "m", PreviousResponseID: second.ID, Input: Items{UserText("third")}})
	if err != nil {
		t.Fatal(err)
	}
	third, err := stream.Wait()
	_ = stream.Close()
	if err != nil {
		t.Fatal(err)
	}
	if third.OutputText() != "third" || third.PreviousResponseID == nil || *third.PreviousResponseID != second.ID {
		t.Errorf("third = %+v", third)
	}
	if store.Len() != 3 {
		t.Errorf("store has %d entries", store.Len())
	}
}

func TestHandlerStoreMisses(t *testing.T) {
	store := NewMemoryStore(8)
	_, c := storeServer(t, store)
	ctx := testContext(t)
	off := false

	if _, err := c.Create(ctx, Request{Model: "m", PreviousResponseID: "resp_nope", Input: Items{UserText("x")}}); !errors.Is(err, &Error{Code: CodePreviousResponseNotFound}) {
		t.Errorf("unknown id: %v", err)
	}
	unstored, err := c.Create(ctx, Request{Model: "m", Store: &off, Input: Items{UserText("x")}})
	if err != nil {
		t.Fatal(err)
	}
	if store.Len() != 0 {
		t.Error("store:false response was saved")
	}
	if _, err := c.Create(ctx, Request{Model: "m", PreviousResponseID: unstored.ID, Input: Items{UserText("y")}}); !IsNotFound(err) {
		t.Errorf("continuing an unstored response: %v", err)
	}
	// A bad function_call_output on a valid continuation is rejected and
	// the stored history survives.
	first, err := c.Create(ctx, Request{Model: "m", Input: Items{UserText("x")}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Create(ctx, Request{Model: "m", PreviousResponseID: first.ID, Input: Items{NewFunctionCallOutput("call_missing", "?")}})
	if !IsInvalidRequest(err) {
		t.Errorf("bad output: %v", err)
	}
	if _, ok, _ := store.Load(ctx, first.ID); !ok {
		t.Error("shared store entry evicted by a client mistake")
	}
}

func TestHandlerStoreCompact(t *testing.T) {
	store := NewMemoryStore(8)
	srv, c := storeServer(t, store)
	ctx := testContext(t)
	first, err := c.Create(ctx, Request{Model: "m", Input: Items{UserText("remember slate")}})
	if err != nil {
		t.Fatal(err)
	}
	compact, err := c.Compact(ctx, CompactRequest{Model: "m", PreviousResponseID: first.ID, Input: Items{UserText("and this")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(compact.Output) != 1 {
		t.Fatalf("compact = %+v", compact)
	}
	if _, err := c.Compact(ctx, CompactRequest{Model: "m", PreviousResponseID: "resp_nope"}); !IsNotFound(err) {
		t.Errorf("unknown id: %v", err)
	}
	// Without a store the field passes through to the adapter untouched.
	plain := httptest.NewServer(NewHandler(recordingAdapter{}))
	defer plain.Close()
	req := CompactRequest{Model: "m", PreviousResponseID: "resp_adapter_knows", Input: Items{UserText("x")}}
	if _, err := NewClient(plain.URL).Compact(ctx, req); err == nil || !contains(err.Error(), "resp_adapter_knows") {
		t.Errorf("passthrough: %v", err)
	}
	_ = srv
}

// recordingAdapter reports what it received through the error message.
type recordingAdapter struct{ UnsupportedStreaming }

func (recordingAdapter) Create(_ context.Context, req Request) (*Response, error) {
	return nil, InvalidRequest("seen", "previous_response_id="+req.PreviousResponseID, "")
}

func (recordingAdapter) Compact(_ context.Context, req CompactRequest) (*CompactResponse, error) {
	return nil, InvalidRequest("seen", "previous_response_id="+req.PreviousResponseID, "")
}

func TestStreamIterator(t *testing.T) {
	ctx := testContext(t)
	var types []string
	var final *Response
	for ev, err := range Events(ctx, echoAdapter{}, Request{Model: "m", Input: Items{UserText("hi")}}) {
		if err != nil {
			t.Fatal(err)
		}
		types = append(types, ev.EventType())
		if r, ok := TerminalResponse(ev); ok {
			final = r
		}
	}
	if len(types) != 4 || types[0] != EventResponseCreated || final == nil || final.OutputText() != "hi" {
		t.Errorf("types = %v final = %+v", types, final)
	}

	// Errors end the sequence with the error.
	failing := &fakeAdapter{stream: func(_ context.Context, req Request, sink EventSink) error {
		if err := sink.Send(&ResponseCreatedEvent{Response: NewResponse(req)}); err != nil {
			return err
		}
		return ModelError("boom", "bad")
	}}
	var got []error
	n := 0
	for _, err := range Events(ctx, failing, Request{Model: "m"}) {
		n++
		if err != nil {
			got = append(got, err)
		}
	}
	if n != 2 || len(got) != 1 || !errors.Is(got[0], &Error{Type: ErrorTypeModelError}) {
		t.Errorf("n=%d errors=%v", n, got)
	}

	// Breaking out cancels the adapter.
	cancelled := make(chan error, 1)
	slow := &fakeAdapter{stream: func(ctx context.Context, req Request, sink EventSink) error {
		if err := sink.Send(&ResponseCreatedEvent{Response: NewResponse(req)}); err != nil {
			return err
		}
		<-ctx.Done()
		err := sink.Send(&OutputTextDeltaEvent{Delta: "x"})
		cancelled <- err
		return err
	}}
	for range Events(ctx, slow, Request{Model: "m"}) {
		break
	}
	if err := <-cancelled; !errors.Is(err, context.Canceled) {
		t.Errorf("adapter saw %v after break", err)
	}

	// Events are copies: mutating the adapter's object afterwards does not
	// change what was yielded.
	live := &Message{ID: "m1", Role: RoleAssistant, Content: Contents{&OutputText{Text: "before"}}}
	mutating := &fakeAdapter{stream: func(_ context.Context, req Request, sink EventSink) error {
		if err := sink.Send(&OutputItemAddedEvent{Item: live}); err != nil {
			return err
		}
		live.Content[0].(*OutputText).Text = "after"
		return nil
	}}
	for ev, err := range Events(ctx, mutating, Request{Model: "m"}) {
		if err != nil {
			t.Fatal(err)
		}
		if a, ok := ev.(*OutputItemAddedEvent); ok {
			data, _ := json.Marshal(a.Item)
			if !contains(string(data), "before") {
				t.Errorf("yielded item was mutated: %s", data)
			}
		}
	}
}

func TestStoreIsolatedFromAdapterMutation(t *testing.T) {
	store := NewMemoryStore(8)
	var lastOutput *Message
	scribbler := &fakeAdapter{stream: func(ctx context.Context, req Request, sink EventSink) error {
		if len(req.Input) > 1 {
			// A continuation: scribble on the history we were handed.
			for _, item := range req.Input {
				if m, ok := item.(*Message); ok {
					for _, part := range m.Content {
						if txt, ok := part.(*InputText); ok {
							txt.Text = "scribbled"
						}
					}
				}
			}
		}
		em := NewEmitter(sink, NewResponse(req))
		msg, err := em.Message("")
		if err != nil {
			return err
		}
		if err := msg.Text("hello"); err != nil {
			return err
		}
		return em.Complete()
	}}
	srv := httptest.NewServer(NewHandler(scribbler, WithResponseStore(store)))
	defer srv.Close()
	ctx := testContext(t)
	c := NewClient(srv.URL)
	first, err := c.Create(ctx, Request{Model: "m", Input: Items{UserText("first")}})
	if err != nil {
		t.Fatal(err)
	}
	lastOutput = first.Output[0].(*Message)
	if _, err := c.Create(ctx, Request{Model: "m", PreviousResponseID: first.ID, Input: Items{UserText("second")}}); err != nil {
		t.Fatal(err)
	}
	// Mutating what the adapter (or the client) still holds must not
	// reach the store either.
	lastOutput.Content[0].(*OutputText).Text = "later"

	hist, ok, err := store.Load(ctx, first.ID)
	if err != nil || !ok {
		t.Fatalf("load: ok=%v err=%v", ok, err)
	}
	if got := hist[0].(*Message).Text(); got != "first" {
		t.Errorf("stored input became %q", got)
	}
	if got := hist[1].(*Message).Text(); got != "hello" {
		t.Errorf("stored output became %q", got)
	}
}
