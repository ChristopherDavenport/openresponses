package anthropic

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ChristopherDavenport/openresponses"
	sdk "github.com/anthropics/anthropic-sdk-go"
)

func ptr[T any](v T) *T { return &v }

func errorsAs(err error, target **openresponses.Error) bool { return errors.As(err, target) }

// wantErr asserts err is an *openresponses.Error with the given code and
// param.
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

func encode(t *testing.T, req openresponses.Request) sdk.MessageNewParams {
	t.Helper()
	p, err := encodeRequest(req, DefaultMaxTokens)
	if err != nil {
		t.Fatalf("encodeRequest: %v", err)
	}
	return p
}

// wire returns the request body the SDK would send.
func wire(t *testing.T, p sdk.MessageNewParams) map[string]any {
	t.Helper()
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestEncodeSystem(t *testing.T) {
	p := encode(t, openresponses.Request{
		Model:        "claude-opus-5",
		Instructions: "Be brief.",
		Input: openresponses.Items{
			openresponses.SystemText("You are a pirate."),
			openresponses.UserText("Hello"),
			openresponses.DeveloperText("Answer in French."),
		},
	})
	var got []string
	for _, b := range p.System {
		got = append(got, b.Text)
	}
	if strings.Join(got, "|") != "Be brief.|You are a pirate.|Answer in French." {
		t.Fatalf("system = %q", got)
	}
	if len(p.Messages) != 1 || p.Messages[0].Role != sdk.MessageParamRoleUser || p.Messages[0].Content[0].OfText.Text != "Hello" {
		t.Fatalf("messages = %+v", p.Messages)
	}
	if p.MaxTokens != DefaultMaxTokens || p.Model != "claude-opus-5" {
		t.Fatalf("max_tokens/model = %d/%s", p.MaxTokens, p.Model)
	}
}

func TestEncodeTurns(t *testing.T) {
	p := encode(t, openresponses.Request{
		Input: openresponses.Items{
			openresponses.UserText("What is the weather?"),
			&openresponses.ReasoningItem{
				Summary:          openresponses.Contents{&openresponses.SummaryText{Text: "Need a lookup."}},
				EncryptedContent: "sig==",
			},
			openresponses.AssistantText("Let me check."),
			&openresponses.FunctionCall{CallID: "toolu_1", Name: "weather", Arguments: `{"city":"Oslo"}`},
			openresponses.NewFunctionCallOutput("toolu_1", "rainy"),
			openresponses.UserText("Thanks"),
		},
	})
	if len(p.Messages) != 3 {
		t.Fatalf("got %d messages, want 3", len(p.Messages))
	}
	assistant := p.Messages[1]
	if assistant.Role != sdk.MessageParamRoleAssistant || len(assistant.Content) != 3 {
		t.Fatalf("assistant = %+v", assistant)
	}
	if th := assistant.Content[0].OfThinking; th == nil || th.Signature != "sig==" || th.Thinking != "Need a lookup." {
		t.Fatalf("thinking = %+v", assistant.Content[0])
	}
	if assistant.Content[1].OfText.Text != "Let me check." {
		t.Fatalf("text = %+v", assistant.Content[1])
	}
	if tu := assistant.Content[2].OfToolUse; tu == nil || tu.ID != "toolu_1" || tu.Name != "weather" || tu.Input.(map[string]any)["city"] != "Oslo" {
		t.Fatalf("tool use = %+v", assistant.Content[2])
	}
	user := p.Messages[2]
	if user.Role != sdk.MessageParamRoleUser || len(user.Content) != 2 {
		t.Fatalf("user = %+v", user)
	}
	if tr := user.Content[0].OfToolResult; tr == nil || tr.ToolUseID != "toolu_1" || tr.Content[0].OfText.Text != "rainy" {
		t.Fatalf("tool result = %+v", user.Content[0])
	}
	if user.Content[1].OfText.Text != "Thanks" {
		t.Fatalf("trailing text = %+v", user.Content[1])
	}
}

func TestEncodeFunctionOutputParts(t *testing.T) {
	p := encode(t, openresponses.Request{Input: openresponses.Items{
		&openresponses.FunctionCall{CallID: "c", Name: "shot"},
		&openresponses.FunctionCallOutput{CallID: "c", Output: openresponses.FunctionCallOutputData{Parts: openresponses.Contents{
			&openresponses.Text{Text: "here"},
			&openresponses.InputImage{ImageURL: "data:image/png;base64,aGk="},
		}}},
	}})
	if tu := p.Messages[0].Content[0].OfToolUse; tu.Input.(map[string]any) == nil {
		t.Fatalf("empty arguments should give an empty object, got %#v", tu.Input)
	}
	tr := p.Messages[1].Content[0].OfToolResult
	if len(tr.Content) != 2 || tr.Content[0].OfText.Text != "here" || tr.Content[1].OfImage.Source.OfBase64.Data != "aGk=" {
		t.Fatalf("tool result = %+v", tr)
	}
}

func TestEncodeMedia(t *testing.T) {
	p := encode(t, openresponses.Request{Input: openresponses.Items{openresponses.UserMessage(
		&openresponses.InputImage{ImageURL: "data:image/jpeg;base64,aGk="},
		&openresponses.InputImage{ImageURL: "https://example.com/cat.png"},
		&openresponses.InputImage{FileID: "file_1"},
		&openresponses.InputFile{Filename: "report.pdf", FileData: "aGk="},
		&openresponses.InputFile{Filename: "notes.txt", FileData: "data:text/plain,hello%20there"},
		&openresponses.InputFile{Filename: "diagram.png", FileData: "aGk="},
		&openresponses.InputFile{FileURL: "https://example.com/a.pdf"},
		&openresponses.InputFile{FileID: "file_2"},
	)}})
	c := p.Messages[0].Content
	if len(c) != 8 {
		t.Fatalf("got %d blocks", len(c))
	}
	if src := c[0].OfImage.Source.OfBase64; src == nil || src.MediaType != "image/jpeg" || src.Data != "aGk=" {
		t.Fatalf("data URL image = %+v", c[0])
	}
	if src := c[1].OfImage.Source.OfURL; src == nil || src.URL != "https://example.com/cat.png" {
		t.Fatalf("URL image = %+v", c[1])
	}
	if src := c[2].OfImage.Source.OfFile; src == nil || src.FileID != "file_1" {
		t.Fatalf("file image = %+v", c[2])
	}
	if d := c[3].OfDocument; d == nil || d.Source.OfBase64 == nil || d.Source.OfBase64.Data != "aGk=" || d.Title.Or("") != "report.pdf" {
		t.Fatalf("pdf = %+v", c[3])
	}
	if d := c[4].OfDocument; d == nil || d.Source.OfText == nil || d.Source.OfText.Data != "hello there" {
		t.Fatalf("text document = %+v", c[4])
	}
	if src := c[5].OfImage.Source.OfBase64; src == nil || src.MediaType != "image/png" {
		t.Fatalf("image file = %+v", c[5])
	}
	if d := c[6].OfDocument; d == nil || d.Source.OfURL == nil || d.Source.OfURL.URL != "https://example.com/a.pdf" {
		t.Fatalf("url document = %+v", c[6])
	}
	if d := c[7].OfDocument; d == nil || d.Source.OfFile == nil || d.Source.OfFile.FileID != "file_2" {
		t.Fatalf("file document = %+v", c[7])
	}
}

func TestEncodeInputErrors(t *testing.T) {
	cases := []struct {
		name        string
		input       openresponses.Items
		code, param string
	}{
		{"bad arguments", openresponses.Items{&openresponses.FunctionCall{CallID: "c", Name: "f", Arguments: "[1]"}}, openresponses.CodeInvalidValue, "input[0].arguments"},
		{"video", openresponses.Items{openresponses.UserMessage(&openresponses.InputVideo{VideoURL: "https://example.com/v.mp4"})}, openresponses.CodeUnsupportedParameter, "input[0].content[0].type"},
		{"compaction", openresponses.Items{&openresponses.Compaction{EncryptedContent: "x"}}, openresponses.CodeUnsupportedParameter, "input[0].type"},
		{"item reference", openresponses.Items{&openresponses.ItemReference{ID: "msg_1"}}, openresponses.CodeUnsupportedParameter, "input[0].type"},
		{"foreign item", openresponses.Items{&openresponses.UnknownItem{Type: "gemini.executable_code", Raw: json.RawMessage(`{"type":"gemini.executable_code"}`)}}, openresponses.CodeUnsupportedParameter, "input[0].type"},
		{"extension without block", openresponses.Items{&openresponses.UnknownItem{Type: "anthropic.server_tool_use", Raw: json.RawMessage(`{"type":"anthropic.server_tool_use"}`)}}, openresponses.CodeInvalidValue, "input[0]"},
		{"system image", openresponses.Items{&openresponses.Message{Role: openresponses.RoleSystem, Content: openresponses.Contents{&openresponses.InputImage{ImageURL: "data:image/png;base64,aGk="}}}}, openresponses.CodeUnsupportedParameter, "input[0].content[0].type"},
		{"bare base64 without filename", openresponses.Items{openresponses.UserMessage(&openresponses.InputFile{FileData: "aGk="})}, openresponses.CodeInvalidValue, "input[0].content[0].filename"},
		{"unsupported file type", openresponses.Items{openresponses.UserMessage(&openresponses.InputFile{Filename: "a.zip", FileData: "aGk="})}, openresponses.CodeInvalidValue, "input[0].content[0].file_data"},
		{"not a url", openresponses.Items{openresponses.UserMessage(&openresponses.InputImage{ImageURL: "cat.png"})}, openresponses.CodeInvalidValue, "input[0].content[0].image_url"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := encodeRequest(openresponses.Request{Input: tc.input}, DefaultMaxTokens)
			wantErr(t, err, tc.code, tc.param)
		})
	}
	_, err := encodeRequest(openresponses.Request{PreviousResponseID: "resp_1"}, DefaultMaxTokens)
	wantErr(t, err, openresponses.CodePreviousResponseNotFound, "previous_response_id")
}

