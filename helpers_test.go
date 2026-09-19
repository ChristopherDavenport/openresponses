package openresponses

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func ptr[T any](v T) *T { return &v }

// sampleResponse is a complete response used by the client and handler
// tests. The conformance module keeps its own copy.
func sampleResponse() *Response {
	strict := true
	resp := NewResponse(Request{
		Model:      "gpt-5",
		Tools:      Tools{&FunctionTool{Name: "lookup", Description: "d", Parameters: json.RawMessage(`{"type":"object"}`), Strict: &strict}},
		ToolChoice: ToolChoice{Mode: ToolChoiceAuto},
		Reasoning:  ReasoningConfig{Effort: ReasoningEffortHigh},
		Metadata:   map[string]string{"k": "v"},
	})
	resp.ID = "resp_1"
	resp.Status = ResponseStatusCompleted
	resp.CompletedAt = ptr[int64](2)
	resp.Usage = &Usage{InputTokens: 1, OutputTokens: 2, TotalTokens: 3}
	resp.Output = Items{
		&Message{ID: "msg_1", Status: StatusCompleted, Role: RoleAssistant, Phase: PhaseFinalAnswer, Content: Contents{&OutputText{Text: "hi"}}},
	}
	return resp
}

// echoAdapter is a small in-package adapter that echoes the last user
// message and honours function_call_output items, enough to exercise
// continuation.
type echoAdapter struct{}

func (echoAdapter) Create(ctx context.Context, req Request) (*Response, error) {
	return CollectStream(ctx, echoAdapter{}, req)
}

func (echoAdapter) Compact(_ context.Context, req CompactRequest) (*CompactResponse, error) {
	if req.PreviousResponseID != "" {
		return nil, PreviousResponseNotFound(req.PreviousResponseID)
	}
	return &CompactResponse{ID: NewID("resp"), Output: Items{&Compaction{ID: NewID("cmp"), EncryptedContent: "x"}}}, nil
}

func (echoAdapter) CreateStream(_ context.Context, req Request, sink EventSink) error {
	resp := NewResponse(req)
	resp.ID = NewID("resp")
	if err := sink.Send(&ResponseCreatedEvent{Response: resp}); err != nil {
		return err
	}
	text := "Hello!"
	for i := len(req.Input) - 1; i >= 0; i-- {
		if m, ok := req.Input[i].(*Message); ok && m.Role == RoleUser {
			text = m.Text()
			break
		}
	}
	msg := &Message{ID: NewID("msg"), Status: StatusCompleted, Role: RoleAssistant, Content: Contents{&OutputText{Text: text}}}
	if err := sink.Send(&OutputItemAddedEvent{Item: msg}); err != nil {
		return err
	}
	if err := sink.Send(&OutputItemDoneEvent{Item: msg}); err != nil {
		return err
	}
	resp.Output = Items{msg}
	resp.Status = ResponseStatusCompleted
	return sink.Send(&ResponseCompletedEvent{Response: resp})
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	return ctx
}
