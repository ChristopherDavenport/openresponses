package gemini

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/ChristopherDavenport/openresponses"
	"google.golang.org/genai"
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

func encode(t *testing.T, req openresponses.Request) ([]*genai.Content, *genai.GenerateContentConfig) {
	t.Helper()
	contents, cfg, err := encodeRequest(req, ThinkingAuto)
	if err != nil {
		t.Fatalf("encodeRequest: %v", err)
	}
	return contents, cfg
}

func TestEncodeSystemInstruction(t *testing.T) {
	contents, cfg := encode(t, openresponses.Request{
		Model:        "gemini-2.5-pro",
		Instructions: "Be brief.",
		Input: openresponses.Items{
			openresponses.SystemText("You are a pirate."),
			openresponses.UserText("Hello"),
			openresponses.DeveloperText("Answer in French."),
		},
	})
	var got []string
	for _, p := range cfg.SystemInstruction.Parts {
		got = append(got, p.Text)
	}
	want := []string{"Be brief.", "You are a pirate.", "Answer in French."}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("system parts = %q, want %q", got, want)
	}
	if len(contents) != 1 || contents[0].Role != genai.RoleUser || contents[0].Parts[0].Text != "Hello" {
		t.Fatalf("contents = %+v, want one user turn", contents)
	}
	if cfg.CandidateCount != 1 {
		t.Fatalf("CandidateCount = %d, want 1", cfg.CandidateCount)
	}
}

func TestEncodeTurns(t *testing.T) {
	contents, _ := encode(t, openresponses.Request{
		Input: openresponses.Items{
			openresponses.UserText("What is the weather?"),
			&openresponses.ReasoningItem{
				Summary:          openresponses.Contents{&openresponses.SummaryText{Text: "Need a lookup."}},
				EncryptedContent: base64.StdEncoding.EncodeToString([]byte("sig")),
			},
			openresponses.AssistantText("Let me check."),
			&openresponses.FunctionCall{CallID: "call_1", Name: "weather", Arguments: `{"city":"Oslo"}`},
			openresponses.NewFunctionCallOutput("call_1", "rainy"),
			openresponses.UserText("Thanks"),
		},
	})
	if len(contents) != 3 {
		t.Fatalf("got %d turns, want 3: %+v", len(contents), contents)
	}
	if contents[0].Role != genai.RoleUser {
		t.Fatalf("turn 0 role = %q", contents[0].Role)
	}
	model := contents[1]
	if model.Role != genai.RoleModel || len(model.Parts) != 3 {
		t.Fatalf("model turn = %+v, want 3 parts", model)
	}
	if !model.Parts[0].Thought || model.Parts[0].ThoughtSignature != nil || model.Parts[0].Text != "Need a lookup." {
		t.Fatalf("thought part = %+v", model.Parts[0])
	}
	// The signature rides on the first part after the thoughts.
	if model.Parts[1].Text != "Let me check." || string(model.Parts[1].ThoughtSignature) != "sig" {
		t.Fatalf("text part = %+v", model.Parts[1])
	}
	fc := model.Parts[2].FunctionCall
	if fc == nil || fc.ID != "call_1" || fc.Name != "weather" || fc.Args["city"] != "Oslo" {
		t.Fatalf("function call = %+v", model.Parts[2])
	}
	user := contents[2]
	if user.Role != genai.RoleUser || len(user.Parts) != 2 {
		t.Fatalf("user turn = %+v, want function response then text", user)
	}
	fr := user.Parts[0].FunctionResponse
	if fr == nil || fr.ID != "call_1" || fr.Name != "weather" || fr.Response["output"] != "rainy" {
		t.Fatalf("function response = %+v", user.Parts[0])
	}
	if user.Parts[1].Text != "Thanks" {
		t.Fatalf("trailing text = %+v", user.Parts[1])
	}
}