func TestEncodeExtensionItem(t *testing.T) {
	p := encode(t, openresponses.Request{Input: openresponses.Items{
		&openresponses.UnknownItem{Type: "anthropic.server_tool_use", Raw: json.RawMessage(
			`{"type":"anthropic.server_tool_use","id":"ax_1","block":{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{"query":"x"}}}`)},
	}})
	body := wire(t, p)
	messages := body["messages"].([]any)
	content := messages[0].(map[string]any)["content"].([]any)
	block := content[0].(map[string]any)
	if messages[0].(map[string]any)["role"] != "assistant" || block["type"] != "server_tool_use" || block["id"] != "srvtoolu_1" {
		t.Fatalf("block = %v", block)
	}
}

func TestEncodeConfig(t *testing.T) {
	p := encode(t, openresponses.Request{
		MaxOutputTokens:  ptr(256),
		Temperature:      ptr(0.5),
		TopP:             ptr(0.9),
		SafetyIdentifier: "user-1",
		PromptCacheKey:   "k",
		ServiceTier:      openresponses.ServiceTierDefault,
		Metadata:         map[string]string{"team": "a"},
		Reasoning:        openresponses.ReasoningConfig{Effort: openresponses.ReasoningEffortXHigh, Summary: openresponses.ReasoningSummaryAuto},
		Text:             openresponses.TextConfig{Format: openresponses.JSONSchemaFormat("out", json.RawMessage(`{"type":"object"}`), true)},
	})
	body := wire(t, p)
	if body["max_tokens"] != float64(256) || body["temperature"] != 0.5 || body["top_p"] != 0.9 {
		t.Fatalf("sampling = %v", body)
	}
	if body["metadata"].(map[string]any)["user_id"] != "user-1" {
		t.Fatalf("metadata = %v", body["metadata"])
	}
	if body["cache_control"].(map[string]any)["type"] != "ephemeral" {
		t.Fatalf("cache_control = %v", body["cache_control"])
	}
	if body["service_tier"] != "standard_only" {
		t.Fatalf("service_tier = %v", body["service_tier"])
	}
	oc := body["output_config"].(map[string]any)
	if oc["effort"] != "xhigh" || oc["format"].(map[string]any)["type"] != "json_schema" {
		t.Fatalf("output_config = %v", oc)
	}
	th := body["thinking"].(map[string]any)
	if th["type"] != "adaptive" || th["display"] != "summarized" {
		t.Fatalf("thinking = %v", th)
	}
	if _, ok := body["labels"]; ok {
		t.Fatalf("metadata must not be sent upstream: %v", body)
	}

	p = encode(t, openresponses.Request{Reasoning: openresponses.ReasoningConfig{Effort: openresponses.ReasoningEffortNone}})
	if body := wire(t, p); body["thinking"].(map[string]any)["type"] != "disabled" {
		t.Fatalf("effort none = %v", body["thinking"])
	}
	p = encode(t, openresponses.Request{Reasoning: openresponses.ReasoningConfig{Effort: openresponses.ReasoningEffortMinimal}})
	if body := wire(t, p); body["output_config"].(map[string]any)["effort"] != "low" {
		t.Fatalf("effort minimal = %v", body["output_config"])
	}

	p = encode(t, openresponses.Request{})
	body = wire(t, p)
	for _, key := range []string{"thinking", "output_config", "tools", "tool_choice", "system", "metadata", "cache_control", "temperature"} {
		if _, ok := body[key]; ok {
			t.Fatalf("empty request should not send %s: %v", key, body)
		}
	}
	if body["max_tokens"] != float64(DefaultMaxTokens) {
		t.Fatalf("max_tokens = %v", body["max_tokens"])
	}
}

