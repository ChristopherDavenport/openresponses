package openresponses

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"sync"

	"github.com/ChristopherDavenport/openresponses/internal/sse"
)

// EventStream reads decoded events from a Server-Sent Events body.
//
//	stream, err := client.CreateStream(ctx, req)
//	...
//	defer stream.Close()
//	for ev := range stream.Events() {
//		if d, ok := ev.(*openresponses.OutputTextDeltaEvent); ok {
//			fmt.Print(d.Delta)
//		}
//	}
//	if err := stream.Err(); err != nil { ... }
//	final := stream.Response()
type EventStream struct {
	body    io.ReadCloser
	scanner *sse.Scanner
	acc     Accumulator
	stop    func() bool

	mu       sync.Mutex
	cur      StreamEvent
	err      error
	done     bool
	closed   bool
	terminal bool
}

// NewEventStream wraps an SSE body. The caller owns body until Close is
// called. When ctx is cancelled the body is closed, which unblocks Next.
func NewEventStream(ctx context.Context, body io.ReadCloser) *EventStream {
	s := &EventStream{body: body, scanner: newSSEScanner(body)}
	if ctx != nil && ctx.Done() != nil {
		s.stop = context.AfterFunc(ctx, func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if !s.done {
				s.err = ctx.Err()
			}
			_ = s.closeLocked() // the read loop reports the close as EOF
		})
	}
	return s
}

// Next advances to the next event. It returns false at the end of the
// stream or on error; check Err afterwards.
func (s *EventStream) Next() bool {
	s.mu.Lock()
	if s.done || s.closed {
		s.mu.Unlock()
		return false
	}
	s.mu.Unlock()

	frame, ok := s.scanner.Next()
	if !ok {
		s.finish(s.scanner.Err(), false)
		return false
	}
	if frame.Data == sseDone {
		s.finish(nil, true)
		return false
	}
	ev, err := DecodeEvent([]byte(frame.Data))
	if err != nil {
		s.finish(fmt.Errorf("decode SSE frame: %w", err), false)
		return false
	}
	s.mu.Lock()
	s.cur = ev
	s.acc.Add(ev)
	if _, isTerminal := TerminalResponse(ev); isTerminal {
		s.terminal = true
	}
	s.mu.Unlock()
	return true
}

// finish records the end of the stream. An EOF without [DONE] and without
// a terminal event is reported as ErrTruncatedStream.
func (s *EventStream) finish(err error, sawDone bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done {
		return
	}
	s.done = true
	s.stopWatching()
	switch {
	case s.err != nil:
		// A cancellation error recorded by the context hook wins.
	case err != nil:
		s.err = err
	case !sawDone && !s.terminal:
		s.err = ErrTruncatedStream
	}
}

// Event returns the current event.
func (s *EventStream) Event() StreamEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur
}

// Err returns the first error encountered, if any. A stream that ended
// without [DONE] or a terminal event returns [ErrTruncatedStream].
func (s *EventStream) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// Response returns the response accumulated so far. After a terminal
// event it is the final response.
func (s *EventStream) Response() *Response {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.acc.Response()
}

// ReusedIndexes reports the output indexes the stream has reused so far;
// see [Accumulator.ReusedIndexes]. Once it is non-empty, a position in
// Response().Output is not an output_index until a terminal snapshot
// replaces Output with the server's own list. [Accumulator.Position] on
// the underlying accumulator is not exposed here; a consumer that needs
// the mapping folds the events with its own [Accumulator].
func (s *EventStream) ReusedIndexes() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.acc.ReusedIndexes()
}

// Events returns an iterator over the remaining events. Check Err after
// the loop.
func (s *EventStream) Events() iter.Seq[StreamEvent] {
	return func(yield func(StreamEvent) bool) {
		for s.Next() {
			if !yield(s.Event()) {
				return
			}
		}
	}
}

// Close releases the underlying body. It is safe to call more than once.
func (s *EventStream) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeLocked()
}

func (s *EventStream) closeLocked() error {
	if s.closed {
		return nil
	}
	s.closed = true
	s.stopWatching()
	return s.body.Close()
}

// stopWatching detaches the context hook once the stream no longer needs
// it.
func (s *EventStream) stopWatching() {
	if s.stop != nil {
		s.stop()
	}
}

// Wait drains the stream and returns the final response. If the stream
// ended with an error event, the returned error is the corresponding
// *Error and the failed response is still returned when available.
func (s *EventStream) Wait() (*Response, error) {
	for s.Next() {
	}
	resp := s.Response()
	if err := s.Err(); err != nil {
		return resp, err
	}
	if resp != nil && resp.Error != nil {
		return resp, &Error{Type: resp.Error.Type, Code: resp.Error.Code, Message: resp.Error.Message, Param: resp.Error.Param}
	}
	return resp, nil
}