func TestEncodeFunctionOutputObject(t *testing.T) {
	contents, _ := encode(t, openresponses.Request{
		Input: openresponses.Items{
			&openresponses.FunctionCall{CallID: "c", Name: "f", Arguments: ""},
			openresponses.NewFunctionCallOutput("c", `{"temp": 12, "unit": "C"}`),
		},
	})
	fr := contents[1].Parts[0].FunctionResponse
	if fr.Response["temp"] != float64(12) || fr.Response["unit"] != "C" {
		t.Fatalf("response = %v, want the object itself", fr.Response)
	}
	if contents[0].Parts[0].FunctionCall.Args != nil {
		t.Fatalf("empty arguments should give nil args, got %v", contents[0].Parts[0].FunctionCall.Args)
	}
}

func TestEncodeFunctionOutputMedia(t *testing.T) {
	contents, _ := encode(t, openresponses.Request{
		Input: openresponses.Items{
			&openresponses.FunctionCall{CallID: "c", Name: "shot"},
			&openresponses.FunctionCallOutput{CallID: "c", Output: openresponses.FunctionCallOutputData{Parts: openresponses.Contents{
				&openresponses.Text{Text: "here"},
				&openresponses.InputImage{ImageURL: "data:image/png;base64,aGk="},
			}}},
		},
	})
	fr := contents[1].Parts[0].FunctionResponse
	if fr.Response["output"] != "here" || len(fr.Parts) != 1 || fr.Parts[0].InlineData.MIMEType != "image/png" || string(fr.Parts[0].InlineData.Data) != "hi" {
		t.Fatalf("function response = %+v", fr)
	}
}

func TestEncodeInputErrors(t *testing.T) {
	cases := []struct {
		name        string
		input       openresponses.Items
		code, param string
	}{
		{"output without call", openresponses.Items{openresponses.NewFunctionCallOutput("nope", "x")}, openresponses.CodeInvalidValue, "input[0].call_id"},
		{"bad arguments", openresponses.Items{&openresponses.FunctionCall{CallID: "c", Name: "f", Arguments: "[1]"}}, openresponses.CodeInvalidValue, "input[0].arguments"},
		{"bad signature", openresponses.Items{&openresponses.ReasoningItem{EncryptedContent: "***"}}, openresponses.CodeInvalidValue, "input[0].encrypted_content"},
		{"compaction", openresponses.Items{&openresponses.Compaction{EncryptedContent: "x"}}, openresponses.CodeUnsupportedParameter, "input[0].type"},
		{"item reference", openresponses.Items{&openresponses.ItemReference{ID: "msg_1"}}, openresponses.CodeUnsupportedParameter, "input[0].type"},
		{"foreign item", openresponses.Items{&openresponses.UnknownItem{Type: "acme.thing", Raw: json.RawMessage(`{"type":"acme.thing"}`)}}, openresponses.CodeUnsupportedParameter, "input[0].type"},
		{"system image", openresponses.Items{&openresponses.Message{Role: openresponses.RoleSystem, Content: openresponses.Contents{&openresponses.InputImage{ImageURL: "data:image/png;base64,aGk="}}}}, openresponses.CodeUnsupportedParameter, "input[0].content[0].type"},
		{"image file_id", openresponses.Items{openresponses.UserMessage(&openresponses.InputImage{FileID: "file_1"})}, openresponses.CodeUnsupportedParameter, "input[0].content[0].file_id"},
		{"file file_id", openresponses.Items{openresponses.UserMessage(&openresponses.InputFile{FileID: "file_1"})}, openresponses.CodeUnsupportedParameter, "input[0].content[0].file_id"},
		{"bare base64 without filename", openresponses.Items{openresponses.UserMessage(&openresponses.InputFile{FileData: "aGk="})}, openresponses.CodeInvalidValue, "input[0].content[0].filename"},
		{"not a url", openresponses.Items{openresponses.UserMessage(&openresponses.InputImage{ImageURL: "cat.png"})}, openresponses.CodeInvalidValue, "input[0].content[0].image_url"},
		{"data url without type", openresponses.Items{openresponses.UserMessage(&openresponses.InputImage{ImageURL: "data:;base64,aGk="})}, openresponses.CodeInvalidValue, "input[0].content[0].image_url"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := encodeRequest(openresponses.Request{Input: tc.input}, ThinkingAuto)
			wantErr(t, err, tc.code, tc.param)
		})
	}
}

