// Package streamtest helps adapter authors unit-test their streaming
// output without a live server. [Sink] records events the way the HTTP
// handler would, and [Validate] checks the recorded sequence against the
// item lifecycle rules of the specification, so an ordering mistake fails
// a Go test instead of decoding to the wrong response at a client.
//
//	sink, err := streamtest.Run(ctx, adapter, req)
//	if err != nil { t.Fatal(err) }
//	if sink.Response().OutputText() != "hello" { ... }
package streamtest

import (
	"context"
	"fmt"
	"sync"

	"github.com/ChristopherDavenport/openresponses"
)

// Sink records streaming events. It assigns sequence numbers, folds the
// events into a response and rejects events after a terminal one, like
// the transports do. The zero value is ready to use.
type Sink struct {
	mu       sync.Mutex
	seq      int64
	events   []openresponses.StreamEvent
	acc      openresponses.Accumulator
	terminal bool
}

var _ openresponses.EventSink = (*Sink)(nil)

// Send records ev.
func (s *Sink) Send(ev openresponses.StreamEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.terminal {
		return openresponses.ErrTerminalEventSent
	}
	if setter, ok := ev.(interface{ SetSequence(int64) }); ok {
		setter.SetSequence(s.seq)
	}
	s.seq++
	// Snapshot through the wire so later mutation of the adapter's
	// objects does not rewrite history, exactly as a client would see it.
	data, err := openresponses.EncodeEvent(ev)
	if err != nil {
		return err
	}
	decoded, err := openresponses.DecodeEvent(data)
	if err != nil {
		return err
	}
	s.events = append(s.events, decoded)
	s.acc.Add(decoded)
	if _, ok := openresponses.TerminalResponse(decoded); ok {
		s.terminal = true
	}
	return nil
}

// Events returns the recorded events, decoded from their wire form.
func (s *Sink) Events() []openresponses.StreamEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]openresponses.StreamEvent(nil), s.events...)
}

// Response returns the response folded from the recorded events.
func (s *Sink) Response() *openresponses.Response {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.acc.Response()
}

// Run streams req through the adapter into a fresh Sink and validates
// the sequence. It returns the sink even when validation fails so the
// test can inspect the events.
func Run(ctx context.Context, adapter openresponses.Streamer, req openresponses.Request) (*Sink, error) {
	sink := &Sink{}
	if err := adapter.CreateStream(ctx, req, sink); err != nil {
		return sink, fmt.Errorf("CreateStream: %w", err)
	}
	if err := Validate(sink.Events()); err != nil {
		return sink, err
	}
	return sink, nil
}

// Validate checks that events form a well-ordered stream:
//
//   - it starts with response.created and ends with exactly one terminal
//     event, with nothing after it;
//   - sequence numbers increase by one from zero;
//   - an error event is followed immediately by response.failed;
//   - output items are added at consecutive indices, one open at a time,
//     and closed with output_item.done carrying the same id and type;
//   - content parts and reasoning summary parts are added at consecutive
//     indices on the open item, deltas and *.done events reference an open
//     part of the matching kind with the item's id, and parts are closed
//     before the item;
//   - function call argument events reference the open function call;
//   - the terminal response's output matches the items that were added.
//
// Extension events are ignored.
func Validate(events []openresponses.StreamEvent) error {
	if len(events) == 0 {
		return fmt.Errorf("no events")
	}
	if _, ok := events[0].(*openresponses.ResponseCreatedEvent); !ok {
		return fmt.Errorf("event 0: first event is %s, want response.created", events[0].EventType())
	}
	v := &validator{}
	for i, ev := range events {
		if err := v.step(i, ev); err != nil {
			return err
		}
	}
	if !v.terminal {
		return fmt.Errorf("stream ended without a terminal response event")
	}
	return nil
}

type validator struct {
	items     []itemState
	open      *itemState
	terminal  bool
	errorSeen bool
	lastSeq   int64
}

type itemState struct {
	index int
	item  openresponses.Item
	id    string
	typ   string
	done  bool

	parts     []partState // message content parts
	part      *partState  // open content part
	summaries []partState // reasoning summary parts
	summary   *partState  // open summary part
	textOpen  bool        // reasoning text delta seen
	textDone  bool
	argsDone  bool
}

