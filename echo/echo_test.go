package echo

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ChristopherDavenport/openresponses"
	"github.com/ChristopherDavenport/openresponses/streamtest"
)

func TestEchoMessage(t *testing.T) {
	a := &Adapter{}
	resp, err := a.Create(context.Background(), openresponses.Request{Model: "m", Input: openresponses.Items{
		openresponses.SystemText("be a pirate"),
		openresponses.UserText("say hello"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.OutputText() != "say hello" || resp.Status != openresponses.ResponseStatusCompleted {
		t.Errorf("resp = %+v", resp)
	}
	msg := resp.Output[0].(*openresponses.Message)
	if msg.Phase != openresponses.PhaseFinalAnswer || msg.ID == "" || msg.Status != openresponses.StatusCompleted {
		t.Errorf("message = %+v", msg)
	}
	if resp.Usage == nil || resp.Usage.TotalTokens == 0 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestEchoToolCall(t *testing.T) {
	a := &Adapter{}
	tool := openresponses.NewFunctionTool("get_weather", "", json.RawMessage(`{"type":"object","required":["location"]}`))
	req := openresponses.Request{Model: "m", Tools: openresponses.Tools{tool}, Input: openresponses.Items{openresponses.UserText("SF")}}
	resp, err := a.Create(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	calls := resp.FunctionCalls()
	if len(calls) != 1 || calls[0].Name != "get_weather" || calls[0].Arguments != `{"location":"SF"}` {
		t.Fatalf("calls = %+v", calls)
	}
	// Feeding the result back yields a message, not another call.
	req.Input = append(req.Input, calls[0], openresponses.NewFunctionCallOutput(calls[0].CallID, "sunny"))
	resp, err = a.Create(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.OutputText() != "Tool result: sunny" {
		t.Errorf("text = %q", resp.OutputText())
	}
	// tool_choice none suppresses calls.
	req.Input = openresponses.Items{openresponses.UserText("hi")}
	req.ToolChoice = openresponses.ToolChoice{Mode: openresponses.ToolChoiceNone}
	resp, err = a.Create(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.FunctionCalls()) != 0 {
		t.Error("tool_choice none ignored")
	}
}

func TestEchoCompactRoundTrip(t *testing.T) {
	a := &Adapter{}
	compact, err := a.Compact(context.Background(), openresponses.CompactRequest{Model: "m", Input: openresponses.Items{
		openresponses.UserText("code word: slate"),
		openresponses.AssistantText("OK"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if compact.Object != openresponses.ObjectCompaction || len(compact.Output) != 1 {
		t.Fatalf("compact = %+v", compact)
	}
	resp, err := a.Create(context.Background(), openresponses.Request{Model: "m", Input: append(compact.Output, openresponses.UserText("what was it?"))})
	if err != nil {
		t.Fatal(err)
	}
	if resp.OutputText() != "what was it?" {
		t.Errorf("text = %q", resp.OutputText())
	}
	_, err = a.Create(context.Background(), openresponses.Request{Model: "m", Input: openresponses.Items{&openresponses.Compaction{EncryptedContent: "not ours"}}})
	if !openresponses.IsInvalidRequest(err) {
		t.Errorf("foreign compaction: %v", err)
	}
}

func TestEchoStreamIsWellOrdered(t *testing.T) {
	a := &Adapter{}
	for name, req := range map[string]openresponses.Request{
		"message": {Model: "m", Input: openresponses.Items{openresponses.UserText("hi there")}},
		"tool":    {Model: "m", Input: openresponses.Items{openresponses.UserText("SF")}, Tools: openresponses.Tools{openresponses.NewFunctionTool("f", "", nil)}},
	} {
		t.Run(name, func(t *testing.T) {
			sink, err := streamtest.Run(context.Background(), a, req)
			if err != nil {
				t.Fatal(err)
			}
			if sink.Response().Usage == nil {
				t.Error("usage missing")
			}
		})
	}
}

func TestEchoCompactRejectsPreviousResponseID(t *testing.T) {
	_, err := (&Adapter{}).Compact(context.Background(), openresponses.CompactRequest{Model: "m", PreviousResponseID: "resp_x"})
	if !openresponses.IsNotFound(err) {
		t.Errorf("err = %v", err)
	}
}