func TestEncodePreviousResponseID(t *testing.T) {
	_, _, err := encodeRequest(openresponses.Request{PreviousResponseID: "resp_1"}, ThinkingAuto)
	wantErr(t, err, openresponses.CodePreviousResponseNotFound, "previous_response_id")
	if !openresponses.IsNotFound(err) {
		t.Fatalf("err = %v, want not_found", err)
	}
}

func TestEncodeMedia(t *testing.T) {
	contents, _ := encode(t, openresponses.Request{
		Input: openresponses.Items{openresponses.UserMessage(
			&openresponses.InputImage{ImageURL: "data:image/jpeg;base64,aGk=", Detail: openresponses.ImageDetailHigh},
			&openresponses.InputImage{ImageURL: "https://example.com/cat.png"},
			&openresponses.InputFile{Filename: "report.pdf", FileData: "aGk="},
			&openresponses.InputFile{Filename: "notes.txt", FileURL: "gs://bucket/notes.txt"},
			&openresponses.InputFile{FileData: "data:text/plain,hello%20there"},
			&openresponses.InputVideo{VideoURL: "https://www.youtube.com/watch?v=abc"},
		)},
	})
	parts := contents[0].Parts
	if len(parts) != 6 {
		t.Fatalf("got %d parts", len(parts))
	}
	if p := parts[0]; p.InlineData == nil || p.InlineData.MIMEType != "image/jpeg" || string(p.InlineData.Data) != "hi" ||
		p.MediaResolution == nil || p.MediaResolution.Level != genai.PartMediaResolutionLevelMediaResolutionHigh {
		t.Fatalf("data URL image = %+v", p)
	}
	if p := parts[1]; p.FileData == nil || p.FileData.FileURI != "https://example.com/cat.png" || p.FileData.MIMEType != "image/png" {
		t.Fatalf("URL image = %+v", p)
	}
	if p := parts[2]; p.InlineData == nil || p.InlineData.MIMEType != "application/pdf" || p.InlineData.DisplayName != "report.pdf" || string(p.InlineData.Data) != "hi" {
		t.Fatalf("base64 file = %+v", p)
	}
	if p := parts[3]; p.FileData == nil || p.FileData.FileURI != "gs://bucket/notes.txt" || p.FileData.DisplayName != "notes.txt" || p.FileData.MIMEType != "text/plain" {
		t.Fatalf("gs file = %+v", p)
	}
	if p := parts[4]; p.InlineData == nil || p.InlineData.MIMEType != "text/plain" || string(p.InlineData.Data) != "hello there" {
		t.Fatalf("plain data URL = %+v", p)
	}
	if p := parts[5]; p.FileData == nil || p.FileData.FileURI != "https://www.youtube.com/watch?v=abc" {
		t.Fatalf("video = %+v", p)
	}
}

func TestEncodeExtensionItem(t *testing.T) {
	contents, _ := encode(t, openresponses.Request{
		Input: openresponses.Items{
			&openresponses.UnknownItem{Type: "gemini.executable_code", Raw: json.RawMessage(
				`{"type":"gemini.executable_code","id":"x","part":{"executableCode":{"language":"PYTHON","code":"print(1)"}}}`)},
		},
	})
	p := contents[0].Parts[0]
	if contents[0].Role != genai.RoleModel || p.ExecutableCode == nil || p.ExecutableCode.Code != "print(1)" {
		t.Fatalf("extension part = %+v", p)
	}
	_, _, err := encodeRequest(openresponses.Request{Input: openresponses.Items{
		&openresponses.UnknownItem{Type: "gemini.executable_code", Raw: json.RawMessage(`{"type":"gemini.executable_code"}`)},
	}}, ThinkingAuto)
	wantErr(t, err, openresponses.CodeInvalidValue, "input[0]")
}