// Accumulator folds a sequence of streaming events into a Response,
// following the order described under "Streaming lifecycle" in the
// package documentation and tolerating streams that omit the terminal
// snapshot. The zero value is ready to use. It is not safe for
// concurrent use.
//
// It also tolerates a stream that reuses an output_index. The lifecycle
// gives every item its own index, and streamtest.Validate rejects a
// stream that does otherwise unless given streamtest.WithOutputIndexReuse,
// but some servers number differently:
// Ollama 0.23 streams every parallel function call at output_index 0,
// each opened and closed in turn. Writing each into Output[0] would keep
// only the last call until the terminal snapshot, and for good if the
// stream is cut before it. So an output_item.added at an index whose
// item has already been closed by output_item.done starts a new item,
// unless it carries that item's own id, appended after everything
// accumulated so far, and the events that follow at that index reach the
// new item. From then on every index the stream has not used before is
// appended too, so no item lands on another, and a snapshot that arrives
// before the terminal one no longer replaces the accumulated list, since
// its positions and the indexes of the events still to come would not
// agree; an item such a snapshot alone names is appended, by id.
// [Accumulator.ReusedIndexes] reports each such reuse, since a
// position in Output is then no longer an output_index, and
// [Accumulator.Position] and [Accumulator.ItemAt] map an output_index to
// the item its events currently name. The terminal snapshot still
// replaces Output with the server's own list, as for any stream.
type Accumulator struct {
	resp *Response
	// lastError remembers an error event so a stream that fails without
	// a response.failed still surfaces the reason.
	lastError *ErrorPayload
	// slots maps each output_index the stream has used to the position
	// in resp.Output of the item most recently added at it. For a
	// conforming stream every entry maps an index to itself.
	slots map[int]int
	// closed marks the positions whose item has had its output_item.done,
	// or that a snapshot listed with a terminal status.
	closed map[int]bool
	// reused records every output_index reused, in stream order.
	reused []int
}

// Response returns the accumulated response, or nil before any
// response.* event.
func (a *Accumulator) Response() *Response {
	if a.resp == nil && a.lastError != nil {
		return &Response{Status: ResponseStatusFailed, Error: a.lastError}
	}
	return a.resp
}

// Add applies one event. Unknown events are ignored. Items and parts
// carried by *.added events are copied, so the accumulator never mutates
// the producer's objects when it applies later deltas.
func (a *Accumulator) Add(ev StreamEvent) {
	switch e := ev.(type) {
	case *ResponseCreatedEvent:
		a.setResponse(e.Response)
	case *ResponseQueuedEvent:
		a.setResponse(e.Response)
	case *ResponseInProgressEvent:
		a.setResponse(e.Response)
	case *ResponseCompletedEvent:
		a.setResponse(e.Response)
	case *ResponseFailedEvent:
		a.setResponse(e.Response)
	case *ResponseIncompleteEvent:
		a.setResponse(e.Response)
	case *OutputItemAddedEvent:
		a.addItem(e.OutputIndex, cloneItem(e.Item))
	case *OutputItemDoneEvent:
		a.closeItem(e.OutputIndex, e.Item)
	case *ContentPartAddedEvent:
		a.setPart(e.OutputIndex, e.ContentIndex, cloneContent(e.Part))
	case *ContentPartDoneEvent:
		a.setPart(e.OutputIndex, e.ContentIndex, e.Part)
	case *OutputTextDeltaEvent:
		if p, ok := a.part(e.OutputIndex, e.ContentIndex).(*OutputText); ok {
			p.Text += e.Delta
			p.Logprobs = append(p.Logprobs, e.Logprobs...)
		}
	case *OutputTextDoneEvent:
		if p, ok := a.part(e.OutputIndex, e.ContentIndex).(*OutputText); ok {
			p.Text = e.Text
			if e.Logprobs != nil {
				p.Logprobs = e.Logprobs
			}
		}
	case *RefusalDeltaEvent:
		if p, ok := a.part(e.OutputIndex, e.ContentIndex).(*Refusal); ok {
			p.Refusal += e.Delta
		}
	case *RefusalDoneEvent:
		if p, ok := a.part(e.OutputIndex, e.ContentIndex).(*Refusal); ok {
			p.Refusal = e.Refusal
		}
	case *FunctionCallArgumentsDeltaEvent:
		if fc, ok := a.item(e.OutputIndex).(*FunctionCall); ok {
			fc.Arguments += e.Delta
		}
	case *FunctionCallArgumentsDoneEvent:
		if fc, ok := a.item(e.OutputIndex).(*FunctionCall); ok {
			fc.Arguments = e.Arguments
		}
	case *ReasoningSummaryPartAddedEvent:
		a.setSummaryPart(e.OutputIndex, e.SummaryIndex, cloneContent(e.Part))
	case *ReasoningSummaryPartDoneEvent:
		a.setSummaryPart(e.OutputIndex, e.SummaryIndex, e.Part)
	case *ReasoningSummaryTextDeltaEvent:
		if p, ok := a.summaryPart(e.OutputIndex, e.SummaryIndex).(*SummaryText); ok {
			p.Text += e.Delta
		}
	case *ReasoningSummaryTextDoneEvent:
		if p, ok := a.summaryPart(e.OutputIndex, e.SummaryIndex).(*SummaryText); ok {
			p.Text = e.Text
		}
	case *ReasoningDeltaEvent:
		if p, ok := a.reasoningPart(e.OutputIndex, e.ContentIndex).(*ReasoningText); ok {
			p.Text += e.Delta
		}
	case *ReasoningDoneEvent:
		if p, ok := a.reasoningPart(e.OutputIndex, e.ContentIndex).(*ReasoningText); ok {
			p.Text = e.Text
		}
	case *OutputTextAnnotationAddedEvent:
		if p, ok := a.part(e.OutputIndex, e.ContentIndex).(*OutputText); ok && e.Annotation != nil {
			for len(p.Annotations) <= e.AnnotationIndex {
				p.Annotations = append(p.Annotations, nil)
			}
			p.Annotations[e.AnnotationIndex] = e.Annotation
		}
	case *ErrorEvent:
		payload := e.Error
		a.lastError = &payload
		if a.resp != nil {
			a.resp.Error = &payload
			a.resp.Status = ResponseStatusFailed
		}
	}
}