func TestEncodeConfigErrors(t *testing.T) {
	cases := []struct {
		name        string
		req         openresponses.Request
		code, param string
	}{
		{"presence_penalty", openresponses.Request{PresencePenalty: ptr(0.1)}, openresponses.CodeUnsupportedParameter, "presence_penalty"},
		{"frequency_penalty", openresponses.Request{FrequencyPenalty: ptr(0.1)}, openresponses.CodeUnsupportedParameter, "frequency_penalty"},
		{"top_logprobs", openresponses.Request{TopLogprobs: ptr(2)}, openresponses.CodeUnsupportedParameter, "top_logprobs"},
		{"max_tool_calls", openresponses.Request{MaxToolCalls: ptr(2)}, openresponses.CodeUnsupportedParameter, "max_tool_calls"},
		{"truncation auto", openresponses.Request{Truncation: openresponses.TruncationAuto}, openresponses.CodeUnsupportedParameter, "truncation"},
		{"verbosity", openresponses.Request{Text: openresponses.TextConfig{Verbosity: openresponses.VerbosityLow}}, openresponses.CodeUnsupportedParameter, "text.verbosity"},
		{"service_tier flex", openresponses.Request{ServiceTier: openresponses.ServiceTierFlex}, openresponses.CodeUnsupportedParameter, "service_tier"},
		{"json_object", openresponses.Request{Text: openresponses.TextConfig{Format: &openresponses.TextFormat{Type: openresponses.TextFormatJSONObject}}}, openresponses.CodeUnsupportedParameter, "text.format.type"},
		{"bad schema", openresponses.Request{Text: openresponses.TextConfig{Format: openresponses.JSONSchemaFormat("o", json.RawMessage(`[]`), false)}}, openresponses.CodeInvalidValue, "text.format.schema"},
		{"allowed_tools", openresponses.Request{ToolChoice: openresponses.ToolChoice{Allowed: &openresponses.AllowedTools{Mode: openresponses.ToolChoiceAuto}}}, openresponses.CodeUnsupportedParameter, "tool_choice.type"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := encodeRequest(tc.req, DefaultMaxTokens)
			wantErr(t, err, tc.code, tc.param)
		})
	}
}