func TestEncodeConfig(t *testing.T) {
	_, cfg := encode(t, openresponses.Request{
		Model:             "gemini-3-pro-preview",
		MaxOutputTokens:   ptr(256),
		Temperature:       ptr(0.5),
		TopP:              ptr(0.9),
		PresencePenalty:   ptr(0.1),
		FrequencyPenalty:  ptr(0.2),
		TopLogprobs:       ptr(3),
		ParallelToolCalls: ptr(true),
		ServiceTier:       openresponses.ServiceTierDefault,
		Metadata:          map[string]string{"team": "a"},
		Reasoning:         openresponses.ReasoningConfig{Effort: openresponses.ReasoningEffortHigh, Summary: openresponses.ReasoningSummaryAuto},
		Text:              openresponses.TextConfig{Format: openresponses.JSONSchemaFormat("out", json.RawMessage(`{"type":"object"}`), true)},
	})
	if cfg.MaxOutputTokens != 256 || *cfg.Temperature != 0.5 || *cfg.TopP != 0.9 || *cfg.PresencePenalty != float32(0.1) || *cfg.FrequencyPenalty != float32(0.2) {
		t.Fatalf("sampling = %+v", cfg)
	}
	if !cfg.ResponseLogprobs || *cfg.Logprobs != 3 {
		t.Fatalf("logprobs = %v/%v", cfg.ResponseLogprobs, cfg.Logprobs)
	}
	if cfg.ServiceTier != genai.ServiceTierStandard || cfg.Labels["team"] != "a" {
		t.Fatalf("tier/labels = %v/%v", cfg.ServiceTier, cfg.Labels)
	}
	if cfg.ThinkingConfig == nil || cfg.ThinkingConfig.ThinkingLevel != genai.ThinkingLevelHigh || !cfg.ThinkingConfig.IncludeThoughts {
		t.Fatalf("thinking = %+v", cfg.ThinkingConfig)
	}
	if cfg.ResponseMIMEType != "application/json" {
		t.Fatalf("mime = %q", cfg.ResponseMIMEType)
	}
	if schema, ok := cfg.ResponseJsonSchema.(map[string]any); !ok || schema["type"] != "object" {
		t.Fatalf("schema = %#v", cfg.ResponseJsonSchema)
	}

	_, cfg = encode(t, openresponses.Request{
		Model:     "gemini-2.5-flash",
		Reasoning: openresponses.ReasoningConfig{Effort: openresponses.ReasoningEffortNone},
		Text:      openresponses.TextConfig{Format: &openresponses.TextFormat{Type: openresponses.TextFormatJSONObject}},
	})
	if cfg.ThinkingConfig == nil || cfg.ThinkingConfig.ThinkingBudget == nil || *cfg.ThinkingConfig.ThinkingBudget != 0 {
		t.Fatalf("effort none = %+v", cfg.ThinkingConfig)
	}
	if cfg.ResponseMIMEType != "application/json" || cfg.ResponseJsonSchema != nil {
		t.Fatalf("json_object = %q/%v", cfg.ResponseMIMEType, cfg.ResponseJsonSchema)
	}

	_, cfg = encode(t, openresponses.Request{})
	if cfg.ThinkingConfig != nil || cfg.Temperature != nil || cfg.ToolConfig != nil || cfg.Tools != nil || cfg.SystemInstruction != nil {
		t.Fatalf("empty request should leave the config empty: %+v", cfg)
	}
}

