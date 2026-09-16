package openresponses

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"iter"
	"sync"
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
	scanner *sseScanner
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

	for {
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

// Accumulator folds a sequence of streaming events into a Response. The
// zero value is ready to use. It is not safe for concurrent use.
type Accumulator struct {
	resp *Response
	// lastError remembers an error event so a stream that fails without
	// a response.failed still surfaces the reason.
	lastError *ErrorPayload
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
		a.setItem(e.OutputIndex, cloneItem(e.Item))
	case *OutputItemDoneEvent:
		a.setItem(e.OutputIndex, e.Item)
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
	if len(cp.Output) == 0 && a.resp != nil && len(a.resp.Output) > 0 && !cp.Status.Terminal() {
		cp.Output = a.resp.Output
	}
	a.resp = cp
}

func (a *Accumulator) ensure() *Response {
	if a.resp == nil {
		a.resp = &Response{Status: ResponseStatusInProgress}
	}
	return a.resp
}

func (a *Accumulator) setItem(idx int, item Item) {
	if item == nil {
		return
	}
	r := a.ensure()
	for len(r.Output) <= idx {
		r.Output = append(r.Output, nil)
	}
	r.Output[idx] = item
}

func (a *Accumulator) item(idx int) Item {
	if a.resp == nil || idx < 0 || idx >= len(a.resp.Output) {
		return nil
	}
	return a.resp.Output[idx]
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
