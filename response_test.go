package openresponses

import (
	"encoding/json"
	"testing"
)

func TestNewResponseDoesNotMutateRequest(t *testing.T) {
	tool := NewFunctionTool("f", "d", json.RawMessage(`{"type":"object"}`))
	req := Request{
		Model:      "m",
		Tools:      Tools{tool},
		ToolChoice: ToolChoiceFunction("f"),
		Text:       TextConfig{Format: JSONSchemaFormat("out", json.RawMessage(`{}`), true)},
		Reasoning:  &ReasoningConfig{Effort: ReasoningEffortLow},
		Metadata:   map[string]string{"k": "v"},
	}
	resp := NewResponse(req)

	if tool.Strict != nil {
		t.Error("NewResponse set Strict on the caller's tool")
	}
	got := resp.Tools[0].(*FunctionTool)
	if got.Strict == nil || *got.Strict {
		t.Errorf("response tool strict = %v", got.Strict)
	}
	resp.Metadata["k"] = "changed"
	resp.Reasoning.Effort = ReasoningEffortHigh
	resp.ToolChoice.Function.Name = "changed"
	resp.Text.Format.Name = "changed"
	if req.Metadata["k"] != "v" || req.Reasoning.Effort != ReasoningEffortLow ||
		req.ToolChoice.Function.Name != "f" || req.Text.Format.Name != "out" {
		t.Errorf("request mutated through the response: %+v", req)
	}
}

func TestItemsUnmarshalString(t *testing.T) {
	var items Items
	if err := json.Unmarshal([]byte(`"hello"`), &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].(*Message).Text() != "hello" {
		t.Errorf("items = %+v", items)
	}
	if err := json.Unmarshal([]byte(`null`), &items); err != nil || items != nil {
		t.Errorf("null: %v %v", err, items)
	}
}

func TestErrorConstructors(t *testing.T) {
	tests := []struct {
		err    *Error
		typ    ErrorType
		status int
		param  string
	}{
		{InvalidRequest("c", "m", "p"), ErrorTypeInvalidRequest, 400, "p"},
		{NotFound("c", "m", "p"), ErrorTypeNotFound, 404, "p"},
		{PreviousResponseNotFound("resp_1"), ErrorTypeNotFound, 404, "previous_response_id"},
		{TooManyRequests("c", "m"), ErrorTypeTooManyRequests, 429, ""},
		{ModelError("c", "m"), ErrorTypeModelError, 500, ""},
		{ServerError("c", "m"), ErrorTypeServerError, 500, ""},
	}
	for _, tt := range tests {
		t.Run(string(tt.typ), func(t *testing.T) {
			if tt.err.Type != tt.typ || tt.err.HTTPStatus() != tt.status || tt.err.Param != tt.param {
				t.Errorf("got %+v", tt.err)
			}
		})
	}
	if PreviousResponseNotFound("resp_1").Code != CodePreviousResponseNotFound {
		t.Error("wrong code")
	}
}