// TestThinkingFor pins which generation takes which encoding. Sending the
// wrong one is a 400 from Gemini, and a model ID that does not name its
// generation has to fall to levels rather than guess.
func TestThinkingFor(t *testing.T) {
	cases := map[string]Thinking{
		"gemini-2.5-pro":                          ThinkingBudget,
		"gemini-2.5-flash-lite":                   ThinkingBudget,
		"gemini-1.5-pro":                          ThinkingBudget,
		"publishers/google/models/gemini-2.5-pro": ThinkingBudget,
		"gemini-3-pro-preview":                    ThinkingLevel,
		"gemini-3.1-flash":                        ThinkingLevel,
		"projects/p/locations/l/endpoints/123":    ThinkingLevel,
		"":                                        ThinkingLevel,
	}
	for model, want := range cases {
		if got := thinkingFor(model); got != want {
			t.Errorf("thinkingFor(%q) = %v, want %v", model, got, want)
		}
	}
}

// TestEncodeReasoning covers both encodings over the whole effort ladder.
// Exactly one of thinkingLevel and thinkingBudget may be set: Gemini
// rejects a request carrying both.
func TestEncodeReasoning(t *testing.T) {
	cases := []struct {
		name   string
		model  string
		opt    Thinking
		effort openresponses.ReasoningEffort
		level  genai.ThinkingLevel
		budget *int32
		code   string
	}{
		{name: "3 high", model: "gemini-3-pro-preview", effort: openresponses.ReasoningEffortHigh, level: genai.ThinkingLevelHigh},
		{name: "3 minimal", model: "gemini-3-flash", effort: openresponses.ReasoningEffortMinimal, level: genai.ThinkingLevelMinimal},
		{name: "2.5 high", model: "gemini-2.5-pro", effort: openresponses.ReasoningEffortHigh, budget: genai.Ptr(int32(24576))},
		{name: "2.5 minimal", model: "gemini-2.5-flash", effort: openresponses.ReasoningEffortMinimal, budget: genai.Ptr(int32(512))},
		{name: "2.5 low", model: "gemini-2.5-flash", effort: openresponses.ReasoningEffortLow, budget: genai.Ptr(int32(4096))},
		{name: "2.5 medium", model: "gemini-2.5-flash", effort: openresponses.ReasoningEffortMedium, budget: genai.Ptr(int32(8192))},
		{name: "2.5 none", model: "gemini-2.5-flash", effort: openresponses.ReasoningEffortNone, budget: genai.Ptr(int32(0))},
		// An override beats the model ID, for a tuned endpoint.
		{name: "forced budget", model: "gemini-3-pro-preview", opt: ThinkingBudget, effort: openresponses.ReasoningEffortHigh, budget: genai.Ptr(int32(24576))},
		{name: "forced level", model: "gemini-2.5-pro", opt: ThinkingLevel, effort: openresponses.ReasoningEffortHigh, level: genai.ThinkingLevelHigh},
		// Gemini 3 always thinks, so "none" cannot be expressed.
		{name: "3 none", model: "gemini-3-pro-preview", effort: openresponses.ReasoningEffortNone, code: openresponses.CodeInvalidValue},
		{name: "xhigh", model: "gemini-3-pro-preview", effort: openresponses.ReasoningEffortXHigh, code: openresponses.CodeInvalidValue},
		{name: "xhigh on 2.5", model: "gemini-2.5-pro", effort: openresponses.ReasoningEffortXHigh, code: openresponses.CodeInvalidValue},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := openresponses.ReasoningConfig{Effort: tc.effort}
			tcfg, err := encodeReasoning(r, tc.model, tc.opt)
			if tc.code != "" {
				wantErr(t, err, tc.code, "reasoning.effort")
				return
			}
			if err != nil {
				t.Fatalf("encodeReasoning: %v", err)
			}
			if tcfg.ThinkingLevel != tc.level {
				t.Errorf("level = %q, want %q", tcfg.ThinkingLevel, tc.level)
			}
			switch {
			case tc.budget == nil && tcfg.ThinkingBudget != nil:
				t.Errorf("budget = %d, want unset", *tcfg.ThinkingBudget)
			case tc.budget != nil && tcfg.ThinkingBudget == nil:
				t.Errorf("budget unset, want %d", *tc.budget)
			case tc.budget != nil && *tcfg.ThinkingBudget != *tc.budget:
				t.Errorf("budget = %d, want %d", *tcfg.ThinkingBudget, *tc.budget)
			}
			if tcfg.ThinkingLevel != "" && tcfg.ThinkingBudget != nil {
				t.Errorf("both level and budget set: %+v", tcfg)
			}
		})
	}
}

