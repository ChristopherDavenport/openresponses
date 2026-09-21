package chatcompletions

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/ChristopherDavenport/openresponses"
)

func ptr[T any](v T) *T { return &v }

func wantErr(t *testing.T, err error, code, param string) {
	t.Helper()
	var oerr *openresponses.Error
	if !errors.As(err, &oerr) {
		t.Fatalf("err = %v, want *openresponses.Error", err)
	}
	if oerr.Code != code || oerr.Param != param {
		t.Fatalf("err = %s/%s (%s), want %s/%s", oerr.Code, oerr.Param, oerr.Message, code, param)
	}
}

// wire encodes req the way the adapter would and decodes the body.
func wire(t *testing.T, a *Adapter, req openresponses.Request) map[string]any {
	t.Helper()
	b, err := a.encodeRequest(req)
	if err != nil {
		t.Fatalf("encodeRequest: %v", err)
	}
	data, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func testAdapter(opts ...Option) *Adapter {
	return New(openresponses.NewClient("http://example.invalid/v1"), opts...)
}

func messages(body map[string]any) []map[string]any {
	var out []map[string]any
	for _, m := range body["messages"].([]any) {
		out = append(out, m.(map[string]any))
	}
	return out
}

func TestEncodeMessages(t *testing.T) {
	body := wire(t, testAdapter(WithReasoningReplay("reasoning_content")), openresponses.Request{
		Model:        "deepseek-reasoner",
		Instructions: "Be brief.",
		Input: openresponses.Items{
			openresponses.SystemText("You are a pirate."),
			openresponses.DeveloperText("Answer in French."),
			openresponses.UserText("What is the weather?"),
			&openresponses.ReasoningItem{Content: openresponses.Contents{&openresponses.ReasoningText{Text: "Need a lookup."}}},
			openresponses.AssistantText("Let me check."),
			&openresponses.FunctionCall{CallID: "call_1", Name: "weather", Arguments: `{"city":"Oslo"}`},
			&openresponses.FunctionCall{CallID: "call_2", Name: "time"},
			openresponses.NewFunctionCallOutput("call_1", "rainy"),
			openresponses.NewFunctionCallOutput("call_2", "noon"),
			openresponses.UserMessage(&openresponses.InputText{Text: "Thanks"}, &openresponses.InputImage{ImageURL: "https://example.com/cat.png", Detail: openresponses.ImageDetailLow}),
		},
	})
	if body["model"] != "deepseek-reasoner" || body["n"] != float64(1) || body["stream"] != true || body["stream_options"].(map[string]any)["include_usage"] != true {
		t.Fatalf("envelope = %v", body)
	}
	ms := messages(body)
	if len(ms) != 8 {
		t.Fatalf("got %d messages: %v", len(ms), ms)
	}
	want := []string{"system", "system", "developer", "user", "assistant", "tool", "tool", "user"}
	for i, role := range want {
		if ms[i]["role"] != role {
			t.Fatalf("message %d role = %v, want %s", i, ms[i]["role"], role)
		}
	}
	if ms[0]["content"] != "Be brief." || ms[1]["content"] != "You are a pirate." || ms[2]["content"] != "Answer in French." || ms[3]["content"] != "What is the weather?" {
		t.Fatalf("text messages = %v", ms[:4])
	}
	assistant := ms[4]
	if assistant["content"] != "Let me check." || assistant["reasoning_content"] != "Need a lookup." {
		t.Fatalf("assistant = %v", assistant)
	}
	calls := assistant["tool_calls"].([]any)
	first := calls[0].(map[string]any)
	fn := first["function"].(map[string]any)
	if len(calls) != 2 || first["id"] != "call_1" || first["type"] != "function" || fn["name"] != "weather" || fn["arguments"] != `{"city":"Oslo"}` {
		t.Fatalf("tool calls = %v", calls)
	}
	if calls[1].(map[string]any)["function"].(map[string]any)["arguments"] != "{}" {
		t.Fatalf("empty arguments should be {}: %v", calls[1])
	}
	if ms[5]["tool_call_id"] != "call_1" || ms[5]["content"] != "rainy" || ms[6]["tool_call_id"] != "call_2" {
		t.Fatalf("tool messages = %v %v", ms[5], ms[6])
	}
	parts := ms[7]["content"].([]any)
	img := parts[1].(map[string]any)["image_url"].(map[string]any)
	if len(parts) != 2 || parts[0].(map[string]any)["text"] != "Thanks" || img["url"] != "https://example.com/cat.png" || img["detail"] != "low" {
		t.Fatalf("parts = %v", parts)
	}
}

func TestEncodeReasoningWithoutReplay(t *testing.T) {
	body := wire(t, testAdapter(), openresponses.Request{Input: openresponses.Items{
		&openresponses.ReasoningItem{Summary: openresponses.Contents{&openresponses.SummaryText{Text: "thought"}}},
		openresponses.AssistantText("Hi"),
	}})
	m := messages(body)[0]
	if _, ok := m["reasoning_content"]; ok || m["content"] != "Hi" {
		t.Fatalf("reasoning must not be sent without a replay field: %v", m)
	}
}

func TestEncodeAssistantParts(t *testing.T) {
	body := wire(t, testAdapter(), openresponses.Request{Input: openresponses.Items{
		&openresponses.Message{Role: openresponses.RoleAssistant, Content: openresponses.Contents{
			&openresponses.OutputText{Text: "a"}, &openresponses.OutputText{Text: "b"}, &openresponses.Refusal{Refusal: "no"},
		}},
	}})
	m := messages(body)[0]
	parts := m["content"].([]any)
	if len(parts) != 2 || parts[1].(map[string]any)["text"] != "b" || m["refusal"] != "no" {
		t.Fatalf("assistant = %v", m)
	}
}

func TestEncodeConfig(t *testing.T) {
	body := wire(t, testAdapter(WithMaxTokensField("max_completion_tokens"), WithExtra()), openresponses.Request{
		MaxOutputTokens:   ptr(256),
		Temperature:       ptr(0.5),
		TopP:              ptr(0.9),
		PresencePenalty:   ptr(0.1),
		FrequencyPenalty:  ptr(0.2),
		TopLogprobs:       ptr(3),
		ParallelToolCalls: ptr(false),
		Reasoning:         openresponses.ReasoningConfig{Effort: openresponses.ReasoningEffortHigh, Summary: openresponses.ReasoningSummaryAuto},
		Text:              openresponses.TextConfig{Verbosity: openresponses.VerbosityLow, Format: openresponses.JSONSchemaFormat("out", json.RawMessage(`{"type":"object"}`), true)},
		ServiceTier:       openresponses.ServiceTierFlex,
		SafetyIdentifier:  "user-1",
		PromptCacheKey:    "k",
		Metadata:          map[string]string{"team": "a"},
		Extra:             map[string]any{"top_k": 40, "model": "override-attempt"},
	})
	if body["max_completion_tokens"] != float64(256) || body["temperature"] != 0.5 || body["top_p"] != 0.9 || body["presence_penalty"] != 0.1 || body["frequency_penalty"] != 0.2 {
		t.Fatalf("sampling = %v", body)
	}
	if _, ok := body["max_tokens"]; ok {
		t.Fatalf("max_tokens should be renamed: %v", body)
	}
	if body["logprobs"] != true || body["top_logprobs"] != float64(3) || body["parallel_tool_calls"] != false {
		t.Fatalf("logprobs/parallel = %v", body)
	}
	if body["reasoning_effort"] != "high" || body["verbosity"] != "low" || body["service_tier"] != "flex" || body["safety_identifier"] != "user-1" || body["prompt_cache_key"] != "k" {
		t.Fatalf("passthrough fields = %v", body)
	}
	if body["metadata"].(map[string]any)["team"] != "a" {
		t.Fatalf("metadata = %v", body["metadata"])
	}
	rf := body["response_format"].(map[string]any)
	js := rf["json_schema"].(map[string]any)
	if rf["type"] != "json_schema" || js["name"] != "out" || js["strict"] != true || js["schema"].(map[string]any)["type"] != "object" {
		t.Fatalf("response_format = %v", rf)
	}
	if body["top_k"] != float64(40) || body["model"] != "" {
		t.Fatalf("extra must be forwarded without overriding mapped keys: %v", body)
	}

	body = wire(t, testAdapter(), openresponses.Request{Extra: map[string]any{"top_k": 40}, Text: openresponses.TextConfig{Format: &openresponses.TextFormat{Type: openresponses.TextFormatJSONObject}}})
	if _, ok := body["top_k"]; ok {
		t.Fatalf("extra must not be forwarded by default: %v", body)
	}
	if body["response_format"].(map[string]any)["type"] != "json_object" {
		t.Fatalf("json_object = %v", body["response_format"])
	}

	body = wire(t, testAdapter(), openresponses.Request{MaxOutputTokens: ptr(10)})
	if body["max_tokens"] != float64(10) {
		t.Fatalf("default field = %v", body)
	}
	for _, key := range []string{"tools", "tool_choice", "temperature", "response_format", "reasoning_effort", "metadata"} {
		if _, ok := body[key]; ok {
			t.Fatalf("empty request should not send %s: %v", key, body)
		}
	}
}

func TestEncodeToolsAndChoice(t *testing.T) {
	body := wire(t, testAdapter(), openresponses.Request{
		Tools: openresponses.Tools{
			openresponses.NewFunctionTool("weather", "Look up weather", json.RawMessage(`{"type":"object"}`)),
			&openresponses.FunctionTool{Name: "time", Strict: ptr(true)},
		},
		ToolChoice: openresponses.ToolChoice{Allowed: &openresponses.AllowedTools{Mode: openresponses.ToolChoiceRequired, Tools: []openresponses.ToolReference{openresponses.FunctionReference("weather")}}},
	})
	tools := body["tools"].([]any)
	w := tools[0].(map[string]any)["function"].(map[string]any)
	if len(tools) != 2 || w["name"] != "weather" || w["description"] != "Look up weather" || w["parameters"].(map[string]any)["type"] != "object" {
		t.Fatalf("tools = %v", tools)
	}
	tm := tools[1].(map[string]any)["function"].(map[string]any)
	if tm["strict"] != true {
		t.Fatalf("strict = %v", tm)
	}
	if _, ok := tm["parameters"]; ok {
		t.Fatalf("nil parameters should be omitted: %v", tm)
	}
	tc := body["tool_choice"].(map[string]any)
	allowed := tc["allowed_tools"].(map[string]any)
	if tc["type"] != "allowed_tools" || allowed["mode"] != "required" || allowed["tools"].([]any)[0].(map[string]any)["function"].(map[string]any)["name"] != "weather" {
		t.Fatalf("tool_choice = %v", tc)
	}

	for _, tc := range []struct {
		choice openresponses.ToolChoice
		want   any
	}{
		{openresponses.ToolChoice{Mode: openresponses.ToolChoiceAuto}, "auto"},
		{openresponses.ToolChoice{Mode: openresponses.ToolChoiceNone}, "none"},
		{openresponses.ToolChoice{Mode: openresponses.ToolChoiceRequired}, "required"},
	} {
		if got := wire(t, testAdapter(), openresponses.Request{ToolChoice: tc.choice})["tool_choice"]; got != tc.want {
			t.Fatalf("tool_choice = %v, want %v", got, tc.want)
		}
	}
	fn := wire(t, testAdapter(), openresponses.Request{ToolChoice: openresponses.ToolChoiceFunction("weather")})["tool_choice"].(map[string]any)
	if fn["type"] != "function" || fn["function"].(map[string]any)["name"] != "weather" {
		t.Fatalf("function choice = %v", fn)
	}
}

func TestEncodeFiles(t *testing.T) {
	body := wire(t, testAdapter(), openresponses.Request{Input: openresponses.Items{openresponses.UserMessage(
		&openresponses.InputFile{Filename: "a.pdf", FileData: "data:application/pdf;base64,aGk="},
		&openresponses.InputFile{FileID: "file_1"},
	)}})
	parts := messages(body)[0]["content"].([]any)
	f0 := parts[0].(map[string]any)["file"].(map[string]any)
	f1 := parts[1].(map[string]any)["file"].(map[string]any)
	if parts[0].(map[string]any)["type"] != "file" || f0["filename"] != "a.pdf" || f0["file_data"] != "data:application/pdf;base64,aGk=" || f1["file_id"] != "file_1" {
		t.Fatalf("files = %v", parts)
	}
}

func TestEncodeErrors(t *testing.T) {
	cases := []struct {
		name        string
		req         openresponses.Request
		code, param string
	}{
		{"max_tool_calls", openresponses.Request{MaxToolCalls: ptr(2)}, openresponses.CodeUnsupportedParameter, "max_tool_calls"},
		{"truncation", openresponses.Request{Truncation: openresponses.TruncationAuto}, openresponses.CodeUnsupportedParameter, "truncation"},
		{"format type", openresponses.Request{Text: openresponses.TextConfig{Format: &openresponses.TextFormat{Type: "yaml"}}}, openresponses.CodeInvalidValue, "text.format.type"},
		{"bad schema", openresponses.Request{Text: openresponses.TextConfig{Format: openresponses.JSONSchemaFormat("o", json.RawMessage(`{`), false)}}, openresponses.CodeInvalidValue, "text.format.schema"},
		{"foreign tool", openresponses.Request{Tools: openresponses.Tools{&openresponses.UnknownTool{Type: "anthropic.web_search_20260209"}}}, openresponses.CodeUnsupportedParameter, "tools[0].type"},
		{"bad parameters", openresponses.Request{Tools: openresponses.Tools{openresponses.NewFunctionTool("f", "", json.RawMessage(`{`))}}, openresponses.CodeInvalidValue, "tools[0].parameters"},
		{"video", openresponses.Request{Input: openresponses.Items{openresponses.UserMessage(&openresponses.InputVideo{VideoURL: "https://x/v.mp4"})}}, openresponses.CodeUnsupportedParameter, "input[0].content[0].type"},
		{"image file_id", openresponses.Request{Input: openresponses.Items{openresponses.UserMessage(&openresponses.InputImage{FileID: "f"})}}, openresponses.CodeUnsupportedParameter, "input[0].content[0].file_id"},
		{"file_url", openresponses.Request{Input: openresponses.Items{openresponses.UserMessage(&openresponses.InputFile{FileURL: "https://x/a.pdf"})}}, openresponses.CodeUnsupportedParameter, "input[0].content[0].file_url"},
		{"system image", openresponses.Request{Input: openresponses.Items{&openresponses.Message{Role: openresponses.RoleSystem, Content: openresponses.Contents{&openresponses.InputImage{ImageURL: "https://x/a.png"}}}}}, openresponses.CodeUnsupportedParameter, "input[0].content[0].type"},
		{"tool output image", openresponses.Request{Input: openresponses.Items{&openresponses.FunctionCallOutput{CallID: "c", Output: openresponses.FunctionCallOutputData{Parts: openresponses.Contents{&openresponses.InputImage{ImageURL: "https://x/a.png"}}}}}}, openresponses.CodeUnsupportedParameter, "input[0].output[0].type"},
		{"compaction", openresponses.Request{Input: openresponses.Items{&openresponses.Compaction{EncryptedContent: "x"}}}, openresponses.CodeUnsupportedParameter, "input[0].type"},
		{"extension item", openresponses.Request{Input: openresponses.Items{&openresponses.UnknownItem{Type: "gemini.executable_code"}}}, openresponses.CodeUnsupportedParameter, "input[0].type"},
		{"previous response", openresponses.Request{PreviousResponseID: "resp_1"}, openresponses.CodePreviousResponseNotFound, "previous_response_id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := testAdapter().encodeRequest(tc.req)
			wantErr(t, err, tc.code, tc.param)
		})
	}
}