type partState struct {
	typ      string
	done     bool
	kindDone bool // output_text.done, refusal.done or reasoning_summary_text.done seen
}

func (v *validator) step(i int, ev openresponses.StreamEvent) error {
	at := func(format string, args ...any) error {
		return fmt.Errorf("event %d (%s): %s", i, ev.EventType(), fmt.Sprintf(format, args...))
	}
	if v.terminal {
		return at("event after the terminal response event")
	}
	if seq := ev.Sequence(); seq != int64(i) {
		return at("sequence_number %d, want %d", seq, i)
	}
	if v.errorSeen {
		if _, ok := ev.(*openresponses.ResponseFailedEvent); !ok {
			return at("error event must be followed by response.failed")
		}
	}
	switch e := ev.(type) {
	case *openresponses.ResponseCreatedEvent, *openresponses.ResponseQueuedEvent, *openresponses.ResponseInProgressEvent:
		if i > 0 {
			if _, ok := e.(*openresponses.ResponseCreatedEvent); ok {
				return at("response.created must be the first event")
			}
		}
	case *openresponses.ResponseCompletedEvent:
		return v.finish(at, e.Response, openresponses.ResponseStatusCompleted)
	case *openresponses.ResponseIncompleteEvent:
		return v.finish(at, e.Response, openresponses.ResponseStatusIncomplete)
	case *openresponses.ResponseFailedEvent:
		v.terminal = true
		if e.Response == nil {
			return at("response is null")
		}
		if e.Response.Status != openresponses.ResponseStatusFailed {
			return at("response status %q, want failed", e.Response.Status)
		}
	case *openresponses.ErrorEvent:
		v.errorSeen = true
	case *openresponses.OutputItemAddedEvent:
		if e.Item == nil {
			return at("item is null")
		}
		if v.open != nil {
			return at("output_index %d added while item %d (%s) is still open", e.OutputIndex, v.open.index, v.open.id)
		}
		if e.OutputIndex != len(v.items) {
			return at("output_index %d, want %d", e.OutputIndex, len(v.items))
		}
		id, _ := itemIdentity(e.Item)
		v.items = append(v.items, itemState{index: e.OutputIndex, item: e.Item, id: id, typ: e.Item.ItemType()})
		v.open = &v.items[len(v.items)-1]
	case *openresponses.OutputItemDoneEvent:
		st, err := v.openItem(at, e.OutputIndex, "")
		if err != nil {
			return err
		}
		if e.Item == nil {
			return at("item is null")
		}
		if id, _ := itemIdentity(e.Item); id != st.id || e.Item.ItemType() != st.typ {
			return at("done item is %s %q, added item was %s %q", e.Item.ItemType(), id, st.typ, st.id)
		}
		if st.part != nil {
			return at("content part %d is still open", len(st.parts)-1)
		}
		if st.summary != nil {
			return at("summary part %d is still open", len(st.summaries)-1)
		}
		if _, ok := e.Item.(*openresponses.FunctionCall); ok && !st.argsDone && len(st.parts) == 0 {
			// Arguments streamed by delta must be finished with .done.
			if st.textOpen {
				return at("function_call_arguments.done missing")
			}
		}
		if st.textOpen && !st.textDone {
			return at("reasoning.done missing")
		}
		st.item = e.Item
		st.done = true
		v.open = nil
	case *openresponses.ContentPartAddedEvent:
		st, err := v.openItem(at, e.OutputIndex, e.ItemID)
		if err != nil {
			return err
		}
		if _, ok := st.item.(*openresponses.Message); !ok {
			return at("content part added to a %s item", st.typ)
		}
		if st.part != nil {
			return at("content part %d added while part %d is open", e.ContentIndex, len(st.parts)-1)
		}
		if e.ContentIndex != len(st.parts) {
			return at("content_index %d, want %d", e.ContentIndex, len(st.parts))
		}
		if e.Part == nil {
			return at("part is null")
		}
		st.parts = append(st.parts, partState{typ: e.Part.ContentType()})
		st.part = &st.parts[len(st.parts)-1]
	case *openresponses.ContentPartDoneEvent:
		st, err := v.openPart(at, e.OutputIndex, e.ItemID, e.ContentIndex, "")
		if err != nil {
			return err
		}
		if e.Part == nil || e.Part.ContentType() != st.part.typ {
			return at("done part type does not match the open %s part", st.part.typ)
		}
		if (st.part.typ == openresponses.ContentTypeOutputText || st.part.typ == openresponses.ContentTypeRefusal) && !st.part.kindDone {
			return at("%s.done missing before content_part.done", st.part.typ)
		}
		st.part.done = true
		st.part = nil
	case *openresponses.OutputTextDeltaEvent:
		if _, err := v.openPart(at, e.OutputIndex, e.ItemID, e.ContentIndex, openresponses.ContentTypeOutputText); err != nil {
			return err
		}
	case *openresponses.OutputTextDoneEvent:
		st, err := v.openPart(at, e.OutputIndex, e.ItemID, e.ContentIndex, openresponses.ContentTypeOutputText)
		if err != nil {
			return err
		}
		st.part.kindDone = true
	case *openresponses.OutputTextAnnotationAddedEvent:
		if _, err := v.openPart(at, e.OutputIndex, e.ItemID, e.ContentIndex, openresponses.ContentTypeOutputText); err != nil {
			return err
		}
	case *openresponses.RefusalDeltaEvent:
		if _, err := v.openPart(at, e.OutputIndex, e.ItemID, e.ContentIndex, openresponses.ContentTypeRefusal); err != nil {
			return err
		}
	case *openresponses.RefusalDoneEvent:
		st, err := v.openPart(at, e.OutputIndex, e.ItemID, e.ContentIndex, openresponses.ContentTypeRefusal)
		if err != nil {
			return err
		}
		st.part.kindDone = true
	case *openresponses.FunctionCallArgumentsDeltaEvent:
		st, err := v.openTyped(at, e.OutputIndex, e.ItemID, openresponses.ItemTypeFunctionCall)
		if err != nil {
			return err
		}
		if st.argsDone {
			return at("arguments delta after function_call_arguments.done")
		}
		st.textOpen = true
	case *openresponses.FunctionCallArgumentsDoneEvent:
		st, err := v.openTyped(at, e.OutputIndex, e.ItemID, openresponses.ItemTypeFunctionCall)
		if err != nil {
			return err
		}
		if st.argsDone {
			return at("duplicate function_call_arguments.done")
		}
		st.argsDone = true
		st.textOpen = false
	case *openresponses.ReasoningSummaryPartAddedEvent:
		st, err := v.openTyped(at, e.OutputIndex, e.ItemID, openresponses.ItemTypeReasoning)
		if err != nil {
			return err
		}
		if st.summary != nil {
			return at("summary part %d added while part %d is open", e.SummaryIndex, len(st.summaries)-1)
		}
		if e.SummaryIndex != len(st.summaries) {
			return at("summary_index %d, want %d", e.SummaryIndex, len(st.summaries))
		}
		if e.Part == nil {
			return at("part is null")
		}
		st.summaries = append(st.summaries, partState{typ: e.Part.ContentType()})
		st.summary = &st.summaries[len(st.summaries)-1]
	case *openresponses.ReasoningSummaryTextDeltaEvent:
		if _, err := v.openSummary(at, e.OutputIndex, e.ItemID, e.SummaryIndex); err != nil {
			return err
		}
	case *openresponses.ReasoningSummaryTextDoneEvent:
		st, err := v.openSummary(at, e.OutputIndex, e.ItemID, e.SummaryIndex)
		if err != nil {
			return err
		}
		st.summary.kindDone = true
	case *openresponses.ReasoningSummaryPartDoneEvent:
		st, err := v.openSummary(at, e.OutputIndex, e.ItemID, e.SummaryIndex)
		if err != nil {
			return err
		}
		if !st.summary.kindDone {
			return at("reasoning_summary_text.done missing before reasoning_summary_part.done")
		}
		st.summary.done = true
		st.summary = nil
	case *openresponses.ReasoningDeltaEvent:
		st, err := v.openTyped(at, e.OutputIndex, e.ItemID, openresponses.ItemTypeReasoning)
		if err != nil {
			return err
		}
		if st.textDone {
			return at("reasoning delta after reasoning.done")
		}
		st.textOpen = true
	case *openresponses.ReasoningDoneEvent:
		st, err := v.openTyped(at, e.OutputIndex, e.ItemID, openresponses.ItemTypeReasoning)
		if err != nil {
			return err
		}
		st.textDone = true
	}
	return nil
}

