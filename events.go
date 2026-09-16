package openresponses

import (
	"encoding/json"
	"fmt"

	"github.com/ChristopherDavenport/openresponses/internal/jsonx"
)

// StreamEvent is one event in a Server-Sent Events or WebSocket stream.
// Decoded values are always pointers, so switch on *OutputTextDeltaEvent
// and so on. Unregistered event types decode to [UnknownEvent].
type StreamEvent interface {
	EventType() string
	Sequence() int64
}

// sequenceSetter is implemented by every built-in event so that server
// sinks can assign sequence numbers. Extension events that do not
// implement it are sent with whatever sequence number they carry.
type sequenceSetter interface {
	SetSequence(int64)
}

// ResponseCreatedEvent is emitted first: the response has been created.
type ResponseCreatedEvent struct {
	SequenceNumber int64     `json:"sequence_number"`
	Response       *Response `json:"response"`
}

// EventType returns "response.created".
func (*ResponseCreatedEvent) EventType() string { return EventResponseCreated }

// Sequence returns the sequence number.
func (e *ResponseCreatedEvent) Sequence() int64 { return e.SequenceNumber }

// SetSequence sets the sequence number.
func (e *ResponseCreatedEvent) SetSequence(n int64) { e.SequenceNumber = n }

// MarshalJSON emits the event with its type discriminator.
func (e *ResponseCreatedEvent) MarshalJSON() ([]byte, error) {
	type plain ResponseCreatedEvent
	return jsonx.MarshalTyped(EventResponseCreated, (*plain)(e))
}

// ResponseQueuedEvent reports that the response is queued.
type ResponseQueuedEvent struct {
	SequenceNumber int64     `json:"sequence_number"`
	Response       *Response `json:"response"`
}

// EventType returns "response.queued".
func (*ResponseQueuedEvent) EventType() string { return EventResponseQueued }

// Sequence returns the sequence number.
func (e *ResponseQueuedEvent) Sequence() int64 { return e.SequenceNumber }

// SetSequence sets the sequence number.
func (e *ResponseQueuedEvent) SetSequence(n int64) { e.SequenceNumber = n }

// MarshalJSON emits the event with its type discriminator.
func (e *ResponseQueuedEvent) MarshalJSON() ([]byte, error) {
	type plain ResponseQueuedEvent
	return jsonx.MarshalTyped(EventResponseQueued, (*plain)(e))
}

// ResponseInProgressEvent reports that generation has started.
type ResponseInProgressEvent struct {
	SequenceNumber int64     `json:"sequence_number"`
	Response       *Response `json:"response"`
}

// EventType returns "response.in_progress".
func (*ResponseInProgressEvent) EventType() string { return EventResponseInProgress }

// Sequence returns the sequence number.
func (e *ResponseInProgressEvent) Sequence() int64 { return e.SequenceNumber }

// SetSequence sets the sequence number.
func (e *ResponseInProgressEvent) SetSequence(n int64) { e.SequenceNumber = n }

// MarshalJSON emits the event with its type discriminator.
func (e *ResponseInProgressEvent) MarshalJSON() ([]byte, error) {
	type plain ResponseInProgressEvent
	return jsonx.MarshalTyped(EventResponseInProgress, (*plain)(e))
}

// ResponseCompletedEvent is terminal: the response completed.
type ResponseCompletedEvent struct {
	SequenceNumber int64     `json:"sequence_number"`
	Response       *Response `json:"response"`
}

// EventType returns "response.completed".
func (*ResponseCompletedEvent) EventType() string { return EventResponseCompleted }

// Sequence returns the sequence number.
func (e *ResponseCompletedEvent) Sequence() int64 { return e.SequenceNumber }

// SetSequence sets the sequence number.
func (e *ResponseCompletedEvent) SetSequence(n int64) { e.SequenceNumber = n }

// MarshalJSON emits the event with its type discriminator.
func (e *ResponseCompletedEvent) MarshalJSON() ([]byte, error) {
	type plain ResponseCompletedEvent
	return jsonx.MarshalTyped(EventResponseCompleted, (*plain)(e))
}

// ResponseFailedEvent is terminal: the response failed. Response.Error
// carries the reason.
type ResponseFailedEvent struct {
	SequenceNumber int64     `json:"sequence_number"`
	Response       *Response `json:"response"`
}

// EventType returns "response.failed".
func (*ResponseFailedEvent) EventType() string { return EventResponseFailed }

