package openresponses

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func contains(s, sub string) bool      { return strings.Contains(s, sub) }
func replaceAll(s, o, n string) string { return strings.ReplaceAll(s, o, n) }

func goldenStream(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("testdata/golden/events.sse")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestEventStreamGolden(t *testing.T) {
	for _, variant := range []struct {
		name  string
		input func(string) string
	}{
		{"lf", func(s string) string { return s }},
		{"crlf", func(s string) string { return strings.ReplaceAll(s, "\n", "\r\n") }},
		{"no event lines", func(s string) string {
			var out []string
			for _, line := range strings.Split(s, "\n") {
				if !strings.HasPrefix(line, "event:") {
					out = append(out, line)
				}
			}
			return strings.Join(out, "\n")
		}},
	} {
		t.Run(variant.name, func(t *testing.T) {
			stream := NewEventStream(context.Background(), io.NopCloser(strings.NewReader(variant.input(goldenStream(t)))))
			defer stream.Close()
			var types []string
			var lastSeq int64 = -1
			for ev := range stream.Events() {
				types = append(types, ev.EventType())
				if ev.Sequence() != lastSeq+1 {
					t.Errorf("sequence %d after %d", ev.Sequence(), lastSeq)
				}
				lastSeq = ev.Sequence()
			}
			if err := stream.Err(); err != nil {
				t.Fatalf("stream error: %v", err)
			}
			if len(types) != 31 {
				t.Fatalf("got %d events: %v", len(types), types)
			}
			if types[27] != "acme:heartbeat" {
				t.Errorf("event 27 = %s", types[27])
			}
			resp := stream.Response()
			if resp == nil || resp.Status != ResponseStatusCompleted {
				t.Fatalf("final response = %+v", resp)
			}
			if resp.OutputText() != "Hello, world" {
				t.Errorf("OutputText = %q", resp.OutputText())
			}
			if len(resp.Output) != 4 {
				t.Errorf("output len = %d", len(resp.Output))
			}
		})
	}
}

func TestAccumulatorFromDeltas(t *testing.T) {
	// Drop the terminal snapshot so the accumulator must build the
	// response from item and delta events alone.
	input := goldenStream(t)
	cut := strings.Index(input, "event: response.completed")
	input = input[:cut] + "data: [DONE]\n\n"
	stream := NewEventStream(context.Background(), io.NopCloser(strings.NewReader(input)))
	defer stream.Close()
	for range stream.Events() {
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("err = %v", err)
	}
	resp := stream.Response()
	if resp.OutputText() != "Hello, world" {
		t.Errorf("OutputText = %q", resp.OutputText())
	}
	rs := resp.Output[0].(*ReasoningItem)
	if rs.Summary.Text() != "think" || rs.Content.Text() != "deep" {
		t.Errorf("reasoning = %+v", rs)
	}
	msg := resp.Output[1].(*Message)
	if msg.Content[1].(*Refusal).Refusal != "nope" {
		t.Errorf("refusal = %+v", msg.Content[1])
	}
	if len(msg.Content[0].(*OutputText).Annotations) != 1 {
		t.Errorf("annotations = %+v", msg.Content[0].(*OutputText).Annotations)
	}
	if fc := resp.Output[2].(*FunctionCall); fc.Arguments != `{"q":"cat"}` {
		t.Errorf("arguments = %q", fc.Arguments)
	}
}

func TestEventStreamTruncated(t *testing.T) {
	input := "event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"r\"}}\n\n"
	stream := NewEventStream(context.Background(), io.NopCloser(strings.NewReader(input)))
	defer stream.Close()
	n := 0
	for range stream.Events() {
		n++
	}
	if n != 1 {
		t.Errorf("events = %d", n)
	}
	if !errors.Is(stream.Err(), ErrTruncatedStream) {
		t.Errorf("err = %v", stream.Err())
	}
}

func TestEventStreamErrorThenFailed(t *testing.T) {
	input := "event: error\ndata: {\"type\":\"error\",\"sequence_number\":0,\"error\":{\"type\":\"server_error\",\"code\":\"boom\",\"message\":\"bad\",\"param\":null}}\n\n" +
		"event: response.failed\ndata: {\"type\":\"response.failed\",\"sequence_number\":1,\"response\":{\"id\":\"r\",\"status\":\"failed\",\"error\":{\"code\":\"boom\",\"message\":\"bad\"}}}\n\n" +
		"data: [DONE]\n\n"
	stream := NewEventStream(context.Background(), io.NopCloser(strings.NewReader(input)))
	defer stream.Close()
	resp, err := stream.Wait()
	if resp == nil || resp.Status != ResponseStatusFailed {
		t.Fatalf("resp = %+v", resp)
	}
	var e *Error
	if !errors.As(err, &e) || e.Code != "boom" {
		t.Errorf("err = %v", err)
	}
}

func TestEventStreamBareErrorEnvelope(t *testing.T) {
	ev, err := DecodeEvent([]byte(`{"error":{"type":"too_many_requests","code":"rate","message":"slow down","param":null}}`))
	if err != nil {
		t.Fatal(err)
	}
	e, ok := ev.(*ErrorEvent)
	if !ok || e.Error.Code != "rate" {
		t.Errorf("event = %#v", ev)
	}
	if !IsRateLimited(e.Err()) {
		t.Errorf("Err() = %v", e.Err())
	}
}

func TestEventStreamMalformedFrame(t *testing.T) {
	stream := NewEventStream(context.Background(), io.NopCloser(strings.NewReader("data: {not json\n\n")))
	defer stream.Close()
	if stream.Next() {
		t.Fatal("expected no event")
	}
	if stream.Err() == nil {
		t.Fatal("expected error")
	}
}

func TestEventStreamContextCancel(t *testing.T) {
	pr, pw := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	stream := NewEventStream(ctx, pr)
	defer stream.Close()
	go func() {
		_, _ = io.WriteString(pw, "event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"r\"}}\n\n")
	}()
	if !stream.Next() {
		t.Fatalf("first event missing: %v", stream.Err())
	}
	cancel()
	if stream.Next() {
		t.Fatal("expected stream to stop")
	}
	if !errors.Is(stream.Err(), context.Canceled) {
		t.Errorf("err = %v", stream.Err())
	}
	_ = pw.Close()
}

// callsAt streams function calls the way Ollama 0.23 streams a parallel
// batch: each call whole, opened and closed before the next, and every
// one at the same output index. Each call's arguments name its id so a
// test can tell whose arguments landed where.
func callsAt(idx int, ids ...string) []StreamEvent {
	var evs []StreamEvent
	for _, id := range ids {
		args := `{"q":"` + id + `"}`
		evs = append(evs,
			&OutputItemAddedEvent{OutputIndex: idx, Item: &FunctionCall{ID: id, CallID: id, Name: "f", Status: StatusInProgress}},
			&FunctionCallArgumentsDeltaEvent{OutputIndex: idx, ItemID: id, Delta: args},
			&FunctionCallArgumentsDoneEvent{OutputIndex: idx, ItemID: id, Arguments: args},
			&OutputItemDoneEvent{OutputIndex: idx, Item: &FunctionCall{ID: id, CallID: id, Name: "f", Arguments: args, Status: StatusCompleted}},
		)
	}
	return evs
}

func callIDs(out Items) (ids, args []string) {
	for _, it := range out {
		fc, ok := it.(*FunctionCall)
		if !ok {
			ids, args = append(ids, fmt.Sprintf("%T", it)), append(args, "")
			continue
		}
		ids, args = append(ids, fc.ID), append(args, fc.Arguments)
	}
	return ids, args
}

func created() StreamEvent {
	return &ResponseCreatedEvent{Response: &Response{ID: "r", Status: ResponseStatusInProgress}}
}

func concat(parts ...[]StreamEvent) []StreamEvent {
	var all []StreamEvent
	for _, p := range parts {
		all = append(all, p...)
	}
	return all
}

// TestAccumulatorReusedIndex covers openresponses#26: a stream that
// reuses an output index after closing the item there must not lose the
// earlier item, and must say that it happened.
func TestAccumulatorReusedIndex(t *testing.T) {
	fc := func(id string) *FunctionCall {
		return &FunctionCall{ID: id, CallID: id, Name: "f", Arguments: `{"q":"` + id + `"}`, Status: StatusCompleted}
	}
	tests := []struct {
		name       string
		events     []StreamEvent
		wantIDs    []string
		wantArgs   []string // nil to skip
		wantReused []int
	}{
		{
			name:       "calls at their own indexes",
			events:     concat([]StreamEvent{created()}, callsAt(0, "a"), callsAt(1, "b")),
			wantIDs:    []string{"a", "b"},
			wantReused: nil,
		},
		{
			// The filing's shape, cut before any terminal snapshot: both
			// calls must be in the response, each with its own arguments.
			name:       "two calls at one index",
			events:     concat([]StreamEvent{created()}, callsAt(0, "a", "b")),
			wantIDs:    []string{"a", "b"},
			wantArgs:   []string{`{"q":"a"}`, `{"q":"b"}`},
			wantReused: []int{0},
		},
		{
			name:       "three calls at one index",
			events:     concat([]StreamEvent{created()}, callsAt(0, "a", "b", "c")),
			wantIDs:    []string{"a", "b", "c"},
			wantReused: []int{0, 0},
		},
		{
			// Index 1 still reaches position 1 after index 0 has moved
			// to position 2.
			name: "a later event at an unmoved index reaches its item",
			events: concat([]StreamEvent{created()}, callsAt(0, "a"), callsAt(1, "b"), callsAt(0, "c"),
				[]StreamEvent{&FunctionCallArgumentsDoneEvent{OutputIndex: 1, ItemID: "b", Arguments: "late"}}),
			wantIDs:    []string{"a", "b", "c"},
			wantArgs:   []string{`{"q":"a"}`, "late", `{"q":"c"}`},
			wantReused: []int{0},
		},
		{
			// An added at an index whose item is still open re-announces
			// it; that is a replacement, as before, not a reuse.
			name: "re-announced open item is replaced",
			events: []StreamEvent{created(),
				&OutputItemAddedEvent{OutputIndex: 0, Item: fc("a")},
				&OutputItemAddedEvent{OutputIndex: 0, Item: fc("a2")}},
			wantIDs:    []string{"a2"},
			wantReused: nil,
		},
		{
			// The server's own list wins, in its order; the record of the
			// reuse stays so a caller knows positions were not indexes.
			name: "terminal snapshot replaces the output",
			events: concat([]StreamEvent{created()}, callsAt(0, "a", "b"), []StreamEvent{
				&ResponseCompletedEvent{Response: &Response{ID: "r", Status: ResponseStatusCompleted, Output: Items{fc("b"), fc("a")}}}}),
			wantIDs:    []string{"b", "a"},
			wantReused: []int{0},
		},
		{
			// Once positions have diverged, a snapshot before the terminal
			// one does not replace the accumulated list, whatever order
			// the server lists; a new index is appended.
			name: "snapshot with output after a reuse keeps the accumulated list",
			events: concat([]StreamEvent{created()}, callsAt(0, "a", "b"), []StreamEvent{
				&ResponseInProgressEvent{Response: &Response{ID: "r", Status: ResponseStatusInProgress, Output: Items{fc("b"), fc("a")}}}},
				callsAt(2, "c")),
			wantIDs:    []string{"a", "b", "c"},
			wantReused: []int{0},
		},
		{
			// An item only the kept snapshot names is still kept, once,
			// appended by id; an id-less one cannot be told apart.
			name: "snapshot after a reuse naming an item never streamed",
			events: concat([]StreamEvent{created()}, callsAt(0, "a", "b"), []StreamEvent{
				&ResponseInProgressEvent{Response: &Response{ID: "r", Status: ResponseStatusInProgress, Output: Items{fc("a"), fc("b"), fc("x"), &FunctionCall{CallID: "n"}}}},
				&ResponseInProgressEvent{Response: &Response{ID: "r", Status: ResponseStatusInProgress, Output: Items{fc("a"), fc("b"), fc("x")}}}},
				callsAt(1, "c")),
			wantIDs:    []string{"a", "b", "x", "c"},
			wantReused: []int{0},
		},
		{
			// Reviewer's case: the snapshot arrives while the reused
			// index's item is still open. Its later events must reach it,
			// not the first item.
			name: "snapshot while a reused item is open",
			events: concat([]StreamEvent{created()}, callsAt(0, "a"), []StreamEvent{
				&OutputItemAddedEvent{OutputIndex: 0, Item: &FunctionCall{ID: "b", CallID: "b", Name: "f", Status: StatusInProgress}},
				&ResponseInProgressEvent{Response: &Response{ID: "r", Status: ResponseStatusInProgress, Output: Items{fc("a"), &FunctionCall{ID: "b", CallID: "b", Name: "f", Status: StatusInProgress}}}},
				&FunctionCallArgumentsDeltaEvent{OutputIndex: 0, ItemID: "b", Delta: "+"},
				&OutputItemDoneEvent{OutputIndex: 0, Item: fc("b")}},
				callsAt(1, "c")),
			wantIDs:    []string{"a", "b", "c"},
			wantArgs:   []string{`{"q":"a"}`, `{"q":"b"}`, `{"q":"c"}`},
			wantReused: []int{0},
		},
		{
			// The same with no ids anywhere: nothing to match by, and
			// still nothing lost.
			name: "snapshot while a reused item is open, no ids",
			events: []StreamEvent{created(),
				&OutputItemAddedEvent{OutputIndex: 0, Item: &FunctionCall{CallID: "a", Name: "f"}},
				&OutputItemDoneEvent{OutputIndex: 0, Item: &FunctionCall{CallID: "a", Name: "f", Arguments: "A", Status: StatusCompleted}},
				&OutputItemAddedEvent{OutputIndex: 0, Item: &FunctionCall{CallID: "b", Name: "f"}},
				&ResponseInProgressEvent{Response: &Response{ID: "r", Status: ResponseStatusInProgress, Output: Items{
					&FunctionCall{CallID: "a", Name: "f", Arguments: "A", Status: StatusCompleted}, &FunctionCall{CallID: "b", Name: "f"}}}},
				&FunctionCallArgumentsDeltaEvent{OutputIndex: 0, Delta: "B"},
				&OutputItemDoneEvent{OutputIndex: 0, Item: &FunctionCall{CallID: "b", Name: "f", Arguments: "B", Status: StatusCompleted}}},
			wantIDs:    []string{"", ""},
			wantArgs:   []string{"A", "B"},
			wantReused: []int{0},
		},
		{
			// A snapshot that omits the open item changes nothing either.
			name: "snapshot omitting the open reused item",
			events: concat([]StreamEvent{created()}, callsAt(0, "a"), []StreamEvent{
				&OutputItemAddedEvent{OutputIndex: 0, Item: &FunctionCall{ID: "b", CallID: "b", Name: "f"}},
				&ResponseInProgressEvent{Response: &Response{ID: "r", Status: ResponseStatusInProgress, Output: Items{fc("a")}}},
				&OutputItemDoneEvent{OutputIndex: 0, Item: fc("b")}}),
			wantIDs:    []string{"a", "b"},
			wantReused: []int{0},
		},
		{
			// A done with another id at a closed position is another
			// item, as an added would be; one without an id restates it.
			name: "done with another id at a closed position",
			events: concat([]StreamEvent{created()}, callsAt(0, "a"), []StreamEvent{
				&OutputItemDoneEvent{OutputIndex: 0, Item: fc("b")},
				&OutputItemDoneEvent{OutputIndex: 0, Item: &FunctionCall{CallID: "b", Name: "f", Arguments: "again"}}}),
			wantIDs:    []string{"a", ""},
			wantArgs:   []string{`{"q":"a"}`, "again"},
			wantReused: []int{0},
		},
		{
			// An empty in_progress payload keeps the accumulated output
			// and with it the positions.
			name: "snapshot without output keeps positions",
			events: concat([]StreamEvent{created()}, callsAt(0, "a", "b"), []StreamEvent{
				&ResponseInProgressEvent{Response: &Response{ID: "r", Status: ResponseStatusInProgress}}},
				callsAt(0, "c")),
			wantIDs:    []string{"a", "b", "c"},
			wantReused: []int{0, 0},
		},
		{
			// Reviewer's case: a call reused at index 0 and still open
			// when a genuine index 1 opens. Index 1 has not been seen, so
			// it is appended rather than landing on the appended call.
			name: "a new index after a reuse does not land on the appended item",
			events: concat([]StreamEvent{created()}, callsAt(0, "a"), []StreamEvent{
				&OutputItemAddedEvent{OutputIndex: 0, Item: fc("b")},
				&OutputItemAddedEvent{OutputIndex: 1, Item: fc("c")},
				&OutputItemDoneEvent{OutputIndex: 0, Item: fc("b")},
				&OutputItemDoneEvent{OutputIndex: 1, Item: fc("c")}}),
			wantIDs:    []string{"a", "b", "c"},
			wantReused: []int{0},
		},
		{
			// A fresh index after a reuse is not itself a reuse, even when
			// it equals an occupied position.
			name:       "a fresh index after a reuse is not reported",
			events:     concat([]StreamEvent{created()}, callsAt(0, "a", "b"), callsAt(1, "c")),
			wantIDs:    []string{"a", "b", "c"},
			wantReused: []int{0},
		},
		{
			// A snapshot carrying output between two calls at one index:
			// the first stays closed, known by its id, so the second is
			// still a reuse and not a re-announcement.
			name: "snapshot with output between reuses keeps the closed item",
			events: concat([]StreamEvent{created()}, callsAt(0, "a"), []StreamEvent{
				&ResponseInProgressEvent{Response: &Response{ID: "r", Status: ResponseStatusInProgress, Output: Items{fc("a")}}}},
				callsAt(0, "b")),
			wantIDs:    []string{"a", "b"},
			wantReused: []int{0},
		},
		{
			// Without ids, the snapshot's own status says the item is
			// finished.
			name: "snapshot with a completed item and no ids",
			events: []StreamEvent{created(),
				&OutputItemAddedEvent{OutputIndex: 0, Item: &FunctionCall{CallID: "a", Name: "f"}},
				&OutputItemDoneEvent{OutputIndex: 0, Item: &FunctionCall{CallID: "a", Name: "f", Status: StatusCompleted}},
				&ResponseInProgressEvent{Response: &Response{ID: "r", Status: ResponseStatusInProgress, Output: Items{&FunctionCall{CallID: "a", Name: "f", Status: StatusCompleted}}}},
				&OutputItemAddedEvent{OutputIndex: 0, Item: &FunctionCall{CallID: "b", Name: "f"}},
				&OutputItemDoneEvent{OutputIndex: 0, Item: &FunctionCall{CallID: "b", Name: "f", Status: StatusCompleted}}},
			wantIDs:    []string{"", ""},
			wantArgs:   []string{"", ""},
			wantReused: []int{0},
		},
		{
			// The same item announced again after its done, by id, is
			// the same item, not a second copy.
			name: "re-added closed item with the same id is replaced",
			events: concat([]StreamEvent{created()}, callsAt(0, "a"), []StreamEvent{
				&OutputItemAddedEvent{OutputIndex: 0, Item: &FunctionCall{ID: "a", CallID: "a", Name: "f"}},
				&OutputItemDoneEvent{OutputIndex: 0, Item: fc("a")}}),
			wantIDs:    []string{"a"},
			wantReused: nil,
		},
		{
			// A delta at an index the stream never opened reaches nothing
			// once positions have diverged, rather than another item.
			name: "delta at an unopened index after a reuse is dropped",
			events: concat([]StreamEvent{created()}, callsAt(0, "a", "b"), []StreamEvent{
				&FunctionCallArgumentsDoneEvent{OutputIndex: 1, Arguments: "stray"}}),
			wantIDs:    []string{"a", "b"},
			wantArgs:   []string{`{"q":"a"}`, `{"q":"b"}`},
			wantReused: []int{0},
		},
		{
			// A message at a reused index streams its parts into the new
			// item, not the closed one.
			name: "message parts after a reuse reach the new item",
			events: concat([]StreamEvent{created()}, callsAt(0, "a"), []StreamEvent{
				&OutputItemAddedEvent{OutputIndex: 0, Item: &Message{ID: "m", Role: RoleAssistant}},
				&ContentPartAddedEvent{OutputIndex: 0, ItemID: "m", ContentIndex: 0, Part: &OutputText{}},
				&OutputTextDeltaEvent{OutputIndex: 0, ItemID: "m", ContentIndex: 0, Delta: "hi"},
				&OutputItemDoneEvent{OutputIndex: 0, Item: &Message{ID: "m", Role: RoleAssistant, Content: Contents{&OutputText{Text: "hi"}}}}}),
			wantIDs:    []string{"a", "*openresponses.Message"},
			wantReused: []int{0},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var acc Accumulator
			for _, ev := range tt.events {
				acc.Add(ev)
			}
			ids, args := callIDs(acc.Response().Output)
			if !slices.Equal(ids, tt.wantIDs) {
				t.Errorf("output ids = %q, want %q", ids, tt.wantIDs)
			}
			if tt.wantArgs != nil && !slices.Equal(args, tt.wantArgs) {
				t.Errorf("arguments = %q, want %q", args, tt.wantArgs)
			}
			if got := acc.ReusedIndexes(); !slices.Equal(got, tt.wantReused) {
				t.Errorf("ReusedIndexes = %v, want %v", got, tt.wantReused)
			}
		})
	}
}

