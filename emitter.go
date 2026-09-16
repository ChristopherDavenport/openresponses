package openresponses

import "fmt"

// Emitter drives the streaming lifecycle of one response on behalf of an
// adapter, so adapters only supply content. It owns the bookend events
// (response.created, output_item.added/done, content_part.added/done, the
// *.done events), output and content indices, item IDs and the response
// snapshot, and it closes an open item before opening the next.
//
//	em := openresponses.NewEmitter(sink, openresponses.NewResponse(req))
//	msg, err := em.Message(openresponses.PhaseFinalAnswer)
//	...
//	err = msg.Text("Hello")
//	...
//	em.Response().Usage = &usage
//	return em.Complete()
//
// To fail, return an error from CreateStream; the handler emits the error
// event and response.failed with the transport's semantics.
//
// Emitter is not safe for concurrent use.
type Emitter struct {
	sink     EventSink
	resp     *Response
	started  bool
	terminal bool
	open     itemCloser
}

// itemCloser is implemented by the item writers so the emitter can close
// whichever one is open.
type itemCloser interface {
	Close() error
}

// NewEmitter returns an emitter that streams resp through sink. resp is
// usually [NewResponse] with an ID set; when the ID is empty one is
// generated. Nothing is sent until the first item or Complete.
func NewEmitter(sink EventSink, resp *Response) *Emitter {
	if resp.ID == "" {
		resp.ID = NewID("resp")
	}
	if resp.Status == "" {
		resp.Status = ResponseStatusInProgress
	}
	return &Emitter{sink: sink, resp: resp}
}

// Response returns the live response snapshot. Adapters set Usage and
// any other final fields on it before calling Complete or Incomplete.
func (e *Emitter) Response() *Response { return e.resp }

// Start sends response.created and response.in_progress. It is called
// implicitly by the first item or terminal event.
func (e *Emitter) Start() error {
	if e.started {
		return nil
	}
	e.started = true
	if err := e.sink.Send(&ResponseCreatedEvent{Response: e.resp}); err != nil {
		return err
	}
	return e.sink.Send(&ResponseInProgressEvent{Response: e.resp})
}

// Message opens an assistant message and returns a writer for its
// content. phase may be empty.
func (e *Emitter) Message(phase Phase) (*MessageWriter, error) {
	msg := &Message{ID: NewID("msg"), Status: StatusInProgress, Role: RoleAssistant, Phase: phase, Content: Contents{}}
	idx, err := e.openItem(msg)
	if err != nil {
		return nil, err
	}
	w := &MessageWriter{e: e, index: idx, msg: msg}
	e.open = w
	return w, nil
}

// FunctionCall opens a function call item. callID may be empty, in which
// case one is generated.
func (e *Emitter) FunctionCall(callID, name string) (*FunctionCallWriter, error) {
	if callID == "" {
		callID = NewID("call")
	}
	call := &FunctionCall{ID: NewID("fc"), Status: StatusInProgress, CallID: callID, Name: name}
	idx, err := e.openItem(call)
	if err != nil {
		return nil, err
	}
	w := &FunctionCallWriter{e: e, index: idx, call: call}
	e.open = w
	return w, nil
}

// Reasoning opens a reasoning item.
func (e *Emitter) Reasoning() (*ReasoningWriter, error) {
	item := &ReasoningItem{ID: NewID("rs"), Status: StatusInProgress, Summary: Contents{}}
	idx, err := e.openItem(item)
	if err != nil {
		return nil, err
	}
	w := &ReasoningWriter{e: e, index: idx, item: item}
	e.open = w
	return w, nil
}

// Item emits a complete item with no deltas: output_item.added followed
// by output_item.done. Use it for tool outputs, compaction items and
// extension items. An empty status is set to completed.
func (e *Emitter) Item(item Item) error {
	idx, err := e.openItem(item)
	if err != nil {
		return err
	}
	return e.closeItem(idx, item)
}

// Complete closes any open item and sends response.completed.
func (e *Emitter) Complete() error {
	if err := e.beforeTerminal(); err != nil {
		return err
	}
	e.resp.Complete()
	e.terminal = true
	return e.sink.Send(&ResponseCompletedEvent{Response: e.resp})
}

// Incomplete closes any open item, marks it incomplete, and sends
// response.incomplete with the given reason.
func (e *Emitter) Incomplete(reason IncompleteReason) error {
	if e.open != nil {
		if w, ok := e.open.(interface{ markIncomplete() }); ok {
			w.markIncomplete()
		}
	}
	if err := e.beforeTerminal(); err != nil {
		return err
	}
	e.resp.Incomplete(reason)
	e.terminal = true
	return e.sink.Send(&ResponseIncompleteEvent{Response: e.resp})
}

func (e *Emitter) beforeTerminal() error {
	if e.terminal {
		return ErrTerminalEventSent
	}
	if err := e.Start(); err != nil {
		return err
	}
	return e.closeOpen()
}