// Sequence returns the sequence number.
func (e *ResponseFailedEvent) Sequence() int64 { return e.SequenceNumber }

// SetSequence sets the sequence number.
func (e *ResponseFailedEvent) SetSequence(n int64) { e.SequenceNumber = n }

// MarshalJSON emits the event with its type discriminator.
func (e *ResponseFailedEvent) MarshalJSON() ([]byte, error) {
	type plain ResponseFailedEvent
	return jsonx.MarshalTyped(EventResponseFailed, (*plain)(e))
}

// ResponseIncompleteEvent is terminal: the response stopped early.
type ResponseIncompleteEvent struct {
	SequenceNumber int64     `json:"sequence_number"`
	Response       *Response `json:"response"`
}

// EventType returns "response.incomplete".
func (*ResponseIncompleteEvent) EventType() string { return EventResponseIncomplete }

// Sequence returns the sequence number.
func (e *ResponseIncompleteEvent) Sequence() int64 { return e.SequenceNumber }

// SetSequence sets the sequence number.
func (e *ResponseIncompleteEvent) SetSequence(n int64) { e.SequenceNumber = n }

// MarshalJSON emits the event with its type discriminator.
func (e *ResponseIncompleteEvent) MarshalJSON() ([]byte, error) {
	type plain ResponseIncompleteEvent
	return jsonx.MarshalTyped(EventResponseIncomplete, (*plain)(e))
}

// OutputItemAddedEvent announces a new output item, usually in_progress
// with empty content.
type OutputItemAddedEvent struct {
	SequenceNumber int64 `json:"sequence_number"`
	OutputIndex    int   `json:"output_index"`
	Item           Item  `json:"item"`
}

// EventType returns "response.output_item.added".
func (*OutputItemAddedEvent) EventType() string { return EventOutputItemAdded }

// Sequence returns the sequence number.
func (e *OutputItemAddedEvent) Sequence() int64 { return e.SequenceNumber }

// SetSequence sets the sequence number.
func (e *OutputItemAddedEvent) SetSequence(n int64) { e.SequenceNumber = n }

// MarshalJSON emits the event with its type discriminator.
func (e *OutputItemAddedEvent) MarshalJSON() ([]byte, error) {
	type plain OutputItemAddedEvent
	return jsonx.MarshalTyped(EventOutputItemAdded, (*plain)(e))
}

