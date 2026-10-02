package streamtest_test

import (
	"context"
	"regexp"
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
		if setter, ok := ev.(openresponses.SequenceSetter); ok {
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
		{"reused output index", func(e []openresponses.StreamEvent) []openresponses.StreamEvent {
			// Every item opened at index 0, as Ollama 0.23 streams
			// parallel calls. Accumulator keeps them all (openresponses#26);
			// the validator still refuses the stream as non-conforming.
			for _, ev := range e {
				if a, ok := ev.(*openresponses.OutputItemAddedEvent); ok {
					a.OutputIndex = 0
				}
			}
			return e
		}, "output_index 0, want 1"},
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

// reindexed rewrites every output_index in good's stream through its
// wire form, so the events inside each item follow the index it was added
// at, as an Ollama 0.23 stream does. to maps an original index to the one
// sent.
func reindexed(t *testing.T, to func(int) int) []openresponses.StreamEvent {
	t.Helper()
	sink, err := streamtest.Run(context.Background(), good{}, openresponses.Request{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`"output_index":(\d+)`)
	var out []openresponses.StreamEvent
	for _, ev := range sink.Events() {
		data, err := openresponses.EncodeEvent(ev)
		if err != nil {
			t.Fatal(err)
		}
		data = re.ReplaceAllFunc(data, func(m []byte) []byte {
			var n int
			for _, c := range re.FindSubmatch(m)[1] {
				n = n*10 + int(c-'0')
			}
			return []byte(`"output_index":` + string(rune('0'+to(n))))
		})
		decoded, err := openresponses.DecodeEvent(data)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, decoded)
	}
	return out
}

func allZero(int) int { return 0 }

func TestOutputIndexReuse(t *testing.T) {
	reuse := streamtest.WithOutputIndexReuse()

	t.Run("strict by default", func(t *testing.T) {
		err := streamtest.Validate(reindexed(t, allZero))
		if err == nil || !strings.Contains(err.Error(), "output_index 0, want 1") {
			t.Errorf("err = %v, want output_index 0, want 1", err)
		}
	})
	t.Run("option accepts the reuse", func(t *testing.T) {
		if err := streamtest.Validate(reindexed(t, allZero), reuse); err != nil {
			t.Errorf("err = %v, want nil", err)
		}
	})
	t.Run("option accepts reuse of an earlier index among fresh ones", func(t *testing.T) {
		// Items at 0, 1, 0: the third reuses index 0 after it closed.
		events := reindexed(t, func(i int) int { return []int{0, 1, 0}[i] })
		if err := streamtest.Validate(events, reuse); err != nil {
			t.Errorf("err = %v, want nil", err)
		}
		if err := streamtest.Validate(events); err == nil {
			t.Error("strict validation accepted the reuse")
		}
	})
	t.Run("option accepts a fresh index after a reuse", func(t *testing.T) {
		// Items at 0, 0, 1 and 0, 1, 0: the next unused index counts
		// distinct indexes, not items.
		for _, idx := range [][]int{{0, 0, 1}, {0, 1, 1}} {
			events := reindexed(t, func(i int) int { return idx[i] })
			if err := streamtest.Validate(events, reuse); err != nil {
				t.Errorf("indexes %v: err = %v, want nil", idx, err)
			}
		}
		events := reindexed(t, func(i int) int { return []int{0, 0, 2}[i] })
		err := streamtest.Validate(events, reuse)
		if err == nil || !strings.Contains(err.Error(), "output_index 2, want 1") {
			t.Errorf("err = %v, want output_index 2, want 1", err)
		}
	})
	t.Run("option refuses a negative index", func(t *testing.T) {
		sink, err := streamtest.Run(context.Background(), good{}, openresponses.Request{Model: "m"})
		if err != nil {
			t.Fatal(err)
		}
		events := sink.Events()
		for _, ev := range events {
			if a, ok := ev.(*openresponses.OutputItemAddedEvent); ok && a.OutputIndex == 1 {
				a.OutputIndex = -1
			}
		}
		err = streamtest.Validate(events, reuse)
		if err == nil || !strings.Contains(err.Error(), "output_index -1, want 1") {
			t.Errorf("err = %v, want output_index -1, want 1", err)
		}
	})
	t.Run("option accepts a conforming stream", func(t *testing.T) {
		if err := streamtest.Validate(reindexed(t, func(i int) int { return i }), reuse); err != nil {
			t.Errorf("err = %v, want nil", err)
		}
	})
	t.Run("option still refuses a gap", func(t *testing.T) {
		err := streamtest.Validate(reindexed(t, func(i int) int { return []int{0, 2, 0}[i] }), reuse)
		if err == nil || !strings.Contains(err.Error(), "output_index 2, want 1") {
			t.Errorf("err = %v, want output_index 2, want 1", err)
		}
	})
	t.Run("option still holds events to the item's index", func(t *testing.T) {
		// Only the added events are rewritten, so the item's own events
		// name an index other than the one it was added at.
		sink, err := streamtest.Run(context.Background(), good{}, openresponses.Request{Model: "m"})
		if err != nil {
			t.Fatal(err)
		}
		events := sink.Events()
		for _, ev := range events {
			if a, ok := ev.(*openresponses.OutputItemAddedEvent); ok {
				a.OutputIndex = 0
			}
		}
		err = streamtest.Validate(events, reuse)
		if err == nil || !strings.Contains(err.Error(), "output_index 1, open item is 0") {
			t.Errorf("err = %v, want output_index 1, open item is 0", err)
		}
	})
	t.Run("option still checks the terminal response", func(t *testing.T) {
		events := reindexed(t, allZero)
		final, _ := openresponses.TerminalResponse(events[len(events)-1])
		final.Output = final.Output[:1]
		err := streamtest.Validate(events, reuse)
		if err == nil || !strings.Contains(err.Error(), "response has 1 output items, 3 were streamed") {
			t.Errorf("err = %v, want output mismatch", err)
		}
	})
	t.Run("Run takes the option", func(t *testing.T) {
		adapter := reindexAdapter{}
		if _, err := streamtest.Run(context.Background(), adapter, openresponses.Request{Model: "m"}); err == nil {
			t.Error("Run accepted the reuse without the option")
		}
		sink, err := streamtest.Run(context.Background(), adapter, openresponses.Request{Model: "m"}, reuse)
		if err != nil {
			t.Fatal(err)
		}
		if got := len(sink.Response().Output); got != 3 {
			t.Errorf("response holds %d items, want 3", got)
		}
		if sink.Response().OutputText() != "hello" {
			t.Errorf("response = %+v", sink.Response())
		}
	})
}

// reindexAdapter streams good's items all at output index 0.
type reindexAdapter struct{}

func (reindexAdapter) CreateStream(ctx context.Context, req openresponses.Request, sink openresponses.EventSink) error {
	return good{}.CreateStream(ctx, req, zeroIndexSink{sink})
}

type zeroIndexSink struct{ openresponses.EventSink }

func (s zeroIndexSink) Send(ev openresponses.StreamEvent) error {
	data, err := openresponses.EncodeEvent(ev)
	if err != nil {
		return err
	}
	data = regexp.MustCompile(`"output_index":\d+`).ReplaceAll(data, []byte(`"output_index":0`))
	decoded, err := openresponses.DecodeEvent(data)
	if err != nil {
		return err
	}
	return s.EventSink.Send(decoded)
}
