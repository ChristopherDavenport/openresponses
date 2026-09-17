package openresponses

import (
	"encoding/json"
	"errors"
	"testing"
)

// recordingSink collects events in-package for emitter tests.
type recordingSink struct {
	events []StreamEvent
	fail   error
}

func (s *recordingSink) Send(ev StreamEvent) error {
	if s.fail != nil {
		return s.fail
	}
	if setter, ok := ev.(SequenceSetter); ok {
		setter.SetSequence(int64(len(s.events)))
	}
	data, err := EncodeEvent(ev)
	if err != nil {
		return err
	}
	decoded, err := DecodeEvent(data)
	if err != nil {
		return err
	}
	s.events = append(s.events, decoded)
	return nil
}

func (s *recordingSink) types() []string {
	out := make([]string, len(s.events))
	for i, ev := range s.events {
		out[i] = ev.EventType()
	}
	return out
}

func TestEmitterMessage(t *testing.T) {
	sink := &recordingSink{}
	em := NewEmitter(sink, NewResponse(Request{Model: "m"}))
	msg, err := em.Message(PhaseFinalAnswer)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"Hel", "lo"} {
		if err := msg.Text(d); err != nil {
			t.Fatal(err)
		}
	}
	if err := msg.Annotation(&URLCitation{URL: "https://x", Title: "x", EndIndex: 5}); err != nil {
		t.Fatal(err)
	}
	if err := msg.Logprobs(LogProb{Token: "Hel", Logprob: -0.1}, LogProb{Token: "lo", Logprob: -0.2}); err != nil {
		t.Fatal(err)
	}
	if err := msg.Refusal("no"); err != nil {
		t.Fatal(err)
	}
	em.Response().Usage = &Usage{TotalTokens: 1}
	if err := em.Complete(); err != nil {
		t.Fatal(err)
	}
	want := []string{
		EventResponseCreated, EventResponseInProgress,
		EventOutputItemAdded,
		EventContentPartAdded, EventOutputTextDelta, EventOutputTextDelta, EventOutputTextAnnotationAdded, EventOutputTextDone, EventContentPartDone,
		EventContentPartAdded, EventRefusalDelta, EventRefusalDone, EventContentPartDone,
		EventOutputItemDone,
		EventResponseCompleted,
	}
	got := sink.types()
	if len(got) != len(want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("event %d = %s, want %s", i, got[i], want[i])
		}
	}
	final, _ := TerminalResponse(sink.events[len(sink.events)-1])
	if final.OutputText() != "Hello" || final.Status != ResponseStatusCompleted || final.CompletedAt == nil || final.Usage == nil {
		t.Errorf("final = %+v", final)
	}
	m := final.Output[0].(*Message)
	if m.Status != StatusCompleted || m.Phase != PhaseFinalAnswer || len(m.Content) != 2 || m.Content[1].(*Refusal).Refusal != "no" {
		t.Errorf("message = %+v", m)
	}
	if len(m.Content[0].(*OutputText).Annotations) != 1 {
		t.Errorf("annotations = %+v", m.Content[0].(*OutputText).Annotations)
	}
	if lp := m.Content[0].(*OutputText).Logprobs; len(lp) != 2 || lp[1].Token != "lo" {
		t.Errorf("logprobs = %+v", lp)
	}
	for _, ev := range sink.events {
		if d, ok := ev.(*OutputTextDoneEvent); ok && len(d.Logprobs) != 2 {
			t.Errorf("output_text.done logprobs = %+v", d.Logprobs)
		}
	}
	// Every content event carries the message's id and index 0.
	for _, ev := range sink.events {
		if d, ok := ev.(*OutputTextDeltaEvent); ok && (d.ItemID != m.ID || d.OutputIndex != 0 || d.ContentIndex != 0) {
			t.Errorf("delta references %+v", d)
		}
	}
	if err := msg.Text("late"); !errors.Is(err, errWriterClosed) {
		t.Errorf("write after close: %v", err)
	}
	if err := em.Complete(); !errors.Is(err, ErrTerminalEventSent) {
		t.Errorf("second Complete: %v", err)
	}
}