func (a *Accumulator) setResponse(r *Response) {
	if r == nil {
		return
	}
	// Terminal and lifecycle events carry the authoritative snapshot, but
	// keep locally accumulated output when the snapshot has none, which
	// happens with servers that stream deltas and send an empty
	// in_progress payload.
	cp := r.Clone()
	switch {
	case len(cp.Output) == 0 && a.resp != nil && len(a.resp.Output) > 0 && !cp.Status.Terminal():
		cp.Output = a.resp.Output
	case len(a.reused) > 0 && !cp.Status.Terminal():
		// Positions have diverged from indexes. The snapshot's list is
		// positional by the server's reckoning while the events still to
		// come name indexes, so taking the list would point an index at
		// the wrong item; the accumulated list, and its map, stay. An
		// item the snapshot alone names, by an id the list does not
		// hold, is still kept, appended with no index of its own.
		cp.Output = a.appendUnknown(r.Output)
	default:
		a.relist(cp.Output)
	}
	a.resp = cp
}

// appendUnknown returns the accumulated output with every item of a
// snapshot's list appended whose id is not yet in it. Items without an id
// cannot be told from those already held and are left out.
func (a *Accumulator) appendUnknown(listed Items) Items {
	out := a.resp.Output
	if len(listed) == 0 {
		return out
	}
	held := make(map[string]bool, len(out))
	for pos := range out {
		if id := itemID(out, pos); id != "" {
			held[id] = true
		}
	}
	for _, item := range listed {
		if item == nil {
			continue
		}
		if id, _ := itemIdentity(item); id != "" && !held[id] {
			held[id] = true
			out = append(out, cloneItem(item))
		}
	}
	return out
}

// relist rebuilds the index bookkeeping around a snapshot's output list,
// in which positions are indexes. An item stays closed when it was
// closed before the snapshot, known by its id, or when the snapshot
// gives it a terminal status; a later output_item.added at its index is
// then a reuse, not a re-announcement.
func (a *Accumulator) relist(out Items) {
	var wasClosed map[string]bool
	if a.resp != nil && len(a.closed) > 0 {
		wasClosed = make(map[string]bool, len(a.closed))
		for pos := range a.closed {
			if id := itemID(a.resp.Output, pos); id != "" {
				wasClosed[id] = true
			}
		}
	}
	a.slots, a.closed = nil, nil
	for pos, item := range out {
		if item == nil {
			continue
		}
		a.setSlot(pos, pos)
		id, status := itemIdentity(item)
		if (id != "" && wasClosed[id]) || status == StatusCompleted || status == StatusIncomplete {
			a.setClosed(pos)
		}
	}
}

func (a *Accumulator) ensure() *Response {
	if a.resp == nil {
		a.resp = &Response{Status: ResponseStatusInProgress}
	}
	return a.resp
}

