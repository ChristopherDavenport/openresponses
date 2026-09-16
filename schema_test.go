package openresponses

import (
	"bytes"
	"encoding/json"
	"os"
	"sync"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const openAPIPath = "testdata/openapi-" + SpecVersion + ".json"

var (
	compilerOnce sync.Once
	compiler     *jsonschema.Compiler
	compilerErr  error
)

// schema compiles #/components/schemas/<name> from the embedded OpenAPI
// document.
func schema(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	compilerOnce.Do(func() {
		f, err := os.Open(openAPIPath)
		if err != nil {
			compilerErr = err
			return
		}
		defer f.Close()
		doc, err := jsonschema.UnmarshalJSON(f)
		if err != nil {
			compilerErr = err
			return
		}
		compiler = jsonschema.NewCompiler()
		compilerErr = compiler.AddResource("openapi.json", doc)
	})
	if compilerErr != nil {
		t.Fatalf("load OpenAPI document: %v", compilerErr)
	}
	sch, err := compiler.Compile("openapi.json#/components/schemas/" + name)
	if err != nil {
		t.Fatalf("compile schema %s: %v", name, err)
	}
	return sch
}

// assertSchema marshals v and validates the bytes against the named
// schema.
func assertSchema(t *testing.T, name string, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %T: %v", v, err)
	}
	assertSchemaBytes(t, name, data)
	return data
}

func assertSchemaBytes(t *testing.T, name string, data []byte) {
	t.Helper()
	decoded, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode %s: %v", data, err)
	}
	if err := schema(t, name).Validate(decoded); err != nil {
		t.Errorf("%s does not match %s:\n%v\n%s", data, name, err, data)
	}
}

func ptr[T any](v T) *T { return &v }