func TestEmitterOpensNextItemClosesPrevious(t *testing.T) {
	sink := &recordingSink{}
	em := NewEmitter(sink, &Response{Model: "m"})
	rs, err := em.Reasoning()
	if err != nil {
		t.Fatal(err)
	}
	if err := rs.Summary("think"); err != nil {
		t.Fatal(err)
	}
	if err := rs.EndSummary(); err != nil {
		t.Fatal(err)
	}
	if err := rs.Summary("more"); err != nil {
		t.Fatal(err)
	}
	if err := rs.Text("deep"); err != nil {
		t.Fatal(err)
	}
	rs.EncryptedContent("enc")
	call, err := em.FunctionCall("", "lookup") // closes the reasoning item
	if err != nil {
		t.Fatal(err)
	}
	if err := call.Arguments(`{"q":`); err != nil {
		t.Fatal(err)
	}
	if err := call.Arguments(`1}`); err != nil {
		t.Fatal(err)
	}
	if err := em.Item(&Compaction{EncryptedContent: "c"}); err != nil { // closes the call
		t.Fatal(err)
	}
	if err := em.Item(&UnknownItem{Type: "acme:thing", Raw: json.RawMessage(`{"type":"acme:thing","id":"t1"}`)}); err != nil {
		t.Fatal(err)
	}
	if err := em.Incomplete(IncompleteReasonMaxOutputTokens); err != nil {
		t.Fatal(err)
	}
	final, _ := TerminalResponse(sink.events[len(sink.events)-1])
	if final.Status != ResponseStatusIncomplete || final.IncompleteDetails == nil || len(final.Output) != 4 {
		t.Fatalf("final = %+v", final)
	}
	r := final.Output[0].(*ReasoningItem)
	if len(r.Summary) != 2 || r.Summary[1].(*SummaryText).Text != "more" || r.Content.Text() != "deep" || r.EncryptedContent != "enc" || r.Status != StatusCompleted {
		t.Errorf("reasoning = %+v", r)
	}
	if fc := final.Output[1].(*FunctionCall); fc.Arguments != `{"q":1}` || fc.CallID == "" || fc.Status != StatusCompleted {
		t.Errorf("call = %+v", fc)
	}
	if c := final.Output[2].(*Compaction); c.Status != StatusCompleted {
		t.Errorf("compaction = %+v", c)
	}
	if u := final.Output[3].(*UnknownItem); u.ID != "t1" {
		t.Errorf("unknown = %+v", u)
	}
	// Stream shape: each item bookended, one open at a time.
	var open int
	for i, ev := range sink.events {
		switch ev.(type) {
		case *OutputItemAddedEvent:
			if open != 0 {
				t.Errorf("event %d: item added while one is open", i)
			}
			open++
		case *OutputItemDoneEvent:
			open--
		}
	}
	if open != 0 {
		t.Errorf("%d items left open", open)
	}
	if em.Response().ID == "" {
		t.Error("emitter did not assign a response id")
	}
}

func TestEmitterIncompleteMarksOpenItem(t *testing.T) {
	sink := &recordingSink{}
	em := NewEmitter(sink, &Response{Model: "m"})
	msg, err := em.Message("")
	if err != nil {
		t.Fatal(err)
	}
	if err := msg.Text("partial"); err != nil {
		t.Fatal(err)
	}
	if err := em.Incomplete(IncompleteReasonContentFilter); err != nil {
		t.Fatal(err)
	}
	final, _ := TerminalResponse(sink.events[len(sink.events)-1])
	if final.Output[0].(*Message).Status != StatusIncomplete {
		t.Errorf("message = %+v", final.Output[0])
	}
}

func TestEmitterPropagatesSinkErrors(t *testing.T) {
	sink := &recordingSink{fail: errors.New("gone")}
	em := NewEmitter(sink, &Response{Model: "m"})
	if _, err := em.Message(""); err == nil || err.Error() != "gone" {
		t.Errorf("err = %v", err)
	}
}

func TestResponseTerminalHelpers(t *testing.T) {
	var r Response
	r.Complete()
	if r.Status != ResponseStatusCompleted || r.CompletedAt == nil {
		t.Errorf("Complete: %+v", r)
	}
	r = Response{}
	r.Incomplete(IncompleteReasonMaxOutputTokens)
	if r.Status != ResponseStatusIncomplete || r.IncompleteDetails.Reason != IncompleteReasonMaxOutputTokens || r.CompletedAt == nil {
		t.Errorf("Incomplete: %+v", r)
	}
	r = Response{}
	r.Fail(TooManyRequests("rate", "slow"))
	if r.Status != ResponseStatusFailed || r.Error == nil || r.Error.Code != "rate" || r.Error.Type != ErrorTypeTooManyRequests {
		t.Errorf("Fail: %+v", r)
	}
}

func TestRequestIncludes(t *testing.T) {
	req := Request{Include: []Include{IncludeOutputTextLogprobs}}
	if !req.Includes(IncludeOutputTextLogprobs) || req.Includes(IncludeReasoningEncryptedContent) {
		t.Error("Includes wrong")
	}
}

func TestReasoningConfigShapes(t *testing.T) {
	data, _ := json.Marshal(Request{Model: "m"})
	if string(data) != `{"model":"m"}` {
		t.Errorf("zero reasoning not omitted: %s", data)
	}
	data, _ = json.Marshal(Request{Model: "m", Reasoning: ReasoningConfig{Effort: ReasoningEffortLow}})
	if string(data) != `{"model":"m","reasoning":{"effort":"low","summary":null}}` {
		t.Errorf("request reasoning: %s", data)
	}
	var resp Response
	if err := json.Unmarshal([]byte(`{"id":"r","reasoning":null}`), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Reasoning.IsZero() {
		t.Errorf("null decoded to %+v", resp.Reasoning)
	}
	data, _ = json.Marshal(CompactResponse{ID: "c"})
	var probe struct {
		Usage *Usage `json:"usage"`
	}
	if err := json.Unmarshal(data, &probe); err != nil || probe.Usage == nil {
		t.Errorf("compact usage not emitted: %s", data)
	}
}
