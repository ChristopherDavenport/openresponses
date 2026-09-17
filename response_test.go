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
		Reasoning:  ReasoningConfig{Effort: ReasoningEffortLow},
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

func TestResponseKeepsAbsentKeys(t *testing.T) {
	src := `{"id":"r","object":"response","status":"completed","output":[],"model":"m","created_at":1}`
	var resp Response
	if err := json.Unmarshal([]byte(src), &resp); err != nil {
		t.Fatal(err)
	}
	keys := func(r *Response) map[string]any {
		t.Helper()
		out, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	m := keys(&resp)
	for _, k := range []string{"temperature", "top_p", "store", "parallel_tool_calls", "tools", "metadata", "usage"} {
		if _, ok := m[k]; ok {
			t.Errorf("re-encoding invented %q: %v", k, m[k])
		}
	}
	for _, k := range []string{"id", "object", "status", "output", "model", "created_at"} {
		if _, ok := m[k]; !ok {
			t.Errorf("re-encoding dropped %q", k)
		}
	}
	// Setting a field brings it back; a clone remembers what was absent.
	resp.Temperature = 0.5
	resp.Store = true
	m = keys(resp.Clone())
	if m["temperature"] != 0.5 || m["store"] != true {
		t.Errorf("set fields missing: %v", m)
	}
	if _, ok := m["top_p"]; ok {
		t.Errorf("clone forgot absent keys: %v", m)
	}
	// A response built in Go emits the full resource.
	m = keys(NewResponse(Request{Model: "m"}))
	for _, k := range []string{"temperature", "top_p", "store", "tools", "metadata", "usage"} {
		if _, ok := m[k]; !ok {
			t.Errorf("NewResponse output lacks %q", k)
		}
	}
}

func TestUnknownEventRenumbers(t *testing.T) {
	raw := `{"type":"acme:thing","sequence_number":9,"foo":"bar"}`
	ev, err := DecodeEvent([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	u, ok := ev.(*UnknownEvent)
	if !ok {
		t.Fatalf("got %T", ev)
	}
	var _ SequenceSetter = u
	out, err := EncodeEvent(u)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != raw {
		t.Errorf("untouched event re-encoded as %s", out)
	}
	u.SetSequence(3)
	out, err = EncodeEvent(u)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if m["sequence_number"] != float64(3) || m["foo"] != "bar" || m["type"] != "acme:thing" {
		t.Errorf("renumbered event = %s", out)
	}
	back, err := DecodeEvent(out)
	if err != nil {
		t.Fatal(err)
	}
	if back.Sequence() != 3 {
		t.Errorf("decoded sequence %d", back.Sequence())
	}
}

func TestErrorPayloadHeadersFiltered(t *testing.T) {
	p := ErrorPayload{Type: ErrorTypeTooManyRequests, Code: "rl", Message: "slow down", Headers: map[string]string{
		"Retry-After":                 "3",
		"RateLimit-Remaining":         "0",
		"Set-Cookie":                  "a=b",
		"Access-Control-Allow-Origin": "*",
	}}
	e := p.Err(429)
	if e.Headers.Get("Retry-After") != "3" || e.Headers.Get("RateLimit-Remaining") != "0" {
		t.Errorf("error headers dropped: %v", e.Headers)
	}
	if e.Headers.Get("Set-Cookie") != "" || e.Headers.Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("non-error headers carried over: %v", e.Headers)
	}
}