// TestAccumulatorReusedIndexMidStream reads the response between events,
// as a consumer of EventStream.Response does, and checks the first call
// is never lost on the way.
func TestAccumulatorReusedIndexMidStream(t *testing.T) {
	var acc Accumulator
	events := concat([]StreamEvent{created()}, callsAt(0, "a", "b"))
	for i, ev := range events {
		acc.Add(ev)
		ids, _ := callIDs(acc.Response().Output)
		if i >= 1 && ids[0] != "a" {
			t.Fatalf("after event %d (%s): output ids = %q, call a lost", i, ev.EventType(), ids)
		}
	}
	if text := acc.ReusedIndexes(); !slices.Equal(text, []int{0}) {
		t.Errorf("ReusedIndexes = %v", text)
	}
	// The returned slice is a copy.
	acc.ReusedIndexes()[0] = 9
	if got := acc.ReusedIndexes(); got[0] != 0 {
		t.Errorf("ReusedIndexes returned its own storage: %v", got)
	}
}

// TestEventStreamReusedIndex is the filing's scenario over the wire: an
// SSE stream with every call at output index 0, cut before any terminal
// snapshot. The response must hold both calls and say the index was
// reused.
func TestEventStreamReusedIndex(t *testing.T) {
	var b strings.Builder
	for _, ev := range concat([]StreamEvent{created()}, callsAt(0, "a", "b")) {
		data, err := EncodeEvent(ev)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "event: %s\ndata: %s\n\n", ev.EventType(), data)
	}
	stream := NewEventStream(context.Background(), io.NopCloser(strings.NewReader(b.String())))
	defer stream.Close()
	for range stream.Events() {
	}
	if err := stream.Err(); !errors.Is(err, ErrTruncatedStream) {
		t.Fatalf("err = %v, want ErrTruncatedStream", err)
	}
	ids, args := callIDs(stream.Response().Output)
	if !slices.Equal(ids, []string{"a", "b"}) || !slices.Equal(args, []string{`{"q":"a"}`, `{"q":"b"}`}) {
		t.Errorf("output = %q %q", ids, args)
	}
	if got := stream.ReusedIndexes(); !slices.Equal(got, []int{0}) {
		t.Errorf("ReusedIndexes = %v, want [0]", got)
	}
}