// openItem closes the open item, appends item to the output and sends
// output_item.added. It returns the item's output index.
func (e *Emitter) openItem(item Item) (int, error) {
	if e.terminal {
		return 0, ErrTerminalEventSent
	}
	if err := e.Start(); err != nil {
		return 0, err
	}
	if err := e.closeOpen(); err != nil {
		return 0, err
	}
	idx := len(e.resp.Output)
	e.resp.Output = append(e.resp.Output, item)
	return idx, e.sink.Send(&OutputItemAddedEvent{OutputIndex: idx, Item: item})
}

func (e *Emitter) closeOpen() error {
	if e.open == nil {
		return nil
	}
	w := e.open
	e.open = nil
	return w.Close()
}

// closeItem sends output_item.done, promoting an in_progress or unset
// status to completed.
func (e *Emitter) closeItem(idx int, item Item) error {
	finish := func(st *Status) {
		if *st == "" || *st == StatusInProgress {
			*st = StatusCompleted
		}
	}
	switch v := item.(type) {
	case *Message:
		finish(&v.Status)
	case *FunctionCall:
		finish(&v.Status)
	case *FunctionCallOutput:
		finish(&v.Status)
	case *ReasoningItem:
		finish(&v.Status)
	case *Compaction:
		finish(&v.Status)
	case *UnknownItem:
		finish(&v.Status)
	}
	return e.sink.Send(&OutputItemDoneEvent{OutputIndex: idx, Item: item})
}

// MessageWriter streams the content of an assistant message. Text and
// Refusal open a content part of the matching kind on first use; calling
// the other kind closes the current part and opens a new one.
type MessageWriter struct {
	e      *Emitter
	index  int
	msg    *Message
	part   Content // open part, nil when none
	closed bool
}

// Item returns the message being written.
func (w *MessageWriter) Item() *Message { return w.msg }

// Text appends delta to the open output_text part, opening one if
// needed.
func (w *MessageWriter) Text(delta string) error {
	part, err := w.textPart()
	if err != nil {
		return err
	}
	part.Text += delta
	return w.e.sink.Send(&OutputTextDeltaEvent{ItemID: w.msg.ID, OutputIndex: w.index, ContentIndex: w.partIndex(), Delta: delta})
}

// Annotation adds an annotation to the open output_text part, opening
// one if needed.
func (w *MessageWriter) Annotation(a Annotation) error {
	part, err := w.textPart()
	if err != nil {
		return err
	}
	part.Annotations = append(part.Annotations, a)
	return w.e.sink.Send(&OutputTextAnnotationAddedEvent{
		ItemID: w.msg.ID, OutputIndex: w.index, ContentIndex: w.partIndex(),
		AnnotationIndex: len(part.Annotations) - 1, Annotation: a,
	})
}

// Refusal appends delta to the open refusal part, opening one if needed.
func (w *MessageWriter) Refusal(delta string) error {
	if w.closed {
		return errWriterClosed
	}
	part, ok := w.part.(*Refusal)
	if !ok {
		if err := w.closePart(); err != nil {
			return err
		}
		part = &Refusal{}
		if err := w.openPart(part); err != nil {
			return err
		}
	}
	part.Refusal += delta
	return w.e.sink.Send(&RefusalDeltaEvent{ItemID: w.msg.ID, OutputIndex: w.index, ContentIndex: w.partIndex(), Delta: delta})
}

// Close finishes the open part and the message. It is called
// automatically when another item is opened or the response ends.
func (w *MessageWriter) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	if err := w.closePart(); err != nil {
		return err
	}
	return w.e.closeItem(w.index, w.msg)
}

func (w *MessageWriter) markIncomplete() { w.msg.Status = StatusIncomplete }

func (w *MessageWriter) textPart() (*OutputText, error) {
	if w.closed {
		return nil, errWriterClosed
	}
	if part, ok := w.part.(*OutputText); ok {
		return part, nil
	}
	if err := w.closePart(); err != nil {
		return nil, err
	}
	part := &OutputText{}
	return part, w.openPart(part)
}

func (w *MessageWriter) openPart(part Content) error {
	w.msg.Content = append(w.msg.Content, part)
	w.part = part
	return w.e.sink.Send(&ContentPartAddedEvent{ItemID: w.msg.ID, OutputIndex: w.index, ContentIndex: w.partIndex(), Part: part})
}

func (w *MessageWriter) closePart() error {
	if w.part == nil {
		return nil
	}
	part := w.part
	idx := w.partIndex()
	w.part = nil
	switch p := part.(type) {
	case *OutputText:
		if err := w.e.sink.Send(&OutputTextDoneEvent{ItemID: w.msg.ID, OutputIndex: w.index, ContentIndex: idx, Text: p.Text, Logprobs: p.Logprobs}); err != nil {
			return err
		}
	case *Refusal:
		if err := w.e.sink.Send(&RefusalDoneEvent{ItemID: w.msg.ID, OutputIndex: w.index, ContentIndex: idx, Refusal: p.Refusal}); err != nil {
			return err
		}
	}
	return w.e.sink.Send(&ContentPartDoneEvent{ItemID: w.msg.ID, OutputIndex: w.index, ContentIndex: idx, Part: part})
}

