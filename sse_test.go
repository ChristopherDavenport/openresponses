package openresponses

import (
	"context"
	"errors"
	"io"
	"os"
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