// TestAccumulatorPosition covers Position and ItemAt: where the item an
// event at an output index names sits in Response().Output, in streams
// that follow the lifecycle and in streams that reuse an index.
func TestAccumulatorPosition(t *testing.T) {
	fc := func(id string, status Status) *FunctionCall {
		return &FunctionCall{ID: id, CallID: id, Name: "f", Status: status}
	}
	snapshot := func(status ResponseStatus, items ...Item) StreamEvent {
		resp := &Response{ID: "r", Status: status, Output: Items(items)}
		if status.Terminal() {
			return &ResponseCompletedEvent{Response: resp}
		}
		return &ResponseInProgressEvent{Response: resp}
	}
	type want struct {
		idx int
		pos int    // position in Output; ignored unless id is set
		id  string // "" means no item: Position and ItemAt report false
	}
	tests := []struct {
		name   string
		events []StreamEvent
		wants  []want
		ids    []string // Response().Output ids after the events
	}{
		{
			name:   "no events",
			events: nil,
			wants:  []want{{idx: 0}, {idx: -1}},
		},
		{
			name:   "created only",
			events: []StreamEvent{created()},
			wants:  []want{{idx: 0}},
		},
		{
			name:   "conforming stream: position is the index",
			events: concat([]StreamEvent{created()}, callsAt(0, "a"), callsAt(1, "b"), callsAt(2, "c")),
			wants:  []want{{0, 0, "a"}, {1, 1, "b"}, {2, 2, "c"}, {idx: 3}, {idx: -1}},
			ids:    []string{"a", "b", "c"},
		},
		{
			name:   "index never opened, below one that was",
			events: []StreamEvent{created(), &OutputItemAddedEvent{OutputIndex: 2, Item: fc("c", StatusInProgress)}},
			wants:  []want{{idx: 0}, {idx: 1}, {2, 2, "c"}},
			ids:    []string{"<nil>", "<nil>", "c"}, // holes are nil items
		},
		{
			name:   "reuse at 0 (Ollama): the index names the latest item",
			events: concat([]StreamEvent{created()}, callsAt(0, "a", "b", "c")),
			wants:  []want{{0, 2, "c"}, {idx: 1}, {idx: 2}},
			ids:    []string{"a", "b", "c"},
		},
		{
			name:   "interleaved reuse: untouched indexes keep their positions",
			events: concat([]StreamEvent{created()}, callsAt(0, "a"), callsAt(1, "b"), callsAt(0, "c")),
			wants:  []want{{0, 2, "c"}, {1, 1, "b"}, {idx: 2}},
			ids:    []string{"a", "b", "c"},
		},
		{
			name:   "interleaved reuse, then the other index is reused",
			events: concat([]StreamEvent{created()}, callsAt(0, "a"), callsAt(1, "b"), callsAt(0, "c"), callsAt(1, "d")),
			wants:  []want{{0, 2, "c"}, {1, 3, "d"}},
			ids:    []string{"a", "b", "c", "d"},
		},
		{
			name:   "a new index after a reuse is appended",
			events: concat([]StreamEvent{created()}, callsAt(0, "a", "b"), callsAt(1, "c")),
			wants:  []want{{0, 1, "b"}, {1, 2, "c"}},
			ids:    []string{"a", "b", "c"},
		},
		{
			name: "re-announced open item keeps its position",
			events: []StreamEvent{created(),
				&OutputItemAddedEvent{OutputIndex: 0, Item: fc("a", StatusInProgress)},
				&OutputItemAddedEvent{OutputIndex: 0, Item: fc("a2", StatusInProgress)}},
			wants: []want{{0, 0, "a2"}},
			ids:   []string{"a2"},
		},
		{
			name: "terminal snapshot after a reuse: positions are the server's indexes",
			events: concat([]StreamEvent{created()}, callsAt(0, "a", "b"),
				[]StreamEvent{snapshot(ResponseStatusCompleted, fc("b", StatusCompleted), fc("a", StatusCompleted))}),
			wants: []want{{0, 0, "b"}, {1, 1, "a"}, {idx: 2}},
			ids:   []string{"b", "a"},
		},
		{
			name: "terminal snapshot shorter than what was opened",
			events: concat([]StreamEvent{created()}, callsAt(0, "a"), callsAt(1, "b"), callsAt(0, "c"),
				[]StreamEvent{snapshot(ResponseStatusCompleted, fc("x", StatusCompleted))}),
			wants: []want{{0, 0, "x"}, {idx: 1}, {idx: 2}},
			ids:   []string{"x"},
		},
		{
			name: "terminal snapshot with no output, nothing was kept",
			events: concat([]StreamEvent{created()}, callsAt(0, "a", "b"),
				[]StreamEvent{snapshot(ResponseStatusCompleted)}),
			wants: []want{{idx: 0}, {idx: 1}},
			ids:   nil,
		},
		{
			name: "snapshot before divergence replaces the list; positions are indexes",
			events: concat([]StreamEvent{created()}, callsAt(0, "a"), callsAt(1, "b"),
				[]StreamEvent{snapshot(ResponseStatusInProgress, fc("b", StatusCompleted), fc("a", StatusCompleted))}),
			wants: []want{{0, 0, "b"}, {1, 1, "a"}},
			ids:   []string{"b", "a"},
		},
		{
			name: "snapshot before divergence that lists fewer items drops the rest",
			events: concat([]StreamEvent{created()}, callsAt(0, "a"), callsAt(1, "b"),
				[]StreamEvent{snapshot(ResponseStatusInProgress, fc("a", StatusCompleted))}),
			wants: []want{{0, 0, "a"}, {idx: 1}},
			ids:   []string{"a"},
		},
		{
			name: "snapshot with no output keeps the list and the mapping",
			events: concat([]StreamEvent{created()}, callsAt(0, "a"), callsAt(1, "b"),
				[]StreamEvent{snapshot(ResponseStatusInProgress)}),
			wants: []want{{0, 0, "a"}, {1, 1, "b"}},
			ids:   []string{"a", "b"},
		},
		{
			// The snapshot closes a, so the added that follows at its
			// index is a reuse, not a re-announcement.
			name: "snapshot before divergence, then a reuse at an index it listed",
			events: concat([]StreamEvent{created()},
				[]StreamEvent{snapshot(ResponseStatusInProgress, fc("a", StatusCompleted))},
				callsAt(0, "b")),
			wants: []want{{0, 1, "b"}},
			ids:   []string{"a", "b"},
		},
		{
			// Positions have diverged, so the snapshot's list is not
			// taken; items it alone names are appended with no index.
			name: "snapshot after a reuse keeps the mapping and appends what it alone names",
			events: concat([]StreamEvent{created()}, callsAt(0, "a", "b"),
				[]StreamEvent{snapshot(ResponseStatusInProgress, fc("z", StatusCompleted), fc("a", StatusCompleted), fc("b", StatusCompleted))}),
			wants: []want{{0, 1, "b"}, {idx: 1}},
			ids:   []string{"a", "b", "z"},
		},
		{
			name: "after that snapshot a new index is appended past the unnamed item",
			events: concat([]StreamEvent{created()}, callsAt(0, "a", "b"),
				[]StreamEvent{snapshot(ResponseStatusInProgress, fc("z", StatusCompleted))},
				callsAt(1, "c")),
			wants: []want{{0, 1, "b"}, {1, 3, "c"}},
			ids:   []string{"a", "b", "z", "c"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var acc Accumulator
			for _, ev := range tt.events {
				acc.Add(ev)
			}
			for _, w := range tt.wants {
				pos, ok := acc.Position(w.idx)
				item, itemOK := acc.ItemAt(w.idx)
				if ok != itemOK {
					t.Errorf("index %d: Position ok = %v but ItemAt ok = %v", w.idx, ok, itemOK)
				}
				if w.id == "" {
					if ok || item != nil || pos != 0 {
						t.Errorf("index %d: Position = (%d, %v), ItemAt = (%v, %v), want none", w.idx, pos, ok, item, itemOK)
					}
					continue
				}
				if !ok || pos != w.pos {
					t.Errorf("index %d: Position = (%d, %v), want (%d, true)", w.idx, pos, ok, w.pos)
					continue
				}
				if !itemOK || item != acc.Response().Output[pos] {
					t.Errorf("index %d: ItemAt is not Response().Output[%d]", w.idx, pos)
				}
				if got, _ := itemIdentity(item); got != w.id {
					t.Errorf("index %d: item id = %q, want %q", w.idx, got, w.id)
				}
			}
			if tt.ids != nil || acc.Response() != nil && len(acc.Response().Output) > 0 {
				ids, _ := callIDs(acc.Response().Output)
				if !slices.Equal(ids, tt.ids) {
					t.Errorf("output ids = %q, want %q", ids, tt.ids)
				}
			}
		})
	}
}