func (w *MessageWriter) partIndex() int { return len(w.msg.Content) - 1 }

// FunctionCallWriter streams the arguments of a function call.
type FunctionCallWriter struct {
	e      *Emitter
	index  int
	call   *FunctionCall
	closed bool
}

// Item returns the call being written.
func (w *FunctionCallWriter) Item() *FunctionCall { return w.call }

// Arguments appends delta to the JSON arguments.
func (w *FunctionCallWriter) Arguments(delta string) error {
	if w.closed {
		return errWriterClosed
	}
	w.call.Arguments += delta
	return w.e.sink.Send(&FunctionCallArgumentsDeltaEvent{ItemID: w.call.ID, OutputIndex: w.index, Delta: delta})
}

// Close sends function_call_arguments.done and output_item.done.
func (w *FunctionCallWriter) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	if err := w.e.sink.Send(&FunctionCallArgumentsDoneEvent{ItemID: w.call.ID, OutputIndex: w.index, Arguments: w.call.Arguments}); err != nil {
		return err
	}
	return w.e.closeItem(w.index, w.call)
}

func (w *FunctionCallWriter) markIncomplete() { w.call.Status = StatusIncomplete }

// ReasoningWriter streams a reasoning item: summary parts, reasoning
// text and encrypted content.
type ReasoningWriter struct {
	e           *Emitter
	index       int
	item        *ReasoningItem
	summary     *SummaryText // open summary part
	textOpen    bool
	textDone    bool
	closed      bool
	textContent *ReasoningText
}

// Item returns the reasoning item being written.
func (w *ReasoningWriter) Item() *ReasoningItem { return w.item }

// Summary appends delta to the open summary part, opening one if
// needed. Call EndSummary to start a new part on the next call.
func (w *ReasoningWriter) Summary(delta string) error {
	if w.closed {
		return errWriterClosed
	}
	if w.summary == nil {
		w.summary = &SummaryText{}
		w.item.Summary = append(w.item.Summary, w.summary)
		if err := w.e.sink.Send(&ReasoningSummaryPartAddedEvent{ItemID: w.item.ID, OutputIndex: w.index, SummaryIndex: w.summaryIndex(), Part: w.summary}); err != nil {
			return err
		}
	}
	w.summary.Text += delta
	return w.e.sink.Send(&ReasoningSummaryTextDeltaEvent{ItemID: w.item.ID, OutputIndex: w.index, SummaryIndex: w.summaryIndex(), Delta: delta})
}

// EndSummary closes the open summary part.
func (w *ReasoningWriter) EndSummary() error {
	if w.summary == nil {
		return nil
	}
	part := w.summary
	idx := w.summaryIndex()
	w.summary = nil
	if err := w.e.sink.Send(&ReasoningSummaryTextDoneEvent{ItemID: w.item.ID, OutputIndex: w.index, SummaryIndex: idx, Text: part.Text}); err != nil {
		return err
	}
	return w.e.sink.Send(&ReasoningSummaryPartDoneEvent{ItemID: w.item.ID, OutputIndex: w.index, SummaryIndex: idx, Part: part})
}

// Text appends delta to the reasoning content.
func (w *ReasoningWriter) Text(delta string) error {
	if w.closed {
		return errWriterClosed
	}
	if w.textDone {
		return fmt.Errorf("openresponses: reasoning text already finished")
	}
	if !w.textOpen {
		w.textOpen = true
		w.textContent = &ReasoningText{}
		w.item.Content = append(w.item.Content, w.textContent)
	}
	w.textContent.Text += delta
	return w.e.sink.Send(&ReasoningDeltaEvent{ItemID: w.item.ID, OutputIndex: w.index, ContentIndex: len(w.item.Content) - 1, Delta: delta})
}

// EncryptedContent sets the encrypted reasoning payload carried on the
// final item.
func (w *ReasoningWriter) EncryptedContent(s string) { w.item.EncryptedContent = s }

// Close finishes the open summary part, the reasoning text and the item.
func (w *ReasoningWriter) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	if err := w.EndSummary(); err != nil {
		return err
	}
	if w.textOpen && !w.textDone {
		w.textDone = true
		if err := w.e.sink.Send(&ReasoningDoneEvent{ItemID: w.item.ID, OutputIndex: w.index, ContentIndex: len(w.item.Content) - 1, Text: w.textContent.Text}); err != nil {
			return err
		}
	}
	return w.e.closeItem(w.index, w.item)
}

func (w *ReasoningWriter) markIncomplete() { w.item.Status = StatusIncomplete }

func (w *ReasoningWriter) summaryIndex() int { return len(w.item.Summary) - 1 }

var errWriterClosed = fmt.Errorf("openresponses: item writer is closed")