// UnmarshalJSON decodes the item through the item registry.
func (e *OutputItemAddedEvent) UnmarshalJSON(data []byte) error {
	var aux struct {
		SequenceNumber int64           `json:"sequence_number"`
		OutputIndex    int             `json:"output_index"`
		Item           json.RawMessage `json:"item"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	item, err := unmarshalItemField(aux.Item)
	if err != nil {
		return err
	}
	*e = OutputItemAddedEvent{SequenceNumber: aux.SequenceNumber, OutputIndex: aux.OutputIndex, Item: item}
	return nil
}

// OutputItemDoneEvent carries the final form of an output item.
type OutputItemDoneEvent struct {
	SequenceNumber int64 `json:"sequence_number"`
	OutputIndex    int   `json:"output_index"`
	Item           Item  `json:"item"`
}

// EventType returns "response.output_item.done".
func (*OutputItemDoneEvent) EventType() string { return EventOutputItemDone }

// Sequence returns the sequence number.
func (e *OutputItemDoneEvent) Sequence() int64 { return e.SequenceNumber }

// SetSequence sets the sequence number.
func (e *OutputItemDoneEvent) SetSequence(n int64) { e.SequenceNumber = n }

// MarshalJSON emits the event with its type discriminator.
func (e *OutputItemDoneEvent) MarshalJSON() ([]byte, error) {
	type plain OutputItemDoneEvent
	return jsonx.MarshalTyped(EventOutputItemDone, (*plain)(e))
}

// UnmarshalJSON decodes the item through the item registry.
func (e *OutputItemDoneEvent) UnmarshalJSON(data []byte) error {
	var aux struct {
		SequenceNumber int64           `json:"sequence_number"`
		OutputIndex    int             `json:"output_index"`
		Item           json.RawMessage `json:"item"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	item, err := unmarshalItemField(aux.Item)
	if err != nil {
		return err
	}
	*e = OutputItemDoneEvent{SequenceNumber: aux.SequenceNumber, OutputIndex: aux.OutputIndex, Item: item}
	return nil
}

// ContentPartAddedEvent announces a new content part in a message.
type ContentPartAddedEvent struct {
	SequenceNumber int64   `json:"sequence_number"`
	ItemID         string  `json:"item_id"`
	OutputIndex    int     `json:"output_index"`
	ContentIndex   int     `json:"content_index"`
	Part           Content `json:"part"`
}

// EventType returns "response.content_part.added".
func (*ContentPartAddedEvent) EventType() string { return EventContentPartAdded }

// Sequence returns the sequence number.
func (e *ContentPartAddedEvent) Sequence() int64 { return e.SequenceNumber }

// SetSequence sets the sequence number.
func (e *ContentPartAddedEvent) SetSequence(n int64) { e.SequenceNumber = n }

// MarshalJSON emits the event with its type discriminator.
func (e *ContentPartAddedEvent) MarshalJSON() ([]byte, error) {
	type plain ContentPartAddedEvent
	return jsonx.MarshalTyped(EventContentPartAdded, (*plain)(e))
}

// UnmarshalJSON decodes the part through the content registry.
func (e *ContentPartAddedEvent) UnmarshalJSON(data []byte) error {
	var aux contentPartAux
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	part, err := unmarshalContentField(aux.Part)
	if err != nil {
		return err
	}
	*e = ContentPartAddedEvent{aux.SequenceNumber, aux.ItemID, aux.OutputIndex, aux.ContentIndex, part}
	return nil
}

// ContentPartDoneEvent carries the final form of a content part.
type ContentPartDoneEvent struct {
	SequenceNumber int64   `json:"sequence_number"`
	ItemID         string  `json:"item_id"`
	OutputIndex    int     `json:"output_index"`
	ContentIndex   int     `json:"content_index"`
	Part           Content `json:"part"`
}

// EventType returns "response.content_part.done".
func (*ContentPartDoneEvent) EventType() string { return EventContentPartDone }

// Sequence returns the sequence number.
func (e *ContentPartDoneEvent) Sequence() int64 { return e.SequenceNumber }

// SetSequence sets the sequence number.
func (e *ContentPartDoneEvent) SetSequence(n int64) { e.SequenceNumber = n }

// MarshalJSON emits the event with its type discriminator.
func (e *ContentPartDoneEvent) MarshalJSON() ([]byte, error) {
	type plain ContentPartDoneEvent
	return jsonx.MarshalTyped(EventContentPartDone, (*plain)(e))
}

// UnmarshalJSON decodes the part through the content registry.
func (e *ContentPartDoneEvent) UnmarshalJSON(data []byte) error {
	var aux contentPartAux
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	part, err := unmarshalContentField(aux.Part)
	if err != nil {
		return err
	}
	*e = ContentPartDoneEvent{aux.SequenceNumber, aux.ItemID, aux.OutputIndex, aux.ContentIndex, part}
	return nil
}

// OutputTextDeltaEvent carries a chunk of output text.
type OutputTextDeltaEvent struct {
	SequenceNumber int64     `json:"sequence_number"`
	ItemID         string    `json:"item_id"`
	OutputIndex    int       `json:"output_index"`
	ContentIndex   int       `json:"content_index"`
	Delta          string    `json:"delta"`
	Logprobs       []LogProb `json:"logprobs,omitempty"`
	Obfuscation    string    `json:"obfuscation,omitempty"`
}

// EventType returns "response.output_text.delta".
func (*OutputTextDeltaEvent) EventType() string { return EventOutputTextDelta }

// Sequence returns the sequence number.
func (e *OutputTextDeltaEvent) Sequence() int64 { return e.SequenceNumber }

// SetSequence sets the sequence number.
func (e *OutputTextDeltaEvent) SetSequence(n int64) { e.SequenceNumber = n }

// MarshalJSON emits the event with its type discriminator.
func (e *OutputTextDeltaEvent) MarshalJSON() ([]byte, error) {
	type plain OutputTextDeltaEvent
	return jsonx.MarshalTyped(EventOutputTextDelta, (*plain)(e))
}

// OutputTextDoneEvent carries the complete text of a part.
type OutputTextDoneEvent struct {
	SequenceNumber int64     `json:"sequence_number"`
	ItemID         string    `json:"item_id"`
	OutputIndex    int       `json:"output_index"`
	ContentIndex   int       `json:"content_index"`
	Text           string    `json:"text"`
	Logprobs       []LogProb `json:"logprobs,omitempty"`
}

// EventType returns "response.output_text.done".
func (*OutputTextDoneEvent) EventType() string { return EventOutputTextDone }

// Sequence returns the sequence number.
func (e *OutputTextDoneEvent) Sequence() int64 { return e.SequenceNumber }

// SetSequence sets the sequence number.
func (e *OutputTextDoneEvent) SetSequence(n int64) { e.SequenceNumber = n }

// MarshalJSON emits the event with its type discriminator.
func (e *OutputTextDoneEvent) MarshalJSON() ([]byte, error) {
	type plain OutputTextDoneEvent
	return jsonx.MarshalTyped(EventOutputTextDone, (*plain)(e))
}

// RefusalDeltaEvent carries a chunk of refusal text.
type RefusalDeltaEvent struct {
	SequenceNumber int64  `json:"sequence_number"`
	ItemID         string `json:"item_id"`
	OutputIndex    int    `json:"output_index"`
	ContentIndex   int    `json:"content_index"`
	Delta          string `json:"delta"`
	Obfuscation    string `json:"obfuscation,omitempty"`
}

// EventType returns "response.refusal.delta".
func (*RefusalDeltaEvent) EventType() string { return EventRefusalDelta }

// Sequence returns the sequence number.
func (e *RefusalDeltaEvent) Sequence() int64 { return e.SequenceNumber }

// SetSequence sets the sequence number.
func (e *RefusalDeltaEvent) SetSequence(n int64) { e.SequenceNumber = n }

// MarshalJSON emits the event with its type discriminator.
func (e *RefusalDeltaEvent) MarshalJSON() ([]byte, error) {
	type plain RefusalDeltaEvent
	return jsonx.MarshalTyped(EventRefusalDelta, (*plain)(e))
}

// RefusalDoneEvent carries the complete refusal text.
type RefusalDoneEvent struct {
	SequenceNumber int64  `json:"sequence_number"`
	ItemID         string `json:"item_id"`
	OutputIndex    int    `json:"output_index"`
	ContentIndex   int    `json:"content_index"`
	Refusal        string `json:"refusal"`
}

// EventType returns "response.refusal.done".
func (*RefusalDoneEvent) EventType() string { return EventRefusalDone }

// Sequence returns the sequence number.
func (e *RefusalDoneEvent) Sequence() int64 { return e.SequenceNumber }

// SetSequence sets the sequence number.
func (e *RefusalDoneEvent) SetSequence(n int64) { e.SequenceNumber = n }

// MarshalJSON emits the event with its type discriminator.
func (e *RefusalDoneEvent) MarshalJSON() ([]byte, error) {
	type plain RefusalDoneEvent
	return jsonx.MarshalTyped(EventRefusalDone, (*plain)(e))
}

// FunctionCallArgumentsDeltaEvent carries a chunk of function call
// arguments.
type FunctionCallArgumentsDeltaEvent struct {
	SequenceNumber int64  `json:"sequence_number"`
	ItemID         string `json:"item_id"`
	OutputIndex    int    `json:"output_index"`
	Delta          string `json:"delta"`
	Obfuscation    string `json:"obfuscation,omitempty"`
}

// EventType returns "response.function_call_arguments.delta".
func (*FunctionCallArgumentsDeltaEvent) EventType() string {
	return EventFunctionCallArgumentsDelta
}

// Sequence returns the sequence number.
func (e *FunctionCallArgumentsDeltaEvent) Sequence() int64 { return e.SequenceNumber }

// SetSequence sets the sequence number.
func (e *FunctionCallArgumentsDeltaEvent) SetSequence(n int64) { e.SequenceNumber = n }

// MarshalJSON emits the event with its type discriminator.
func (e *FunctionCallArgumentsDeltaEvent) MarshalJSON() ([]byte, error) {
	type plain FunctionCallArgumentsDeltaEvent
	return jsonx.MarshalTyped(EventFunctionCallArgumentsDelta, (*plain)(e))
}

// FunctionCallArgumentsDoneEvent carries the complete arguments.
type FunctionCallArgumentsDoneEvent struct {
	SequenceNumber int64  `json:"sequence_number"`
	ItemID         string `json:"item_id"`
	OutputIndex    int    `json:"output_index"`
	Arguments      string `json:"arguments"`
}

// EventType returns "response.function_call_arguments.done".
func (*FunctionCallArgumentsDoneEvent) EventType() string {
	return EventFunctionCallArgumentsDone
}

// Sequence returns the sequence number.
func (e *FunctionCallArgumentsDoneEvent) Sequence() int64 { return e.SequenceNumber }

// SetSequence sets the sequence number.
func (e *FunctionCallArgumentsDoneEvent) SetSequence(n int64) { e.SequenceNumber = n }

// MarshalJSON emits the event with its type discriminator.
func (e *FunctionCallArgumentsDoneEvent) MarshalJSON() ([]byte, error) {
	type plain FunctionCallArgumentsDoneEvent
	return jsonx.MarshalTyped(EventFunctionCallArgumentsDone, (*plain)(e))
}

// ReasoningSummaryPartAddedEvent announces a new reasoning summary part.
type ReasoningSummaryPartAddedEvent struct {
	SequenceNumber int64   `json:"sequence_number"`
	ItemID         string  `json:"item_id"`
	OutputIndex    int     `json:"output_index"`
	SummaryIndex   int     `json:"summary_index"`
	Part           Content `json:"part"`
}

// EventType returns "response.reasoning_summary_part.added".
func (*ReasoningSummaryPartAddedEvent) EventType() string {
	return EventReasoningSummaryPartAdded
}

// Sequence returns the sequence number.
func (e *ReasoningSummaryPartAddedEvent) Sequence() int64 { return e.SequenceNumber }

// SetSequence sets the sequence number.
func (e *ReasoningSummaryPartAddedEvent) SetSequence(n int64) { e.SequenceNumber = n }

// MarshalJSON emits the event with its type discriminator.
func (e *ReasoningSummaryPartAddedEvent) MarshalJSON() ([]byte, error) {
	type plain ReasoningSummaryPartAddedEvent
	return jsonx.MarshalTyped(EventReasoningSummaryPartAdded, (*plain)(e))
}

// UnmarshalJSON decodes the part through the content registry.
func (e *ReasoningSummaryPartAddedEvent) UnmarshalJSON(data []byte) error {
	var aux summaryPartAux
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	part, err := unmarshalContentField(aux.Part)
	if err != nil {
		return err
	}
	*e = ReasoningSummaryPartAddedEvent{aux.SequenceNumber, aux.ItemID, aux.OutputIndex, aux.SummaryIndex, part}
	return nil
}

// ReasoningSummaryPartDoneEvent carries the final reasoning summary part.
type ReasoningSummaryPartDoneEvent struct {
	SequenceNumber int64   `json:"sequence_number"`
	ItemID         string  `json:"item_id"`
	OutputIndex    int     `json:"output_index"`
	SummaryIndex   int     `json:"summary_index"`
	Part           Content `json:"part"`
}

// EventType returns "response.reasoning_summary_part.done".
func (*ReasoningSummaryPartDoneEvent) EventType() string {
	return EventReasoningSummaryPartDone
}

// Sequence returns the sequence number.
func (e *ReasoningSummaryPartDoneEvent) Sequence() int64 { return e.SequenceNumber }

// SetSequence sets the sequence number.
func (e *ReasoningSummaryPartDoneEvent) SetSequence(n int64) { e.SequenceNumber = n }

// MarshalJSON emits the event with its type discriminator.
func (e *ReasoningSummaryPartDoneEvent) MarshalJSON() ([]byte, error) {
	type plain ReasoningSummaryPartDoneEvent
	return jsonx.MarshalTyped(EventReasoningSummaryPartDone, (*plain)(e))
}

// UnmarshalJSON decodes the part through the content registry.
func (e *ReasoningSummaryPartDoneEvent) UnmarshalJSON(data []byte) error {
	var aux summaryPartAux
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	part, err := unmarshalContentField(aux.Part)
	if err != nil {
		return err
	}
	*e = ReasoningSummaryPartDoneEvent{aux.SequenceNumber, aux.ItemID, aux.OutputIndex, aux.SummaryIndex, part}
	return nil
}

// ReasoningSummaryTextDeltaEvent carries a chunk of reasoning summary
// text.
type ReasoningSummaryTextDeltaEvent struct {
	SequenceNumber int64  `json:"sequence_number"`
	ItemID         string `json:"item_id"`
	OutputIndex    int    `json:"output_index"`
	SummaryIndex   int    `json:"summary_index"`
	Delta          string `json:"delta"`
	Obfuscation    string `json:"obfuscation,omitempty"`
}

// EventType returns "response.reasoning_summary_text.delta".
func (*ReasoningSummaryTextDeltaEvent) EventType() string {
	return EventReasoningSummaryTextDelta
}

// Sequence returns the sequence number.
func (e *ReasoningSummaryTextDeltaEvent) Sequence() int64 { return e.SequenceNumber }

// SetSequence sets the sequence number.
func (e *ReasoningSummaryTextDeltaEvent) SetSequence(n int64) { e.SequenceNumber = n }

// MarshalJSON emits the event with its type discriminator.
func (e *ReasoningSummaryTextDeltaEvent) MarshalJSON() ([]byte, error) {
	type plain ReasoningSummaryTextDeltaEvent
	return jsonx.MarshalTyped(EventReasoningSummaryTextDelta, (*plain)(e))
}

// ReasoningSummaryTextDoneEvent carries the complete reasoning summary
// text.
type ReasoningSummaryTextDoneEvent struct {
	SequenceNumber int64  `json:"sequence_number"`
	ItemID         string `json:"item_id"`
	OutputIndex    int    `json:"output_index"`
	SummaryIndex   int    `json:"summary_index"`
	Text           string `json:"text"`
}

// EventType returns "response.reasoning_summary_text.done".
func (*ReasoningSummaryTextDoneEvent) EventType() string {
	return EventReasoningSummaryTextDone
}

// Sequence returns the sequence number.
func (e *ReasoningSummaryTextDoneEvent) Sequence() int64 { return e.SequenceNumber }

// SetSequence sets the sequence number.
func (e *ReasoningSummaryTextDoneEvent) SetSequence(n int64) { e.SequenceNumber = n }

// MarshalJSON emits the event with its type discriminator.
func (e *ReasoningSummaryTextDoneEvent) MarshalJSON() ([]byte, error) {
	type plain ReasoningSummaryTextDoneEvent
	return jsonx.MarshalTyped(EventReasoningSummaryTextDone, (*plain)(e))
}

// ReasoningDeltaEvent carries a chunk of reasoning text.
type ReasoningDeltaEvent struct {
	SequenceNumber int64  `json:"sequence_number"`
	ItemID         string `json:"item_id"`
	OutputIndex    int    `json:"output_index"`
	ContentIndex   int    `json:"content_index"`
	Delta          string `json:"delta"`
	Obfuscation    string `json:"obfuscation,omitempty"`
}

// EventType returns "response.reasoning.delta".
func (*ReasoningDeltaEvent) EventType() string { return EventReasoningDelta }

// Sequence returns the sequence number.
func (e *ReasoningDeltaEvent) Sequence() int64 { return e.SequenceNumber }

// SetSequence sets the sequence number.
func (e *ReasoningDeltaEvent) SetSequence(n int64) { e.SequenceNumber = n }

// MarshalJSON emits the event with its type discriminator.
func (e *ReasoningDeltaEvent) MarshalJSON() ([]byte, error) {
	type plain ReasoningDeltaEvent
	return jsonx.MarshalTyped(EventReasoningDelta, (*plain)(e))
}

// ReasoningDoneEvent carries the complete reasoning text.
type ReasoningDoneEvent struct {
	SequenceNumber int64  `json:"sequence_number"`
	ItemID         string `json:"item_id"`
	OutputIndex    int    `json:"output_index"`
	ContentIndex   int    `json:"content_index"`
	Text           string `json:"text"`
}

// EventType returns "response.reasoning.done".
func (*ReasoningDoneEvent) EventType() string { return EventReasoningDone }

// Sequence returns the sequence number.
func (e *ReasoningDoneEvent) Sequence() int64 { return e.SequenceNumber }

// SetSequence sets the sequence number.
func (e *ReasoningDoneEvent) SetSequence(n int64) { e.SequenceNumber = n }

// MarshalJSON emits the event with its type discriminator.
func (e *ReasoningDoneEvent) MarshalJSON() ([]byte, error) {
	type plain ReasoningDoneEvent
	return jsonx.MarshalTyped(EventReasoningDone, (*plain)(e))
}

// OutputTextAnnotationAddedEvent announces an annotation on an output
// text part.
type OutputTextAnnotationAddedEvent struct {
	SequenceNumber  int64      `json:"sequence_number"`
	ItemID          string     `json:"item_id"`
	OutputIndex     int        `json:"output_index"`
	ContentIndex    int        `json:"content_index"`
	AnnotationIndex int        `json:"annotation_index"`
	Annotation      Annotation `json:"annotation"`
}

// EventType returns "response.output_text.annotation.added".
func (*OutputTextAnnotationAddedEvent) EventType() string {
	return EventOutputTextAnnotationAdded
}

// Sequence returns the sequence number.
func (e *OutputTextAnnotationAddedEvent) Sequence() int64 { return e.SequenceNumber }

// SetSequence sets the sequence number.
func (e *OutputTextAnnotationAddedEvent) SetSequence(n int64) { e.SequenceNumber = n }

// MarshalJSON emits the event with its type discriminator.
func (e *OutputTextAnnotationAddedEvent) MarshalJSON() ([]byte, error) {
	type plain OutputTextAnnotationAddedEvent
	return jsonx.MarshalTyped(EventOutputTextAnnotationAdded, (*plain)(e))
}

// UnmarshalJSON decodes the annotation through the annotation registry.
func (e *OutputTextAnnotationAddedEvent) UnmarshalJSON(data []byte) error {
	var aux struct {
		SequenceNumber  int64           `json:"sequence_number"`
		ItemID          string          `json:"item_id"`
		OutputIndex     int             `json:"output_index"`
		ContentIndex    int             `json:"content_index"`
		AnnotationIndex int             `json:"annotation_index"`
		Annotation      json.RawMessage `json:"annotation"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	var ann Annotation
	if len(aux.Annotation) > 0 && string(aux.Annotation) != "null" {
		var err error
		if ann, err = UnmarshalAnnotation(aux.Annotation); err != nil {
			return err
		}
	}
	*e = OutputTextAnnotationAddedEvent{aux.SequenceNumber, aux.ItemID, aux.OutputIndex, aux.ContentIndex, aux.AnnotationIndex, ann}
	return nil
}

// ErrorEvent reports an error. In an SSE stream it is followed by
// response.failed. On a WebSocket it is the terminal frame of a turn and
// carries an HTTP-style Status instead of a sequence number.
type ErrorEvent struct {
	SequenceNumber int64        `json:"sequence_number"`
	Status         int          `json:"status,omitempty"`
	Error          ErrorPayload `json:"error"`
}

// EventType returns "error".
func (*ErrorEvent) EventType() string { return EventError }

// Sequence returns the sequence number.
func (e *ErrorEvent) Sequence() int64 { return e.SequenceNumber }

// SetSequence sets the sequence number.
func (e *ErrorEvent) SetSequence(n int64) { e.SequenceNumber = n }

// MarshalJSON emits the event with its type discriminator.
func (e *ErrorEvent) MarshalJSON() ([]byte, error) {
	type plain ErrorEvent
	return jsonx.MarshalTyped(EventError, (*plain)(e))
}

// Err converts the event into an *Error.
func (e *ErrorEvent) Err() *Error { return e.Error.Err(e.Status) }

// UnknownEvent is an event whose type is not registered. Raw holds the
// original bytes and is re-emitted verbatim.
type UnknownEvent struct {
	Type           string
	SequenceNumber int64
	Raw            json.RawMessage
}

// EventType returns the wire type.
func (e *UnknownEvent) EventType() string { return e.Type }

// Sequence returns the sequence number.
func (e *UnknownEvent) Sequence() int64 { return e.SequenceNumber }

// MarshalJSON emits the original bytes.
func (e *UnknownEvent) MarshalJSON() ([]byte, error) {
	if len(e.Raw) == 0 {
		return jsonx.MarshalTyped(e.Type, struct {
			SequenceNumber int64 `json:"sequence_number"`
		}{e.SequenceNumber})
	}
	return e.Raw, nil
}

// UnmarshalJSON records the type, sequence number and raw bytes.
func (e *UnknownEvent) UnmarshalJSON(data []byte) error {
	var head struct {
		Type           string `json:"type"`
		SequenceNumber int64  `json:"sequence_number"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return err
	}
	e.Type = head.Type
	e.SequenceNumber = head.SequenceNumber
	e.Raw = append(json.RawMessage(nil), data...)
	return nil
}

type contentPartAux struct {
	SequenceNumber int64           `json:"sequence_number"`
	ItemID         string          `json:"item_id"`
	OutputIndex    int             `json:"output_index"`
	ContentIndex   int             `json:"content_index"`
	Part           json.RawMessage `json:"part"`
}

type summaryPartAux struct {
	SequenceNumber int64           `json:"sequence_number"`
	ItemID         string          `json:"item_id"`
	OutputIndex    int             `json:"output_index"`
	SummaryIndex   int             `json:"summary_index"`
	Part           json.RawMessage `json:"part"`
}

func unmarshalItemField(raw json.RawMessage) (Item, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	return UnmarshalItem(raw)
}

func unmarshalContentField(raw json.RawMessage) (Content, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	return UnmarshalContent(raw)
}

var eventRegistry = &registry[StreamEvent]{decoders: map[string]func(json.RawMessage) (StreamEvent, error){
	EventResponseCreated:            decodeInto[StreamEvent, ResponseCreatedEvent],
	EventResponseQueued:             decodeInto[StreamEvent, ResponseQueuedEvent],
	EventResponseInProgress:         decodeInto[StreamEvent, ResponseInProgressEvent],
	EventResponseCompleted:          decodeInto[StreamEvent, ResponseCompletedEvent],
	EventResponseFailed:             decodeInto[StreamEvent, ResponseFailedEvent],
	EventResponseIncomplete:         decodeInto[StreamEvent, ResponseIncompleteEvent],
	EventOutputItemAdded:            decodeInto[StreamEvent, OutputItemAddedEvent],
	EventOutputItemDone:             decodeInto[StreamEvent, OutputItemDoneEvent],
	EventContentPartAdded:           decodeInto[StreamEvent, ContentPartAddedEvent],
	EventContentPartDone:            decodeInto[StreamEvent, ContentPartDoneEvent],
	EventOutputTextDelta:            decodeInto[StreamEvent, OutputTextDeltaEvent],
	EventOutputTextDone:             decodeInto[StreamEvent, OutputTextDoneEvent],
	EventRefusalDelta:               decodeInto[StreamEvent, RefusalDeltaEvent],
	EventRefusalDone:                decodeInto[StreamEvent, RefusalDoneEvent],
	EventFunctionCallArgumentsDelta: decodeInto[StreamEvent, FunctionCallArgumentsDeltaEvent],
	EventFunctionCallArgumentsDone:  decodeInto[StreamEvent, FunctionCallArgumentsDoneEvent],
	EventReasoningSummaryPartAdded:  decodeInto[StreamEvent, ReasoningSummaryPartAddedEvent],
	EventReasoningSummaryPartDone:   decodeInto[StreamEvent, ReasoningSummaryPartDoneEvent],
	EventReasoningSummaryTextDelta:  decodeInto[StreamEvent, ReasoningSummaryTextDeltaEvent],
	EventReasoningSummaryTextDone:   decodeInto[StreamEvent, ReasoningSummaryTextDoneEvent],
	EventReasoningDelta:             decodeInto[StreamEvent, ReasoningDeltaEvent],
	EventReasoningDone:              decodeInto[StreamEvent, ReasoningDoneEvent],
	EventOutputTextAnnotationAdded:  decodeInto[StreamEvent, OutputTextAnnotationAddedEvent],
	EventError:                      decodeInto[StreamEvent, ErrorEvent],
}}

// RegisterEvent registers a decoder for an extension event type.
func RegisterEvent(typ string, decode func(json.RawMessage) (StreamEvent, error)) {
	eventRegistry.register(typ, decode)
}

// DecodeEvent decodes one streaming event, dispatching on its "type".
// Unregistered types decode to [*UnknownEvent]. A bare {"error": {...}}
// envelope without a type, which some servers emit mid-stream, is
// promoted to an [*ErrorEvent].
func DecodeEvent(data []byte) (StreamEvent, error) {
	typ, err := jsonx.PeekType(data)
	if err != nil {
		return nil, fmt.Errorf("event: %w", err)
	}
	if typ == "" && jsonx.HasKey(data, "error") {
		typ = EventError
	}
	if decode, ok := eventRegistry.lookup(typ); ok {
		ev, err := decode(data)
		if err != nil {
			return nil, fmt.Errorf("event %q: %w", typ, err)
		}
		return ev, nil
	}
	var u UnknownEvent
	if err := json.Unmarshal(data, &u); err != nil {
		return nil, fmt.Errorf("event %q: %w", typ, err)
	}
	return &u, nil
}

// EncodeEvent marshals an event to its wire form.
func EncodeEvent(ev StreamEvent) ([]byte, error) {
	return json.Marshal(ev)
}

// TerminalResponse returns the response carried by a terminal event
// (response.completed, response.failed or response.incomplete) and true,
// or nil and false for any other event.
func TerminalResponse(ev StreamEvent) (*Response, bool) {
	switch e := ev.(type) {
	case *ResponseCompletedEvent:
		return e.Response, true
	case *ResponseFailedEvent:
		return e.Response, true
	case *ResponseIncompleteEvent:
		return e.Response, true
	}
	return nil, false
}
