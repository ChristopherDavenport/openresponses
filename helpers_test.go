package openresponses

import "encoding/json"

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