// TestAccumulatorPositionMidStream asks after every event, as agentturn's
// loop does: an index names the item most recently added there, and the
// answer moves to the new item when the next one is added.
func TestAccumulatorPositionMidStream(t *testing.T) {
	var acc Accumulator
	events := concat([]StreamEvent{created()}, callsAt(0, "a", "b"))
	type at struct {
		pos int
		id  string
	}
	// What Position(0) reports after each event: nothing until the first
	// added, a at 0 through its done, b at 1 from its added on.
	want := map[int]at{
		1: {0, "a"}, 2: {0, "a"}, 3: {0, "a"}, 4: {0, "a"},
		5: {1, "b"}, 6: {1, "b"}, 7: {1, "b"}, 8: {1, "b"},
	}
	for i, ev := range events {
		acc.Add(ev)
		pos, ok := acc.Position(0)
		w, has := want[i]
		if !has {
			if ok {
				t.Errorf("after event %d (%s): Position(0) = (%d, true), want none", i, ev.EventType(), pos)
			}
			continue
		}
		if !ok || pos != w.pos {
			t.Errorf("after event %d (%s): Position(0) = (%d, %v), want (%d, true)", i, ev.EventType(), pos, ok, w.pos)
			continue
		}
		item, _ := acc.ItemAt(0)
		if id, _ := itemIdentity(item); id != w.id {
			t.Errorf("after event %d (%s): ItemAt(0) is %q, want %q", i, ev.EventType(), id, w.id)
		}
	}
	// Arguments streamed at index 0 reach the item Position names.
	acc.Add(&FunctionCallArgumentsDeltaEvent{OutputIndex: 0, ItemID: "b", Delta: "+"})
	item, _ := acc.ItemAt(0)
	if fc := item.(*FunctionCall); fc.ID != "b" || !strings.HasSuffix(fc.Arguments, "+") {
		t.Errorf("ItemAt(0) = %+v, want call b with the delta", fc)
	}
}