// ReusedIndexes reports the output_index of every output_item.added, or
// output_item.done naming another item, that arrived at an index whose
// item was already closed, by output_item.done or by the terminal status
// a snapshot gave it, in stream order, one entry per reuse. It is nil for a stream that
// follows the lifecycle. The items are all in Response().Output, each
// later one appended in the order it was added, so once this is non-empty
// a position in Output is not an output_index; use [Accumulator.Position]
// to find the item an index names. The terminal snapshot replaces Output
// with the server's own list; the record of the reuse stays.
func (a *Accumulator) ReusedIndexes() []int {
	return append([]int(nil), a.reused...)
}

// Position returns the position in Response().Output of the item the
// stream's events at outputIndex currently name: the item the last
// output_item.added, or output_item.done, placed at that index, which is
// the one a delta at the index reaches. Either event replaces the item in
// place while it is still open, or when it names the item already there;
// one that names another item at a closed index appends it and moves the
// index to the new position. The second result is false when there is no such item:
// a negative index, an index the stream never opened once an index has
// been reused, or an index whose item is no longer in Output. Before any
// reuse an index that has no item of its own yet maps to its own
// position, when Output has an item there.
//
// The answer is valid straight after Add of the event that names the
// index: after an output_item.added or output_item.done at it, it is that
// event's item, including one appended because the index was reused. A
// read assigns nothing and changes no state.
//
// For a stream that follows the lifecycle Position(i) is (i, true) for
// every index opened. It differs once the stream reuses an index, see
// [Accumulator.ReusedIndexes]: an item added at a closed index is
// appended, so Position then reports where, and Response().Output[i] is
// not the item an event at i names.
//
// Position reads the accumulator as it stands, so ask after applying the
// event whose item is wanted, and ask again after the next
// output_item.added or output_item.done that names another item at the
// index, which moves it to the new item. Items
// positions hold are stable across later events, but a snapshot changes
// what is held:
//
//   - A terminal snapshot replaces Output with the server's own list, in
//     which positions are output indexes, so Position(i) is (i, true) for
//     each item that list holds and false for any other index, including
//     an index the stream opened at a position beyond the end of the list.
//     The mapping a reuse had built is gone with it, since no event
//     follows a terminal one. Read the item before the terminal event
//     when the mapping is needed.
//   - A snapshot before the terminal one, taken while no index has been
//     reused, is read the same way: its list replaces Output and its
//     positions are indexes. One with no output keeps what is held.
//   - A snapshot before the terminal one, taken after an index has been
//     reused, changes no mapping: the held list and the positions of the
//     indexes in it stay, and an item the snapshot alone names is
//     appended with no index of its own, so no outputIndex reaches it.
func (a *Accumulator) Position(outputIndex int) (int, bool) {
	if a.resp == nil || outputIndex < 0 {
		return 0, false
	}
	pos, ok := a.slots[outputIndex]
	if !ok {
		if len(a.reused) > 0 {
			return 0, false
		}
		pos = outputIndex
	}
	if pos >= len(a.resp.Output) || a.resp.Output[pos] == nil {
		return 0, false
	}
	return pos, true
}

// ItemAt returns the item the stream's events at outputIndex currently
// name, which is Response().Output[p] for the p that
// [Accumulator.Position] reports, or false when Position does. The item
// is the accumulator's own copy, not a snapshot: later events at the
// index change it, as they change the response.
func (a *Accumulator) ItemAt(outputIndex int) (Item, bool) {
	pos, ok := a.Position(outputIndex)
	if !ok {
		return nil, false
	}
	return a.resp.Output[pos], true
}

// slot returns the position of the item at idx, assigning one to an
// index the stream has not used before: its own, until a reuse has moved
// positions away from indexes, and the end of Output after that, so an
// index that first appears then cannot land on an appended item.
func (a *Accumulator) slot(idx int) int {
	if pos, ok := a.slots[idx]; ok {
		return pos
	}
	pos := idx
	if len(a.reused) > 0 {
		pos = len(a.ensure().Output)
	}
	a.setSlot(idx, pos)
	return pos
}

func (a *Accumulator) setSlot(idx, pos int) {
	if a.slots == nil {
		a.slots = make(map[int]int)
	}
	a.slots[idx] = pos
}

func (a *Accumulator) setClosed(pos int) {
	if a.closed == nil {
		a.closed = make(map[int]bool)
	}
	a.closed[pos] = true
}