// A summary of thinking that was switched off is contradictory, and
// Gemini rejects the pair rather than picking one.
func TestEncodeReasoningNoneWithSummary(t *testing.T) {
	_, err := encodeReasoning(openresponses.ReasoningConfig{
		Effort:  openresponses.ReasoningEffortNone,
		Summary: openresponses.ReasoningSummaryAuto,
	}, "gemini-2.5-flash", ThinkingAuto)
	wantErr(t, err, openresponses.CodeInvalidValue, "reasoning.summary")
}

func TestEncodeConfigErrors(t *testing.T) {
	cases := []struct {
		name        string
		req         openresponses.Request
		code, param string
	}{
		{"parallel_tool_calls false", openresponses.Request{ParallelToolCalls: ptr(false)}, openresponses.CodeUnsupportedParameter, "parallel_tool_calls"},
		{"max_tool_calls", openresponses.Request{MaxToolCalls: ptr(2)}, openresponses.CodeUnsupportedParameter, "max_tool_calls"},
		{"safety_identifier", openresponses.Request{SafetyIdentifier: "u"}, openresponses.CodeUnsupportedParameter, "safety_identifier"},
		{"prompt_cache_key", openresponses.Request{PromptCacheKey: "k"}, openresponses.CodeUnsupportedParameter, "prompt_cache_key"},
		{"truncation auto", openresponses.Request{Truncation: openresponses.TruncationAuto}, openresponses.CodeUnsupportedParameter, "truncation"},
		{"verbosity", openresponses.Request{Text: openresponses.TextConfig{Verbosity: openresponses.VerbosityLow}}, openresponses.CodeUnsupportedParameter, "text.verbosity"},
		{"service_tier", openresponses.Request{ServiceTier: "gold"}, openresponses.CodeInvalidValue, "service_tier"},
		{"effort xhigh", openresponses.Request{Reasoning: openresponses.ReasoningConfig{Effort: openresponses.ReasoningEffortXHigh}}, openresponses.CodeInvalidValue, "reasoning.effort"},
		{"bad schema", openresponses.Request{Text: openresponses.TextConfig{Format: openresponses.JSONSchemaFormat("o", json.RawMessage(`{`), false)}}, openresponses.CodeInvalidValue, "text.format.schema"},
		{"format type", openresponses.Request{Text: openresponses.TextConfig{Format: &openresponses.TextFormat{Type: "yaml"}}}, openresponses.CodeInvalidValue, "text.format.type"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := encodeRequest(tc.req, ThinkingAuto)
			wantErr(t, err, tc.code, tc.param)
		})
	}
}