// TestAccumulatorPositionReadsChangeNothing checks that asking, for any
// index, assigns no slot and leaves the bookkeeping as it was, so a
// later added at an index that was asked about first lands where it
// would have without the question.
func TestAccumulatorPositionReadsChangeNothing(t *testing.T) {
	build := func(ask bool) *Accumulator {
		acc := &Accumulator{}
		for _, ev := range concat([]StreamEvent{created()}, callsAt(0, "a", "b")) {
			acc.Add(ev)
			if ask {
				for idx := -1; idx < 4; idx++ {
					acc.Position(idx)
					acc.ItemAt(idx)
				}
			}
		}
		// Index 3 first appears after the reuse.
		for _, ev := range callsAt(3, "c") {
			acc.Add(ev)
			if ask {
				acc.Position(3)
			}
		}
		return acc
	}
	asked, quiet := build(true), build(false)
	if !reflect.DeepEqual(asked.slots, quiet.slots) || !reflect.DeepEqual(asked.closed, quiet.closed) || !reflect.DeepEqual(asked.reused, quiet.reused) {
		t.Errorf("asking changed the bookkeeping: slots %v vs %v, closed %v vs %v", asked.slots, quiet.slots, asked.closed, quiet.closed)
	}
	if got, _ := callIDs(asked.Response().Output); !slices.Equal(got, []string{"a", "b", "c"}) {
		t.Errorf("output ids = %q, want [a b c]", got)
	}
	if pos, ok := asked.Position(3); !ok || pos != 2 {
		t.Errorf("Position(3) = (%d, %v), want (2, true)", pos, ok)
	}
}

// TestAccumulatorPositionFallback covers the one case Add alone does not
// build: before any reuse, an index that has no item of its own maps to
// its own position when Output has an item there, and after a reuse it
// does not.
func TestAccumulatorPositionFallback(t *testing.T) {
	held := func(reused []int) *Accumulator {
		return &Accumulator{
			resp:   &Response{Output: Items{&FunctionCall{ID: "a"}, &FunctionCall{ID: "b"}}},
			reused: reused,
		}
	}
	acc := held(nil)
	if pos, ok := acc.Position(1); !ok || pos != 1 {
		t.Errorf("Position(1) = (%d, %v), want (1, true)", pos, ok)
	}
	if item, ok := acc.ItemAt(1); !ok || item != acc.resp.Output[1] {
		t.Errorf("ItemAt(1) = (%v, %v), want Output[1]", item, ok)
	}
	if _, ok := acc.Position(2); ok {
		t.Error("Position(2) past the end of Output reported an item")
	}
	if _, ok := held([]int{0}).Position(1); ok {
		t.Error("Position(1) of an index never opened reported an item after a reuse")
	}
}