// target is the position an item announced at idx goes to. The item
// already there is replaced while it is still open, or when the new item
// carries its id: both re-announce the same item. A closed item has been
// finished by the server, so an item that does not name it is another
// item, appended, and the reuse recorded. same says whether an item
// without an id counts as the closed one.
func (a *Accumulator) target(idx int, item Item, same bool) int {
	r := a.ensure()
	pos := a.slot(idx)
	if pos < len(r.Output) && r.Output[pos] != nil && a.closed[pos] {
		id, _ := itemIdentity(item)
		another := id != itemID(r.Output, pos)
		if id == "" {
			another = !same
		}
		if another {
			pos = len(r.Output)
			a.setSlot(idx, pos)
			a.reused = append(a.reused, idx)
		}
	}
	return pos
}

// addItem places an item opened at idx. An added names a new item unless
// it carries the id of the one already there.
func (a *Accumulator) addItem(idx int, item Item) {
	if item == nil || idx < 0 {
		return
	}
	pos := a.target(idx, item, false)
	a.place(pos, item)
	delete(a.closed, pos)
}

// closeItem records the final form of the item at idx and marks it
// closed, so a later output_item.added at idx starts a new item. A done
// without an id at a closed position restates that item; one with
// another id is another item.
func (a *Accumulator) closeItem(idx int, item Item) {
	if item == nil || idx < 0 {
		return
	}
	pos := a.target(idx, item, true)
	a.place(pos, item)
	a.setClosed(pos)
}

func (a *Accumulator) place(pos int, item Item) {
	r := a.ensure()
	for len(r.Output) <= pos {
		r.Output = append(r.Output, nil)
	}
	r.Output[pos] = item
}

// item is the item events at idx refer to, or nil when the stream has
// not opened one there.
func (a *Accumulator) item(idx int) Item {
	item, _ := a.ItemAt(idx)
	return item
}

// itemID is the id of the item at pos in out, or "" when there is none.
func itemID(out Items, pos int) string {
	if pos < 0 || pos >= len(out) || out[pos] == nil {
		return ""
	}
	id, _ := itemIdentity(out[pos])
	return id
}

// itemIdentity returns the id and status of any item type.
func itemIdentity(item Item) (string, Status) {
	switch v := item.(type) {
	case *Message:
		return v.ID, v.Status
	case *FunctionCall:
		return v.ID, v.Status
	case *FunctionCallOutput:
		return v.ID, v.Status
	case *ReasoningItem:
		return v.ID, v.Status
	case *Compaction:
		return v.ID, v.Status
	case *ItemReference:
		return v.ID, ""
	case *UnknownItem:
		return v.ID, v.Status
	}
	return "", ""
}

func (a *Accumulator) setPart(idx, cidx int, part Content) {
	m, ok := a.item(idx).(*Message)
	if !ok || part == nil {
		return
	}
	for len(m.Content) <= cidx {
		m.Content = append(m.Content, nil)
	}
	m.Content[cidx] = part
}

func (a *Accumulator) part(idx, cidx int) Content {
	m, ok := a.item(idx).(*Message)
	if !ok || cidx < 0 || cidx >= len(m.Content) {
		return nil
	}
	return m.Content[cidx]
}

func (a *Accumulator) setSummaryPart(idx, sidx int, part Content) {
	r, ok := a.item(idx).(*ReasoningItem)
	if !ok || part == nil {
		return
	}
	for len(r.Summary) <= sidx {
		r.Summary = append(r.Summary, nil)
	}
	r.Summary[sidx] = part
}

func (a *Accumulator) summaryPart(idx, sidx int) Content {
	r, ok := a.item(idx).(*ReasoningItem)
	if !ok || sidx < 0 || sidx >= len(r.Summary) {
		return nil
	}
	return r.Summary[sidx]
}

func (a *Accumulator) reasoningPart(idx, cidx int) Content {
	r, ok := a.item(idx).(*ReasoningItem)
	if !ok {
		return nil
	}
	for len(r.Content) <= cidx {
		r.Content = append(r.Content, &ReasoningText{})
	}
	return r.Content[cidx]
}

// cloneItem deep-copies an item through its wire form. On a decode
// failure the original is returned; the item was produced by this
// package's own types so that does not happen in practice.
func cloneItem(item Item) Item {
	if item == nil {
		return nil
	}
	data, err := json.Marshal(item)
	if err != nil {
		return item
	}
	cp, err := UnmarshalItem(data)
	if err != nil {
		return item
	}
	return cp
}

func cloneContent(part Content) Content {
	if part == nil {
		return nil
	}
	data, err := json.Marshal(part)
	if err != nil {
		return part
	}
	cp, err := UnmarshalContent(data)
	if err != nil {
		return part
	}
	return cp
}
