package openresponses

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
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

	if got := req.Extra["acme:priority"]; got != json.Number("7") {
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
	// Unknown members are kept at every depth, not only the top level.
	if r, ok := req.Input[6].(*ReasoningItem); !ok || r.Extra["openresponses-experimental.issuer"] != "anthropic" {
		t.Errorf("input[6] = %#v", req.Input[6])
	}
	if p := req.Input[1].(*Message).Content[0].(*InputText); p.Extra["acme:lang"] != "en" {
		t.Errorf("input[1].content[0] = %#v", p)
	}
	if req.Reasoning.Extra["openresponses-experimental.effort"] != "max" {
		t.Errorf("reasoning = %#v", req.Reasoning)
	}
	if req.ToolChoice.Allowed.Extra["acme:sticky"] != true || req.ToolChoice.Allowed.Tools[0].Extra["acme:weight"] != json.Number("1") {
		t.Errorf("tool_choice = %#v", req.ToolChoice.Allowed)
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
	if got := resp.Usage.InputTokensDetails.Extra["openresponses-experimental.cache_write_tokens"]; got != json.Number("2100") {
		t.Errorf("usage.input_tokens_details = %#v", resp.Usage.InputTokensDetails)
	}
	if r := resp.Output[0].(*ReasoningItem); r.Extra["openresponses-experimental.issuer"] != "anthropic" {
		t.Errorf("output[0] = %#v", r)
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

// TestUnknownMembersKept decodes each spec object with a member the spec
// does not define and checks that encoding writes it back unchanged
// (docs/extensions.md, section 1.3).
func TestUnknownMembersKept(t *testing.T) {
	tests := []struct {
		name   string
		raw    string
		decode func([]byte) (any, error)
	}{
		{"message", `{"type":"message","role":"user","content":[{"type":"input_text","text":"x"}],"x:a":1}`, anyItem},
		{"function_call", `{"type":"function_call","call_id":"c","name":"f","arguments":"{}","x:a":{"b":[1]}}`, anyItem},
		{"function_call_output", `{"type":"function_call_output","call_id":"c","output":"o","x:a":true}`, anyItem},
		{"reasoning", `{"type":"reasoning","summary":[],"x:a":"v"}`, anyItem},
		{"compaction", `{"type":"compaction","encrypted_content":"e","x:a":null}`, anyItem},
		{"item_reference", `{"type":"item_reference","id":"i","x:a":1}`, anyItem},
		{"input_text", `{"type":"input_text","text":"x","x:a":1}`, anyContent},
		{"input_image", `{"type":"input_image","image_url":"u","detail":"auto","x:a":1}`, anyContent},
		{"input_file", `{"type":"input_file","file_url":"u","x:a":1}`, anyContent},
		{"input_video", `{"type":"input_video","video_url":"u","x:a":1}`, anyContent},
		{"output_text", `{"type":"output_text","text":"x","annotations":[],"x:a":1}`, anyContent},
		{"refusal", `{"type":"refusal","refusal":"no","x:a":1}`, anyContent},
		{"text", `{"type":"text","text":"x","x:a":1}`, anyContent},
		{"summary_text", `{"type":"summary_text","text":"x","x:a":1}`, anyContent},
		{"reasoning_text", `{"type":"reasoning_text","text":"x","x:a":1}`, anyContent},
		{"url_citation", `{"type":"url_citation","url":"u","title":"t","start_index":0,"end_index":1,"x:a":1}`, func(b []byte) (any, error) { return UnmarshalAnnotation(b) }},
		{"function tool", `{"type":"function","name":"f","description":"","parameters":null,"x:a":1}`, func(b []byte) (any, error) { return UnmarshalTool(b) }},
		{"tool_choice function", `{"type":"function","name":"f","x:a":1}`, into[ToolChoice]},
		{"text config", `{"format":{"type":"text","x:b":2},"x:a":1}`, into[TextConfig]},
		{"reasoning config", `{"effort":"high","summary":null,"x:a":1}`, into[ReasoningConfig]},
		{"stream_options", `{"include_obfuscation":true,"x:a":1}`, into[StreamOptions]},
		{"incomplete_details", `{"reason":"max_output_tokens","x:a":1}`, into[IncompleteDetails]},
		{"usage", `{"input_tokens":1,"output_tokens":2,"total_tokens":3,"input_tokens_details":{"cached_tokens":0,"x:b":2},"output_tokens_details":{"reasoning_tokens":0,"x:c":3},"x:a":1}`, into[Usage]},
		{"logprob", `{"token":"t","logprob":0,"bytes":[],"top_logprobs":[{"token":"t","logprob":0,"bytes":[],"x:b":2}],"x:a":1}`, into[LogProb]},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			v, err := tt.decode([]byte(tt.raw))
			if err != nil {
				t.Fatal(err)
			}
			out, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			assertSameJSON(t, []byte(tt.raw), out)
		})
	}
}

func anyItem(b []byte) (any, error)    { return UnmarshalItem(b) }
func anyContent(b []byte) (any, error) { return UnmarshalContent(b) }

func into[T any](b []byte) (any, error) {
	v := new(T)
	err := json.Unmarshal(b, v)
	return v, err
}

func TestNewResponseLeavesReasoningExtensionsOut(t *testing.T) {
	req := Request{Model: "m", Reasoning: ReasoningConfig{Effort: ReasoningEffortXHigh, Extra: map[string]any{"openresponses-experimental.effort": "max"}}}
	resp := NewResponse(req)
	if resp.Reasoning.Effort != ReasoningEffortXHigh || resp.Reasoning.Extra != nil {
		t.Errorf("reasoning = %#v", resp.Reasoning)
	}
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

func TestValidateFunctionCallOutputs(t *testing.T) {
	output := NewFunctionCallOutput("c1", "x")
	cases := []struct {
		name string
		req  Request
		ok   bool
	}{
		{"orphan", Request{Model: "m", Input: Items{output}}, false},
		{"paired", Request{Model: "m", Input: Items{&FunctionCall{CallID: "c1", Name: "f"}, output}}, true},
		{"continued", Request{Model: "m", PreviousResponseID: "resp_1", Input: Items{output}}, true},
		{"after compaction", Request{Model: "m", Input: Items{&Compaction{EncryptedContent: "e"}, output}}, true},
		{"after item_reference", Request{Model: "m", Input: Items{&ItemReference{ID: "fc_1"}, output}}, true},
		{"after extension item", Request{Model: "m", Input: Items{&UnknownItem{Type: "acme:call"}, output}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.req.Validate()
			if tc.ok && err != nil {
				t.Fatalf("unexpected %v", err)
			}
			if !tc.ok {
				e := AsError(err)
				if !IsInvalidRequest(err) || e.Param != "input[0].call_id" {
					t.Fatalf("got %v", err)
				}
			}
		})
	}
	if err := (CompactRequest{Model: "m", Input: Items{output}}).Validate(); !IsInvalidRequest(err) {
		t.Errorf("compact request with orphan output: %v", err)
	}
}

// TestExtraIsExact checks the three ways Extra could change a document:
// a number losing precision, a key in another case filling a field, and
// Extra setting a member the type declares.
func TestExtraIsExact(t *testing.T) {
	raw := `{"type":"function_call","call_id":"a","Call_Id":"b","name":"f","arguments":"{}","acme.seq":9007199254740993,"acme.f":1.0e400}`
	item, err := UnmarshalItem([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if CallID(item) != "a" {
		t.Errorf("call_id = %q", CallID(item))
	}
	out, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"acme.seq":9007199254740993`, `"acme.f":1.0e400`, `"Call_Id":"b"`, `"call_id":"a"`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("%s lost %s", out, want)
		}
	}
	out, err = json.Marshal(&FunctionCall{CallID: "c", Name: "f", Extra: map[string]any{"id": "fc_x"}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "fc_x") {
		t.Errorf("Extra set a declared member: %s", out)
	}
}

// TestRawValuesKeptByteForByte checks that a raw value the root keeps
// (an unknown item, function tool parameters) reaches its field with the
// bytes it arrived with: no compaction, no HTML escaping.
func TestRawValuesKeptByteForByte(t *testing.T) {
	item := `{"type":"acme.x", "note":"a<b&c",  "n": [1, 2]}`
	params := `{ "description" : "x<y & z>w" }`
	tools := `[{"type":"function","name":"f","description":"","parameters":` + params + `}]`

	var resp Response
	if err := json.Unmarshal([]byte(`{"id":"r","output":[`+item+`],"tools":`+tools+`}`), &resp); err != nil {
		t.Fatal(err)
	}
	if got := string(resp.Output[0].(*UnknownItem).Raw); got != item {
		t.Errorf("response output raw = %s, want %s", got, item)
	}
	if got := string(resp.Tools[0].(*FunctionTool).Parameters); got != params {
		t.Errorf("response tool parameters = %s, want %s", got, params)
	}

	var req Request
	if err := json.Unmarshal([]byte(`{"model":"m","input":[`+item+`],"tools":`+tools+`}`), &req); err != nil {
		t.Fatal(err)
	}
	if got := string(req.Input[0].(*UnknownItem).Raw); got != item {
		t.Errorf("request input raw = %s, want %s", got, item)
	}
	if got := string(req.Tools[0].(*FunctionTool).Parameters); got != params {
		t.Errorf("request tool parameters = %s, want %s", got, params)
	}
}