func sampleResponse() *Response {
	strict := true
	resp := NewResponse(Request{
		Model:      "gpt-5",
		Tools:      Tools{&FunctionTool{Name: "lookup", Description: "d", Parameters: json.RawMessage(`{"type":"object"}`), Strict: &strict}},
		ToolChoice: ToolChoice{Mode: ToolChoiceAuto},
		Reasoning:  &ReasoningConfig{Effort: ReasoningEffortHigh},
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

func TestSchemaItemsOutput(t *testing.T) {
	tests := []struct {
		name   string
		schema string
		item   Item
	}{
		{"message", "Message", &Message{ID: "msg_1", Status: StatusCompleted, Role: RoleAssistant, Content: Contents{&OutputText{Text: "x"}, &Refusal{Refusal: "n"}}}},
		{"message_phase", "Message", &Message{ID: "msg_1", Status: StatusCompleted, Role: RoleAssistant, Phase: PhaseCommentary, Content: Contents{&OutputText{Text: "x"}}}},
		{"function_call", "FunctionCall", &FunctionCall{ID: "fc_1", Status: StatusCompleted, CallID: "call_1", Name: "f", Arguments: "{}"}},
		{"function_call_output_text", "FunctionCallOutput", &FunctionCallOutput{ID: "fco_1", Status: StatusCompleted, CallID: "call_1", Output: FunctionCallOutputData{Text: "ok"}}},
		{"function_call_output_parts", "FunctionCallOutput", &FunctionCallOutput{ID: "fco_1", Status: StatusCompleted, CallID: "call_1", Output: FunctionCallOutputData{Parts: Contents{&InputText{Text: "ok"}, &InputImage{ImageURL: "https://x/y.png"}}}}},
		{"reasoning", "ReasoningBody", &ReasoningItem{ID: "rs_1", Summary: Contents{&SummaryText{Text: "s"}}, Content: Contents{&ReasoningText{Text: "r"}}, EncryptedContent: "e"}},
		{"reasoning_empty", "ReasoningBody", &ReasoningItem{ID: "rs_1"}},
		{"compaction", "CompactionBody", &Compaction{ID: "cmp_1", EncryptedContent: "e", CreatedBy: "x"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := assertSchema(t, tt.schema, tt.item)
			assertSchemaBytes(t, "ItemField", data)
		})
	}
}

func TestSchemaItemsInput(t *testing.T) {
	tests := []struct {
		name string
		item Item
	}{
		{"user_text", UserText("hi")},
		{"user_parts", UserMessage(&InputText{Text: "a"}, &InputImage{ImageURL: "data:image/png;base64,AA=="}, &InputFile{Filename: "f", FileData: "AA=="})},
		{"system", SystemText("s")},
		{"developer", DeveloperText("d")},
		{"assistant", AssistantText("a")},
		{"assistant_phase", &Message{Role: RoleAssistant, Phase: PhaseFinalAnswer, Content: Contents{&OutputText{Text: "a"}, &Refusal{Refusal: "r"}}}},
		{"function_call", &FunctionCall{CallID: "call_1", Name: "f", Arguments: "{}"}},
		{"function_call_output", NewFunctionCallOutput("call_1", "ok")},
		{"reasoning", &ReasoningItem{Summary: Contents{&SummaryText{Text: "s"}}, EncryptedContent: "e"}},
		{"compaction", &Compaction{EncryptedContent: "e"}},
		{"item_reference", &ItemReference{ID: "msg_1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertSchema(t, "ItemParam", tt.item)
		})
	}
}

func TestSchemaRequest(t *testing.T) {
	tests := []struct {
		name string
		req  Request
	}{
		{"minimal", Request{Model: "m", Input: Input{UserText("hi")}}},
		{"tools", Request{
			Model:      "m",
			Input:      Input{UserText("hi")},
			Tools:      Tools{NewFunctionTool("f", "desc", json.RawMessage(`{"type":"object","properties":{}}`))},
			ToolChoice: ToolChoiceFunction("f"),
		}},
		{"allowed_tools", Request{
			Model:      "m",
			Input:      Input{UserText("hi")},
			ToolChoice: ToolChoice{Allowed: &AllowedTools{Tools: []ToolReference{FunctionReference("f")}, Mode: ToolChoiceRequired}},
		}},
		{"everything", Request{
			Model:              "m",
			Input:              Input{UserText("hi")},
			PreviousResponseID: "resp_0",
			Include:            []Include{IncludeReasoningEncryptedContent, IncludeOutputTextLogprobs},
			Metadata:           map[string]string{"a": "b"},
			Text:               TextConfig{Format: JSONSchemaFormat("out", json.RawMessage(`{"type":"object"}`), true), Verbosity: VerbosityLow},
			Temperature:        ptr(0.5),
			TopP:               ptr(0.9),
			PresencePenalty:    ptr(0.1),
			FrequencyPenalty:   ptr(0.1),
			ParallelToolCalls:  ptr(false),
			Stream:             true,
			StreamOptions:      &StreamOptions{IncludeObfuscation: true},
			MaxOutputTokens:    ptr(16),
			MaxToolCalls:       ptr(1),
			Reasoning:          &ReasoningConfig{Effort: ReasoningEffortLow, Summary: ReasoningSummaryAuto},
			SafetyIdentifier:   "u",
			PromptCacheKey:     "c",
			Truncation:         TruncationAuto,
			Instructions:       "i",
			Store:              ptr(false),
			ServiceTier:        ServiceTierPriority,
			TopLogprobs:        ptr(2),
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertSchema(t, "CreateResponseBody", tt.req)
		})
	}
	t.Run("compact", func(t *testing.T) {
		assertSchema(t, "CompactResponseMethodPublicBody", CompactRequest{Model: "m", Input: Input{UserText("hi")}, PromptCacheKey: "k"})
	})
}

func TestSchemaResponse(t *testing.T) {
	t.Run("completed", func(t *testing.T) {
		assertSchema(t, "ResponseResource", sampleResponse())
	})
	t.Run("zero_value_fills_defaults", func(t *testing.T) {
		assertSchema(t, "ResponseResource", &Response{ID: "resp_1", Model: "m"})
	})
	t.Run("failed", func(t *testing.T) {
		resp := sampleResponse()
		resp.Status = ResponseStatusFailed
		resp.Error = &ErrorPayload{Type: ErrorTypeServerError, Code: "boom", Message: "bad"}
		assertSchema(t, "ResponseResource", resp)
	})
	t.Run("incomplete", func(t *testing.T) {
		resp := sampleResponse()
		resp.Status = ResponseStatusIncomplete
		resp.IncompleteDetails = &IncompleteDetails{Reason: IncompleteReasonMaxOutputTokens}
		assertSchema(t, "ResponseResource", resp)
	})
	t.Run("compaction", func(t *testing.T) {
		assertSchema(t, "CompactResource", &CompactResponse{ID: "resp_1", CreatedAt: 1, Output: Items{&Compaction{ID: "cmp_1", EncryptedContent: "e"}}})
	})
}

func TestSchemaEvents(t *testing.T) {
	resp := sampleResponse()
	msg := &Message{ID: "msg_1", Status: StatusInProgress, Role: RoleAssistant}
	tests := []struct {
		schema string
		event  StreamEvent
	}{
		{"ResponseCreatedStreamingEvent", &ResponseCreatedEvent{Response: resp}},
		{"ResponseQueuedStreamingEvent", &ResponseQueuedEvent{Response: resp}},
		{"ResponseInProgressStreamingEvent", &ResponseInProgressEvent{Response: resp}},
		{"ResponseCompletedStreamingEvent", &ResponseCompletedEvent{Response: resp}},
		{"ResponseFailedStreamingEvent", &ResponseFailedEvent{Response: resp}},
		{"ResponseIncompleteStreamingEvent", &ResponseIncompleteEvent{Response: resp}},
		{"ResponseOutputItemAddedStreamingEvent", &OutputItemAddedEvent{OutputIndex: 0, Item: msg}},
		{"ResponseOutputItemDoneStreamingEvent", &OutputItemDoneEvent{OutputIndex: 0, Item: msg}},
		{"ResponseContentPartAddedStreamingEvent", &ContentPartAddedEvent{ItemID: "msg_1", Part: &OutputText{}}},
		{"ResponseContentPartDoneStreamingEvent", &ContentPartDoneEvent{ItemID: "msg_1", Part: &OutputText{Text: "x"}}},
		{"ResponseOutputTextDeltaStreamingEvent", &OutputTextDeltaEvent{ItemID: "msg_1", Delta: "x", Obfuscation: "y", Logprobs: []LogProb{{Token: "x", Logprob: -1}}}},
		{"ResponseOutputTextDoneStreamingEvent", &OutputTextDoneEvent{ItemID: "msg_1", Text: "x"}},
		{"ResponseRefusalDeltaStreamingEvent", &RefusalDeltaEvent{ItemID: "msg_1", Delta: "x"}},
		{"ResponseRefusalDoneStreamingEvent", &RefusalDoneEvent{ItemID: "msg_1", Refusal: "x"}},
		{"ResponseFunctionCallArgumentsDeltaStreamingEvent", &FunctionCallArgumentsDeltaEvent{ItemID: "fc_1", Delta: "{"}},
		{"ResponseFunctionCallArgumentsDoneStreamingEvent", &FunctionCallArgumentsDoneEvent{ItemID: "fc_1", Arguments: "{}"}},
		{"ResponseReasoningSummaryPartAddedStreamingEvent", &ReasoningSummaryPartAddedEvent{ItemID: "rs_1", Part: &SummaryText{}}},
		{"ResponseReasoningSummaryPartDoneStreamingEvent", &ReasoningSummaryPartDoneEvent{ItemID: "rs_1", Part: &SummaryText{Text: "s"}}},
		{"ResponseReasoningSummaryDeltaStreamingEvent", &ReasoningSummaryTextDeltaEvent{ItemID: "rs_1", Delta: "s"}},
		{"ResponseReasoningSummaryDoneStreamingEvent", &ReasoningSummaryTextDoneEvent{ItemID: "rs_1", Text: "s"}},
		{"ResponseReasoningDeltaStreamingEvent", &ReasoningDeltaEvent{ItemID: "rs_1", Delta: "r"}},
		{"ResponseReasoningDoneStreamingEvent", &ReasoningDoneEvent{ItemID: "rs_1", Text: "r"}},
		{"ResponseOutputTextAnnotationAddedStreamingEvent", &OutputTextAnnotationAddedEvent{ItemID: "msg_1", Annotation: &URLCitation{URL: "https://x", Title: "t", EndIndex: 1}}},
		{"ErrorStreamingEvent", &ErrorEvent{Error: ErrorPayload{Type: ErrorTypeServerError, Code: "c", Message: "m"}}},
	}
	seen := map[string]bool{}
	for _, tt := range tests {
		t.Run(tt.schema, func(t *testing.T) {
			tt.event.(sequenceSetter).SetSequence(7)
			data := assertSchema(t, tt.schema, tt.event)
			back, err := DecodeEvent(data)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if back.EventType() != tt.event.EventType() || back.Sequence() != 7 {
				t.Errorf("decoded %s seq %d", back.EventType(), back.Sequence())
			}
			seen[tt.event.EventType()] = true
		})
	}
	if len(seen) != len(eventRegistry.decoders) {
		t.Errorf("covered %d event types, registry has %d", len(seen), len(eventRegistry.decoders))
	}
}

func TestSchemaErrors(t *testing.T) {
	e := &Error{Type: ErrorTypeNotFound, Code: CodePreviousResponseNotFound, Message: "gone", Param: "previous_response_id"}
	t.Run("payload", func(t *testing.T) {
		assertSchema(t, "ErrorPayload", e.Payload())
	})
	t.Run("response_error", func(t *testing.T) {
		assertSchema(t, "Error", e.Payload())
	})
	t.Run("websocket", func(t *testing.T) {
		assertSchema(t, "WebSocketErrorEvent", webSocketError{Type: EventError, Status: e.HTTPStatus(), Error: e.Payload()})
	})
}

func TestSchemaTools(t *testing.T) {
	strict := true
	t.Run("resource", func(t *testing.T) {
		assertSchema(t, "FunctionTool", &FunctionTool{Name: "f", Description: "d", Parameters: json.RawMessage(`{}`), Strict: &strict})
		assertSchema(t, "Tool", &FunctionTool{Name: "f", Strict: &strict})
	})
	t.Run("param", func(t *testing.T) {
		assertSchema(t, "FunctionToolParam", NewFunctionTool("f", "", nil))
		assertSchema(t, "ResponsesToolParam", NewFunctionTool("f", "d", json.RawMessage(`{"type":"object"}`)))
	})
	t.Run("tool_choice", func(t *testing.T) {
		for _, tc := range []ToolChoice{
			{Mode: ToolChoiceNone}, {Mode: ToolChoiceAuto}, {Mode: ToolChoiceRequired},
			ToolChoiceFunction("f"),
			{Allowed: &AllowedTools{Tools: []ToolReference{FunctionReference("f")}, Mode: ToolChoiceAuto}},
		} {
			assertSchema(t, "ToolChoiceParam", tc)
		}
	})
}
