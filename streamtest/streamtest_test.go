package streamtest_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ChristopherDavenport/openresponses"
	"github.com/ChristopherDavenport/openresponses/streamtest"
)

// good is an adapter that uses the emitter and therefore streams
// correctly.
type good struct{}

func (good) CreateStream(_ context.Context, req openresponses.Request, sink openresponses.EventSink) error {
	em := openresponses.NewEmitter(sink, openresponses.NewResponse(req))
	msg, err := em.Message(openresponses.PhaseFinalAnswer)
	if err != nil {
		return err
	}
	if err := msg.Text("hello"); err != nil {
		return err
	}
	rs, err := em.Reasoning()
	if err != nil {
		return err
	}
	if err := rs.Summary("s"); err != nil {
		return err
	}
	call, err := em.FunctionCall("", "f")
	if err != nil {
		return err
	}
	if err := call.Arguments("{}"); err != nil {
		return err
	}
	return em.Complete()
}

func TestRunGood(t *testing.T) {
	sink, err := streamtest.Run(context.Background(), good{}, openresponses.Request{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if sink.Response().OutputText() != "hello" || len(sink.Response().Output) != 3 {
		t.Errorf("response = %+v", sink.Response())
	}
}

// broken applies a mutation to a correct event list so each lifecycle
// rule can be tested.
func broken(t *testing.T, mutate func([]openresponses.StreamEvent) []openresponses.StreamEvent) error {
	t.Helper()
	sink, err := streamtest.Run(context.Background(), good{}, openresponses.Request{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	events := mutate(sink.Events())
	for i, ev := range events {
		if setter, ok := ev.(interface{ SetSequence(int64) }); ok {
			setter.SetSequence(int64(i))
		}
	}
	return streamtest.Validate(events)
}

func remove(events []openresponses.StreamEvent, typ string, nth int) []openresponses.StreamEvent {
	seen := 0
	for i, ev := range events {
		if ev.EventType() == typ {
			if seen == nth {
				return append(append([]openresponses.StreamEvent(nil), events[:i]...), events[i+1:]...)
			}
			seen++
		}
	}
	return events
}

func TestValidateCatches(t *testing.T) {
	tests := []struct {
		name   string
		mutate func([]openresponses.StreamEvent) []openresponses.StreamEvent
		want   string
	}{
		{"missing content_part.done", func(e []openresponses.StreamEvent) []openresponses.StreamEvent {
			return remove(e, openresponses.EventContentPartDone, 0)
		}, "content part 0 is still open"},
		{"missing output_item.done", func(e []openresponses.StreamEvent) []openresponses.StreamEvent {
			return remove(e, openresponses.EventOutputItemDone, 0)
		}, "still open"},
		{"missing output_text.done", func(e []openresponses.StreamEvent) []openresponses.StreamEvent {
			return remove(e, openresponses.EventOutputTextDone, 0)
		}, "output_text.done missing"},
		{"missing summary text done", func(e []openresponses.StreamEvent) []openresponses.StreamEvent {
			return remove(e, openresponses.EventReasoningSummaryTextDone, 0)
		}, "reasoning_summary_text.done missing"},
		{"missing terminal", func(e []openresponses.StreamEvent) []openresponses.StreamEvent {
			return e[:len(e)-1]
		}, "without a terminal"},
		{"missing created", func(e []openresponses.StreamEvent) []openresponses.StreamEvent {
			return e[1:]
		}, "want response.created"},
		{"wrong output index", func(e []openresponses.StreamEvent) []openresponses.StreamEvent {
			for _, ev := range e {
				if a, ok := ev.(*openresponses.OutputItemAddedEvent); ok && a.OutputIndex == 1 {
					a.OutputIndex = 5
				}
			}
			return e
		}, "output_index 5, want 1"},
		{"wrong item id on delta", func(e []openresponses.StreamEvent) []openresponses.StreamEvent {
			for _, ev := range e {
				if d, ok := ev.(*openresponses.OutputTextDeltaEvent); ok {
					d.ItemID = "msg_other"
				}
			}
			return e
		}, `item_id "msg_other"`},
		{"event after terminal", func(e []openresponses.StreamEvent) []openresponses.StreamEvent {
			return append(e, &openresponses.OutputTextDeltaEvent{Delta: "x"})
		}, "after the terminal"},
		{"error without failed", func(e []openresponses.StreamEvent) []openresponses.StreamEvent {
			return append(e[:len(e)-1], &openresponses.ErrorEvent{}, e[len(e)-1])
		}, "must be followed by response.failed"},
		{"output mismatch", func(e []openresponses.StreamEvent) []openresponses.StreamEvent {
			final, _ := openresponses.TerminalResponse(e[len(e)-1])
			final.Output = final.Output[:1]
			return e
		}, "response has 1 output items, 3 were streamed"},
		{"missing arguments.done", func(e []openresponses.StreamEvent) []openresponses.StreamEvent {
			return remove(e, openresponses.EventFunctionCallArgumentsDone, 0)
		}, "function_call_arguments.done missing"},
		{"missing summary part done", func(e []openresponses.StreamEvent) []openresponses.StreamEvent {
			return remove(e, openresponses.EventReasoningSummaryPartDone, 0)
		}, "summary part 0 is still open"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := broken(t, tt.mutate)
			if err == nil {
				t.Fatal("expected validation error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestSinkRejectsAfterTerminal(t *testing.T) {
	sink := &streamtest.Sink{}
	if err := sink.Send(&openresponses.ResponseCompletedEvent{Response: &openresponses.Response{ID: "r"}}); err != nil {
		t.Fatal(err)
	}
	if err := sink.Send(&openresponses.OutputTextDeltaEvent{}); err == nil {
		t.Error("expected error after terminal event")
	}
}