func TestEncodeTools(t *testing.T) {
	p := encode(t, openresponses.Request{
		Tools: openresponses.Tools{
			openresponses.NewFunctionTool("weather", "Look up weather", json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"],"additionalProperties":false}`)),
			&openresponses.FunctionTool{Name: "time", Strict: ptr(true)},
			&openresponses.UnknownTool{Type: "anthropic.web_search_20260209", Raw: json.RawMessage(`{"type":"anthropic.web_search_20260209","name":"web_search","max_uses":3}`)},
		},
	})
	tools := wire(t, p)["tools"].([]any)
	if len(tools) != 3 {
		t.Fatalf("tools = %v", tools)
	}
	weather := tools[0].(map[string]any)
	schema := weather["input_schema"].(map[string]any)
	if weather["name"] != "weather" || weather["description"] != "Look up weather" || schema["additionalProperties"] != false || schema["required"].([]any)[0] != "city" {
		t.Fatalf("weather = %v", weather)
	}
	tm := tools[1].(map[string]any)
	if tm["strict"] != true || tm["input_schema"].(map[string]any)["type"] != "object" {
		t.Fatalf("time = %v", tm)
	}
	ws := tools[2].(map[string]any)
	if ws["type"] != "web_search_20260209" || ws["name"] != "web_search" || ws["max_uses"] != float64(3) {
		t.Fatalf("web search = %v", ws)
	}

	cases := []struct {
		name        string
		tool        openresponses.Tool
		code, param string
	}{
		{"foreign tool", &openresponses.UnknownTool{Type: "gemini.google_search", Raw: json.RawMessage(`{"type":"gemini.google_search"}`)}, openresponses.CodeUnsupportedParameter, "tools[0].type"},
		{"bad parameters", openresponses.NewFunctionTool("f", "", json.RawMessage(`{`)), openresponses.CodeInvalidValue, "tools[0].parameters"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := encodeRequest(openresponses.Request{Tools: openresponses.Tools{tc.tool}}, DefaultMaxTokens)
			wantErr(t, err, tc.code, tc.param)
		})
	}
}