func (v *validator) finish(at func(string, ...any) error, resp *openresponses.Response, want openresponses.ResponseStatus) error {
	v.terminal = true
	if resp == nil {
		return at("response is null")
	}
	if resp.Status != want {
		return at("response status %q, want %s", resp.Status, want)
	}
	if v.open != nil {
		return at("item %d (%s) is still open", v.open.index, v.open.id)
	}
	if len(resp.Output) != len(v.items) {
		return at("response has %d output items, %d were streamed", len(resp.Output), len(v.items))
	}
	for i, st := range v.items {
		id, _ := itemIdentity(resp.Output[i])
		if resp.Output[i].ItemType() != st.typ || id != st.id {
			return at("output[%d] is %s %q, streamed item was %s %q", i, resp.Output[i].ItemType(), id, st.typ, st.id)
		}
	}
	return nil
}

// openItem returns the open item at index, checking its id when itemID
// is non-empty.
func (v *validator) openItem(at func(string, ...any) error, index int, itemID string) (*itemState, error) {
	if v.open == nil {
		return nil, at("no open item (output_index %d)", index)
	}
	if v.open.index != index {
		return nil, at("output_index %d, open item is %d", index, v.open.index)
	}
	if itemID != "" && itemID != v.open.id {
		return nil, at("item_id %q, open item is %q", itemID, v.open.id)
	}
	return v.open, nil
}

