package openresponses

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// assertSameJSON compares two JSON documents structurally.
func assertSameJSON(t *testing.T, want, got []byte) {
	t.Helper()
	var w, g any
	if err := json.Unmarshal(want, &w); err != nil {
		t.Fatalf("want is not JSON: %v", err)
	}
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("got is not JSON: %v\n%s", err, got)
	}
	if !reflect.DeepEqual(w, g) {
		t.Errorf("round trip changed the document\nwant: %s\ngot:  %s", want, got)
	}
}

func TestRoundTripRequest(t *testing.T) {
	data, err := os.ReadFile("testdata/golden/request.json")
	if err != nil {
		t.Fatal(err)
	}
	var req Request
	if err := json.Unmarshal(data, &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// The golden uses string content for two messages; they become one
	// part each, which is the same document semantically. Normalise the
	// expectation to the array form before comparing.
	want := []byte(string(data))
	want = replaceJSON(t, want, `"content": "Be brief."`, `"content": [{"type":"input_text","text":"Be brief."}]`)

	if got := req.Extra["acme:priority"]; got != float64(7) {
		t.Errorf("extra acme:priority = %v", got)
	}
	if _, ok := req.Input[8].(*ItemReference); !ok {
		t.Errorf("input[8] = %T, want *ItemReference", req.Input[8])
	}
	u, ok := req.Input[9].(*UnknownItem)
	if !ok || u.Type != "acme:search_result" || u.ID != "sr_1" || u.Status != StatusCompleted {
		t.Errorf("input[9] = %#v", req.Input[9])
	}
	if _, ok := req.Tools[1].(*UnknownTool); !ok {
		t.Errorf("tools[1] = %T", req.Tools[1])
	}
	if req.ToolChoice.Allowed == nil || req.ToolChoice.Allowed.Mode != ToolChoiceAuto {
		t.Errorf("tool_choice = %+v", req.ToolChoice)
	}
	if fco, ok := req.Input[5].(*FunctionCallOutput); !ok || fco.Output.Parts == nil || fco.Output.String() != "still a cat" {
		t.Errorf("input[5] = %#v", req.Input[5])
	}
	out, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	assertSameJSON(t, want, out)
	// The golden carries extension items and tools on purpose, so it is
	// checked for lossless round-tripping only; TestSchemaRequest covers
	// schema validity of standard shapes.
}

func TestRoundTripResponse(t *testing.T) {
	data, err := os.ReadFile("testdata/golden/response.json")
	if err != nil {
		t.Fatal(err)
	}
	var resp Response
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.OutputText() != "The answer is four." {
		t.Errorf("OutputText = %q", resp.OutputText())
	}
	if calls := resp.FunctionCalls(); len(calls) != 1 || calls[0].Name != "lookup" {
		t.Errorf("FunctionCalls = %+v", calls)
	}
	if resp.Extra["openrouter:provider"] != "acme" {
		t.Errorf("extra = %v", resp.Extra)
	}
	msg := resp.Output[1].(*Message)
	text := msg.Content[0].(*OutputText)
	if _, ok := text.Annotations[1].(*UnknownAnnotation); !ok {
		t.Errorf("annotation[1] = %T", text.Annotations[1])
	}
	if _, ok := msg.Content[2].(*UnknownContent); !ok {
		t.Errorf("content[2] = %T", msg.Content[2])
	}
	if _, ok := resp.Output[5].(*UnknownItem); !ok {
		t.Errorf("output[5] = %T", resp.Output[5])
	}
	if resp.Reasoning.Effort != ReasoningEffortHigh || resp.Reasoning.Summary != "" {
		t.Errorf("reasoning = %+v", resp.Reasoning)
	}
	out, err := json.Marshal(resp)
	if err != nil {
		t.Fatal(err)
	}
	// The golden emits "description": null; this package emits "" for an
	// unset description. Both satisfy the schema.
	want := replaceJSON(t, data, `"description": null`, `"description": ""`)
	assertSameJSON(t, want, out)
}

func TestRoundTripUnknownItemMarshalsRaw(t *testing.T) {
	raw := `{"type":"acme:thing","id":"t_1","status":"completed","nested":{"a":[1,2,{"b":null}]}}`
	item, err := UnmarshalItem([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	assertSameJSON(t, []byte(raw), out)
	// Inside a slice too.
	out, err = json.Marshal(Items{item})
	if err != nil {
		t.Fatal(err)
	}
	assertSameJSON(t, []byte("["+raw+"]"), out)
}

func TestInputUnmarshalString(t *testing.T) {
	var req Request
	if err := json.Unmarshal([]byte(`{"model":"m","input":"hello"}`), &req); err != nil {
		t.Fatal(err)
	}
	if len(req.Input) != 1 {
		t.Fatalf("input = %+v", req.Input)
	}
	m, ok := req.Input[0].(*Message)
	if !ok || m.Role != RoleUser || m.Text() != "hello" {
		t.Errorf("input[0] = %#v", req.Input[0])
	}
}

func TestMessageStringContentByRole(t *testing.T) {
	tests := []struct {
		role Role
		want string
	}{
		{RoleUser, ContentTypeInputText},
		{RoleSystem, ContentTypeInputText},
		{RoleDeveloper, ContentTypeInputText},
		{RoleAssistant, ContentTypeOutputText},
	}
	for _, tt := range tests {
		t.Run(string(tt.role), func(t *testing.T) {
			var m Message
			if err := json.Unmarshal([]byte(`{"type":"message","role":"`+string(tt.role)+`","content":"x"}`), &m); err != nil {
				t.Fatal(err)
			}
			if len(m.Content) != 1 || m.Content[0].ContentType() != tt.want {
				t.Errorf("content = %#v", m.Content)
			}
		})
	}
}

func TestRegistryExtension(t *testing.T) {
	type searchResult struct {
		ID    string   `json:"id"`
		Hits  []string `json:"hits"`
		Extra string   `json:"extra,omitempty"`
	}
	RegisterItem("test:search_result", func(raw json.RawMessage) (Item, error) {
		var v struct {
			searchResult
			Status Status `json:"status"`
		}
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
		return &UnknownItem{Type: "test:search_result", ID: v.ID, Status: v.Status, Raw: raw}, nil
	})
	item, err := UnmarshalItem([]byte(`{"type":"test:search_result","id":"x","status":"completed","hits":["a"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if item.ItemType() != "test:search_result" {
		t.Errorf("type = %s", item.ItemType())
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name  string
		req   Request
		param string
	}{
		{"missing model", Request{Input: Items{UserText("x")}}, "model"},
		{"assistant with input_text", Request{Model: "m", Input: Items{&Message{Role: RoleAssistant, Content: Contents{&InputText{Text: "x"}}}}}, "input[0].content[0]"},
		{"user with output_text", Request{Model: "m", Input: Items{&Message{Role: RoleUser, Content: Contents{&OutputText{Text: "x"}}}}}, "input[0].content[0]"},
		{"bad phase", Request{Model: "m", Input: Items{&Message{Role: RoleAssistant, Phase: "weird"}}}, "input[0].phase"},
		{"missing role", Request{Model: "m", Input: Items{&Message{}}}, "input[0].role"},
		{"function_call no name", Request{Model: "m", Input: Items{&FunctionCall{CallID: "c"}}}, "input[0].name"},
		{"function_call_output no call_id", Request{Model: "m", Input: Items{&FunctionCallOutput{}}}, "input[0].call_id"},
		{"compaction empty", Request{Model: "m", Input: Items{&Compaction{}}}, "input[0].encrypted_content"},
		{"tool without name", Request{Model: "m", Tools: Tools{&FunctionTool{}}}, "tools[0].name"},
		{"json_schema without name", Request{Model: "m", Text: TextConfig{Format: &TextFormat{Type: TextFormatJSONSchema}}}, "text.format.name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.req.Validate()
			if err == nil {
				t.Fatal("expected error")
			}
			e := AsError(err)
			if e.Type != ErrorTypeInvalidRequest || e.Param != tt.param {
				t.Errorf("got %v (param %q), want param %q", err, e.Param, tt.param)
			}
		})
	}
	ok := Request{Model: "m", Input: Items{
		SystemText("s"), UserText("u"), AssistantText("a"),
		&Message{Role: RoleAssistant, Phase: PhaseCommentary, Content: Contents{&Refusal{Refusal: "r"}}},
		&FunctionCall{CallID: "c", Name: "f"}, NewFunctionCallOutput("c", "o"),
		&ReasoningItem{}, &Compaction{EncryptedContent: "e"}, &ItemReference{ID: "i"},
		&UnknownItem{Type: "acme:x"},
	}}
	if err := ok.Validate(); err != nil {
		t.Errorf("valid request rejected: %v", err)
	}
}

func TestErrorHelpers(t *testing.T) {
	notFound := &Error{Type: ErrorTypeNotFound, Code: CodePreviousResponseNotFound, Message: "x"}
	if !IsNotFound(notFound) || IsRateLimited(notFound) || IsInvalidRequest(notFound) {
		t.Error("type matching failed")
	}
	if notFound.HTTPStatus() != 404 {
		t.Errorf("status = %d", notFound.HTTPStatus())
	}
	wrapped := AsError(&Error{StatusCode: 429, Type: ErrorTypeTooManyRequests})
	if !IsRateLimited(wrapped) {
		t.Error("rate limit not matched")
	}
	plain := AsError(os.ErrNotExist)
	if plain.Type != ErrorTypeServerError || plain.HTTPStatus() != 500 {
		t.Errorf("plain error mapped to %+v", plain)
	}
	var payload ErrorPayload
	if err := json.Unmarshal([]byte(`{"type":"invalid_request","code":null,"message":"m","param":null}`), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Code != "" || payload.Param != "" || payload.Message != "m" {
		t.Errorf("payload = %+v", payload)
	}
}

// replaceJSON substitutes a literal snippet in a golden file, failing
// when the snippet is absent so the test does not silently drift.
func replaceJSON(t *testing.T, data []byte, old, new string) []byte {
	t.Helper()
	s := string(data)
	if !contains(s, old) {
		t.Fatalf("golden does not contain %q", old)
	}
	return []byte(replaceAll(s, old, new))
}