func TestEncodeToolChoice(t *testing.T) {
	cases := []struct {
		name     string
		choice   openresponses.ToolChoice
		parallel *bool
		want     map[string]any
	}{
		{"auto", openresponses.ToolChoice{Mode: openresponses.ToolChoiceAuto}, nil, map[string]any{"type": "auto"}},
		{"none", openresponses.ToolChoice{Mode: openresponses.ToolChoiceNone}, nil, map[string]any{"type": "none"}},
		{"required", openresponses.ToolChoice{Mode: openresponses.ToolChoiceRequired}, nil, map[string]any{"type": "any"}},
		{"function", openresponses.ToolChoiceFunction("weather"), nil, map[string]any{"type": "tool", "name": "weather"}},
		{"required serial", openresponses.ToolChoice{Mode: openresponses.ToolChoiceRequired}, ptr(false), map[string]any{"type": "any", "disable_parallel_tool_use": true}},
		{"serial only", openresponses.ToolChoice{}, ptr(false), map[string]any{"type": "auto", "disable_parallel_tool_use": true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := encode(t, openresponses.Request{ToolChoice: tc.choice, ParallelToolCalls: tc.parallel})
			got := wire(t, p)["tool_choice"].(map[string]any)
			for k, v := range tc.want {
				if got[k] != v {
					t.Fatalf("tool_choice = %v, want %v", got, tc.want)
				}
			}
			if len(got) != len(tc.want) {
				t.Fatalf("tool_choice = %v, want %v", got, tc.want)
			}
		})
	}
	if _, ok := wire(t, encode(t, openresponses.Request{ParallelToolCalls: ptr(true)}))["tool_choice"]; ok {
		t.Fatal("parallel_tool_calls: true should not add a tool_choice")
	}
}