func (v *validator) openTyped(at func(string, ...any) error, index int, itemID, typ string) (*itemState, error) {
	st, err := v.openItem(at, index, itemID)
	if err != nil {
		return nil, err
	}
	if st.typ != typ {
		return nil, at("open item is a %s, want %s", st.typ, typ)
	}
	return st, nil
}

func (v *validator) openPart(at func(string, ...any) error, index int, itemID string, contentIndex int, typ string) (*itemState, error) {
	st, err := v.openTyped(at, index, itemID, openresponses.ItemTypeMessage)
	if err != nil {
		return nil, err
	}
	if st.part == nil {
		return nil, at("no open content part (content_index %d)", contentIndex)
	}
	if contentIndex != len(st.parts)-1 {
		return nil, at("content_index %d, open part is %d", contentIndex, len(st.parts)-1)
	}
	if typ != "" && st.part.typ != typ {
		return nil, at("open part is %s, want %s", st.part.typ, typ)
	}
	return st, nil
}

func (v *validator) openSummary(at func(string, ...any) error, index int, itemID string, summaryIndex int) (*itemState, error) {
	st, err := v.openTyped(at, index, itemID, openresponses.ItemTypeReasoning)
	if err != nil {
		return nil, err
	}
	if st.summary == nil {
		return nil, at("no open summary part (summary_index %d)", summaryIndex)
	}
	if summaryIndex != len(st.summaries)-1 {
		return nil, at("summary_index %d, open part is %d", summaryIndex, len(st.summaries)-1)
	}
	return st, nil
}

// itemIdentity returns the id and status of any item type.
func itemIdentity(item openresponses.Item) (string, openresponses.Status) {
	switch v := item.(type) {
	case *openresponses.Message:
		return v.ID, v.Status
	case *openresponses.FunctionCall:
		return v.ID, v.Status
	case *openresponses.FunctionCallOutput:
		return v.ID, v.Status
	case *openresponses.ReasoningItem:
		return v.ID, v.Status
	case *openresponses.Compaction:
		return v.ID, v.Status
	case *openresponses.ItemReference:
		return v.ID, ""
	case *openresponses.UnknownItem:
		return v.ID, v.Status
	}
	return "", ""
}