func TestEncodeTools(t *testing.T) {
	_, cfg := encode(t, openresponses.Request{
		Tools: openresponses.Tools{
			&openresponses.UnknownTool{Type: "gemini.google_search", Raw: json.RawMessage(`{"type":"gemini.google_search","excludeDomains":["spam.example"]}`)},
			openresponses.NewFunctionTool("weather", "Look up weather", json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}}}`)),
			&openresponses.UnknownTool{Type: "gemini.url_context"},
			openresponses.NewFunctionTool("time", "Current time", nil),
		},
	})
	if len(cfg.Tools) != 3 {
		t.Fatalf("got %d tools, want declarations + 2 server tools: %+v", len(cfg.Tools), cfg.Tools)
	}
	decls := cfg.Tools[0].FunctionDeclarations
	if len(decls) != 2 || decls[0].Name != "weather" || decls[1].Name != "time" {
		t.Fatalf("declarations = %+v", decls)
	}
	if schema, ok := decls[0].ParametersJsonSchema.(map[string]any); !ok || schema["type"] != "object" {
		t.Fatalf("parameters = %#v", decls[0].ParametersJsonSchema)
	}
	if decls[1].ParametersJsonSchema != nil {
		t.Fatalf("nil parameters should stay nil, got %#v", decls[1].ParametersJsonSchema)
	}
	if gs := cfg.Tools[1].GoogleSearch; gs == nil || !reflect.DeepEqual(gs.ExcludeDomains, []string{"spam.example"}) {
		t.Fatalf("google search = %+v", cfg.Tools[1])
	}
	if cfg.Tools[2].URLContext == nil {
		t.Fatalf("url context = %+v", cfg.Tools[2])
	}

	cases := []struct {
		name        string
		tool        openresponses.Tool
		code, param string
	}{
		{"foreign tool", &openresponses.UnknownTool{Type: "web_search", Raw: json.RawMessage(`{"type":"web_search"}`)}, openresponses.CodeUnsupportedParameter, "tools[0].type"},
		{"unknown gemini tool", &openresponses.UnknownTool{Type: "gemini.crystal_ball", Raw: json.RawMessage(`{"type":"gemini.crystal_ball"}`)}, openresponses.CodeInvalidValue, "tools[0].type"},
		{"bad parameters", openresponses.NewFunctionTool("f", "", json.RawMessage(`{`)), openresponses.CodeInvalidValue, "tools[0].parameters"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := encodeRequest(openresponses.Request{Tools: openresponses.Tools{tc.tool}}, ThinkingAuto)
			wantErr(t, err, tc.code, tc.param)
		})
	}
}

func TestEncodeToolChoice(t *testing.T) {
	cases := []struct {
		name   string
		choice openresponses.ToolChoice
		mode   genai.FunctionCallingConfigMode
		names  []string
	}{
		{"auto", openresponses.ToolChoice{Mode: openresponses.ToolChoiceAuto}, genai.FunctionCallingConfigModeAuto, nil},
		{"none", openresponses.ToolChoice{Mode: openresponses.ToolChoiceNone}, genai.FunctionCallingConfigModeNone, nil},
		{"required", openresponses.ToolChoice{Mode: openresponses.ToolChoiceRequired}, genai.FunctionCallingConfigModeAny, nil},
		{"function", openresponses.ToolChoiceFunction("weather"), genai.FunctionCallingConfigModeAny, []string{"weather"}},
		{"allowed", openresponses.ToolChoice{Allowed: &openresponses.AllowedTools{Mode: openresponses.ToolChoiceRequired, Tools: []openresponses.ToolReference{openresponses.FunctionReference("a"), openresponses.FunctionReference("b")}}}, genai.FunctionCallingConfigModeAny, []string{"a", "b"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, cfg := encode(t, openresponses.Request{ToolChoice: tc.choice})
			fcc := cfg.ToolConfig.FunctionCallingConfig
			if fcc.Mode != tc.mode || !reflect.DeepEqual(fcc.AllowedFunctionNames, tc.names) {
				t.Fatalf("config = %+v, want %s %v", fcc, tc.mode, tc.names)
			}
		})
	}
	_, _, err := encodeRequest(openresponses.Request{ToolChoice: openresponses.ToolChoice{Allowed: &openresponses.AllowedTools{
		Mode: openresponses.ToolChoiceAuto, Tools: []openresponses.ToolReference{{Type: "gemini.google_search"}},
	}}}, ThinkingAuto)
	wantErr(t, err, openresponses.CodeUnsupportedParameter, "tool_choice.tools[0].type")
}
