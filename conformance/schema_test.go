// Package conformance validates every shape the openresponses package
// marshals against the OpenAPI document of the specification version it
// targets. It lives in its own module so that the JSON Schema validator it
// needs never enters the dependency graph of the library or its users.
// Run it with "make test" or "cd conformance && go test ./...".
package conformance

import (
	"bytes"
	"encoding/json"
	"os"
	"sync"
	"testing"

	or "github.com/ChristopherDavenport/openresponses"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// openAPIPath is the document that "make spec-update" refreshes.
const openAPIPath = "../testdata/openapi-" + or.SpecVersion + ".json"

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

func sampleResponse() *or.Response {
	strict := true
	resp := or.NewResponse(or.Request{
		Model:      "gpt-5",
		Tools:      or.Tools{&or.FunctionTool{Name: "lookup", Description: "d", Parameters: json.RawMessage(`{"type":"object"}`), Strict: &strict}},
		ToolChoice: or.ToolChoice{Mode: or.ToolChoiceAuto},
		Reasoning:  or.ReasoningConfig{Effort: or.ReasoningEffortHigh},
		Metadata:   map[string]string{"k": "v"},
	})
	resp.ID = "resp_1"
	resp.Status = or.ResponseStatusCompleted
	resp.CompletedAt = ptr[int64](2)
	resp.Usage = &or.Usage{InputTokens: 1, OutputTokens: 2, TotalTokens: 3}
	resp.Output = or.Items{
		&or.Message{ID: "msg_1", Status: or.StatusCompleted, Role: or.RoleAssistant, Phase: or.PhaseFinalAnswer, Content: or.Contents{&or.OutputText{Text: "hi"}}},
	}
	return resp
}

func TestSchemaItemsOutput(t *testing.T) {
	tests := []struct {
		name   string
		schema string
		item   or.Item
	}{
		{"message", "Message", &or.Message{ID: "msg_1", Status: or.StatusCompleted, Role: or.RoleAssistant, Content: or.Contents{&or.OutputText{Text: "x"}, &or.Refusal{Refusal: "n"}}}},
		{"message_phase", "Message", &or.Message{ID: "msg_1", Status: or.StatusCompleted, Role: or.RoleAssistant, Phase: or.PhaseCommentary, Content: or.Contents{&or.OutputText{Text: "x"}}}},
		{"function_call", "FunctionCall", &or.FunctionCall{ID: "fc_1", Status: or.StatusCompleted, CallID: "call_1", Name: "f", Arguments: "{}"}},
		{"function_call_output_text", "FunctionCallOutput", &or.FunctionCallOutput{ID: "fco_1", Status: or.StatusCompleted, CallID: "call_1", Output: or.FunctionCallOutputData{Text: "ok"}}},
		{"function_call_output_parts", "FunctionCallOutput", &or.FunctionCallOutput{ID: "fco_1", Status: or.StatusCompleted, CallID: "call_1", Output: or.FunctionCallOutputData{Parts: or.Contents{&or.InputText{Text: "ok"}, &or.InputImage{ImageURL: "https://x/y.png"}}}}},
		{"reasoning", "ReasoningBody", &or.ReasoningItem{ID: "rs_1", Summary: or.Contents{&or.SummaryText{Text: "s"}}, Content: or.Contents{&or.ReasoningText{Text: "r"}}, EncryptedContent: "e"}},
		{"reasoning_empty", "ReasoningBody", &or.ReasoningItem{ID: "rs_1"}},
		{"compaction", "CompactionBody", &or.Compaction{ID: "cmp_1", EncryptedContent: "e", CreatedBy: "x"}},
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
		item or.Item
	}{
		{"user_text", or.UserText("hi")},
		{"user_parts", or.UserMessage(&or.InputText{Text: "a"}, &or.InputImage{ImageURL: "data:image/png;base64,AA=="}, &or.InputFile{Filename: "f", FileData: "AA=="})},
		{"system", or.SystemText("s")},
		{"developer", or.DeveloperText("d")},
		{"assistant", or.AssistantText("a")},
		{"assistant_phase", &or.Message{Role: or.RoleAssistant, Phase: or.PhaseFinalAnswer, Content: or.Contents{&or.OutputText{Text: "a"}, &or.Refusal{Refusal: "r"}}}},
		{"function_call", &or.FunctionCall{CallID: "call_1", Name: "f", Arguments: "{}"}},
		{"function_call_output", or.NewFunctionCallOutput("call_1", "ok")},
		{"reasoning", &or.ReasoningItem{Summary: or.Contents{&or.SummaryText{Text: "s"}}, EncryptedContent: "e"}},
		{"compaction", &or.Compaction{EncryptedContent: "e"}},
		{"item_reference", &or.ItemReference{ID: "msg_1"}},
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
		req  or.Request
	}{
		{"minimal", or.Request{Model: "m", Input: or.Items{or.UserText("hi")}}},
		{"tools", or.Request{
			Model:      "m",
			Input:      or.Items{or.UserText("hi")},
			Tools:      or.Tools{or.NewFunctionTool("f", "desc", json.RawMessage(`{"type":"object","properties":{}}`))},
			ToolChoice: or.ToolChoiceFunction("f"),
		}},
		{"allowed_tools", or.Request{
			Model:      "m",
			Input:      or.Items{or.UserText("hi")},
			ToolChoice: or.ToolChoice{Allowed: &or.AllowedTools{Tools: []or.ToolReference{or.FunctionReference("f")}, Mode: or.ToolChoiceRequired}},
		}},
		{"everything", or.Request{
			Model:              "m",
			Input:              or.Items{or.UserText("hi")},
			PreviousResponseID: "resp_0",
			Include:            []or.Include{or.IncludeReasoningEncryptedContent, or.IncludeOutputTextLogprobs},
			Metadata:           map[string]string{"a": "b"},
			Text:               or.TextConfig{Format: or.JSONSchemaFormat("out", json.RawMessage(`{"type":"object"}`), true), Verbosity: or.VerbosityLow},
			Temperature:        ptr(0.5),
			TopP:               ptr(0.9),
			PresencePenalty:    ptr(0.1),
			FrequencyPenalty:   ptr(0.1),
			ParallelToolCalls:  ptr(false),
			Stream:             true,
			StreamOptions:      &or.StreamOptions{IncludeObfuscation: true},
			MaxOutputTokens:    ptr(16),
			MaxToolCalls:       ptr(1),
			Reasoning:          or.ReasoningConfig{Effort: or.ReasoningEffortLow, Summary: or.ReasoningSummaryAuto},
			SafetyIdentifier:   "u",
			PromptCacheKey:     "c",
			Truncation:         or.TruncationAuto,
			Instructions:       "i",
			Store:              ptr(false),
			ServiceTier:        or.ServiceTierPriority,
			TopLogprobs:        ptr(2),
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertSchema(t, "CreateResponseBody", tt.req)
		})
	}
	t.Run("compact", func(t *testing.T) {
		assertSchema(t, "CompactResponseMethodPublicBody", or.CompactRequest{Model: "m", Input: or.Items{or.UserText("hi")}, PromptCacheKey: "k"})
	})
}

func TestSchemaResponse(t *testing.T) {
	t.Run("completed", func(t *testing.T) {
		assertSchema(t, "ResponseResource", sampleResponse())
	})
	t.Run("zero_value_fills_defaults", func(t *testing.T) {
		assertSchema(t, "ResponseResource", &or.Response{ID: "resp_1", Model: "m"})
	})
	t.Run("failed", func(t *testing.T) {
		resp := sampleResponse()
		resp.Status = or.ResponseStatusFailed
		resp.Error = &or.ErrorPayload{Type: or.ErrorTypeServerError, Code: "boom", Message: "bad"}
		assertSchema(t, "ResponseResource", resp)
	})
	t.Run("incomplete", func(t *testing.T) {
		resp := sampleResponse()
		resp.Status = or.ResponseStatusIncomplete
		resp.IncompleteDetails = &or.IncompleteDetails{Reason: or.IncompleteReasonMaxOutputTokens}
		assertSchema(t, "ResponseResource", resp)
	})
	t.Run("compaction", func(t *testing.T) {
		assertSchema(t, "CompactResource", &or.CompactResponse{ID: "resp_1", CreatedAt: 1, Output: or.Items{&or.Compaction{ID: "cmp_1", EncryptedContent: "e"}}})
	})
}

// allEventTypes lists every streaming event the specification defines,
// so the table below is checked for completeness.
var allEventTypes = []string{
	or.EventResponseCreated, or.EventResponseQueued, or.EventResponseInProgress,
	or.EventResponseCompleted, or.EventResponseFailed, or.EventResponseIncomplete,
	or.EventOutputItemAdded, or.EventOutputItemDone,
	or.EventContentPartAdded, or.EventContentPartDone,
	or.EventOutputTextDelta, or.EventOutputTextDone,
	or.EventRefusalDelta, or.EventRefusalDone,
	or.EventFunctionCallArgumentsDelta, or.EventFunctionCallArgumentsDone,
	or.EventReasoningSummaryPartAdded, or.EventReasoningSummaryPartDone,
	or.EventReasoningSummaryTextDelta, or.EventReasoningSummaryTextDone,
	or.EventReasoningDelta, or.EventReasoningDone,
	or.EventOutputTextAnnotationAdded, or.EventError,
}

func TestSchemaEvents(t *testing.T) {
	resp := sampleResponse()
	msg := &or.Message{ID: "msg_1", Status: or.StatusInProgress, Role: or.RoleAssistant}
	tests := []struct {
		schema string
		event  or.StreamEvent
	}{
		{"ResponseCreatedStreamingEvent", &or.ResponseCreatedEvent{Response: resp}},
		{"ResponseQueuedStreamingEvent", &or.ResponseQueuedEvent{Response: resp}},
		{"ResponseInProgressStreamingEvent", &or.ResponseInProgressEvent{Response: resp}},
		{"ResponseCompletedStreamingEvent", &or.ResponseCompletedEvent{Response: resp}},
		{"ResponseFailedStreamingEvent", &or.ResponseFailedEvent{Response: resp}},
		{"ResponseIncompleteStreamingEvent", &or.ResponseIncompleteEvent{Response: resp}},
		{"ResponseOutputItemAddedStreamingEvent", &or.OutputItemAddedEvent{OutputIndex: 0, Item: msg}},
		{"ResponseOutputItemDoneStreamingEvent", &or.OutputItemDoneEvent{OutputIndex: 0, Item: msg}},
		{"ResponseContentPartAddedStreamingEvent", &or.ContentPartAddedEvent{ItemID: "msg_1", Part: &or.OutputText{}}},
		{"ResponseContentPartDoneStreamingEvent", &or.ContentPartDoneEvent{ItemID: "msg_1", Part: &or.OutputText{Text: "x"}}},
		{"ResponseOutputTextDeltaStreamingEvent", &or.OutputTextDeltaEvent{ItemID: "msg_1", Delta: "x", Obfuscation: "y", Logprobs: []or.LogProb{{Token: "x", Logprob: -1}}}},
		{"ResponseOutputTextDoneStreamingEvent", &or.OutputTextDoneEvent{ItemID: "msg_1", Text: "x"}},
		{"ResponseRefusalDeltaStreamingEvent", &or.RefusalDeltaEvent{ItemID: "msg_1", Delta: "x"}},
		{"ResponseRefusalDoneStreamingEvent", &or.RefusalDoneEvent{ItemID: "msg_1", Refusal: "x"}},
		{"ResponseFunctionCallArgumentsDeltaStreamingEvent", &or.FunctionCallArgumentsDeltaEvent{ItemID: "fc_1", Delta: "{"}},
		{"ResponseFunctionCallArgumentsDoneStreamingEvent", &or.FunctionCallArgumentsDoneEvent{ItemID: "fc_1", Arguments: "{}"}},
		{"ResponseReasoningSummaryPartAddedStreamingEvent", &or.ReasoningSummaryPartAddedEvent{ItemID: "rs_1", Part: &or.SummaryText{}}},
		{"ResponseReasoningSummaryPartDoneStreamingEvent", &or.ReasoningSummaryPartDoneEvent{ItemID: "rs_1", Part: &or.SummaryText{Text: "s"}}},
		{"ResponseReasoningSummaryDeltaStreamingEvent", &or.ReasoningSummaryTextDeltaEvent{ItemID: "rs_1", Delta: "s"}},
		{"ResponseReasoningSummaryDoneStreamingEvent", &or.ReasoningSummaryTextDoneEvent{ItemID: "rs_1", Text: "s"}},
		{"ResponseReasoningDeltaStreamingEvent", &or.ReasoningDeltaEvent{ItemID: "rs_1", Delta: "r"}},
		{"ResponseReasoningDoneStreamingEvent", &or.ReasoningDoneEvent{ItemID: "rs_1", Text: "r"}},
		{"ResponseOutputTextAnnotationAddedStreamingEvent", &or.OutputTextAnnotationAddedEvent{ItemID: "msg_1", Annotation: &or.URLCitation{URL: "https://x", Title: "t", EndIndex: 1}}},
		{"ErrorStreamingEvent", &or.ErrorEvent{Error: or.ErrorPayload{Type: or.ErrorTypeServerError, Code: "c", Message: "m"}}},
	}
	seen := map[string]bool{}
	for _, tt := range tests {
		t.Run(tt.schema, func(t *testing.T) {
			tt.event.(or.SequenceSetter).SetSequence(7)
			data := assertSchema(t, tt.schema, tt.event)
			back, err := or.DecodeEvent(data)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if back.EventType() != tt.event.EventType() || back.Sequence() != 7 {
				t.Errorf("decoded %s seq %d", back.EventType(), back.Sequence())
			}
			seen[tt.event.EventType()] = true
		})
	}
	for _, typ := range allEventTypes {
		if !seen[typ] {
			t.Errorf("event type %s is not covered", typ)
		}
	}
	if len(seen) != len(allEventTypes) {
		t.Errorf("covered %d event types, the spec defines %d", len(seen), len(allEventTypes))
	}
}

func TestSchemaErrors(t *testing.T) {
	e := &or.Error{Type: or.ErrorTypeNotFound, Code: or.CodePreviousResponseNotFound, Message: "gone", Param: "previous_response_id"}
	t.Run("payload", func(t *testing.T) {
		assertSchema(t, "ErrorPayload", e.Payload())
	})
	t.Run("response_error", func(t *testing.T) {
		assertSchema(t, "Error", e.Payload())
	})
	t.Run("websocket", func(t *testing.T) {
		// The frame shape the server writes; the type is unexported.
		frame := struct {
			Type   string          `json:"type"`
			Status int             `json:"status"`
			Error  or.ErrorPayload `json:"error"`
		}{Type: or.EventError, Status: e.HTTPStatus(), Error: e.Payload()}
		assertSchema(t, "WebSocketErrorEvent", frame)
	})
}

func TestSchemaTools(t *testing.T) {
	strict := true
	t.Run("resource", func(t *testing.T) {
		assertSchema(t, "FunctionTool", &or.FunctionTool{Name: "f", Description: "d", Parameters: json.RawMessage(`{}`), Strict: &strict})
		assertSchema(t, "Tool", &or.FunctionTool{Name: "f", Strict: &strict})
	})
	t.Run("param", func(t *testing.T) {
		assertSchema(t, "FunctionToolParam", or.NewFunctionTool("f", "", nil))
		assertSchema(t, "ResponsesToolParam", or.NewFunctionTool("f", "d", json.RawMessage(`{"type":"object"}`)))
	})
	t.Run("tool_choice", func(t *testing.T) {
		for _, tc := range []or.ToolChoice{
			{Mode: or.ToolChoiceNone}, {Mode: or.ToolChoiceAuto}, {Mode: or.ToolChoiceRequired},
			or.ToolChoiceFunction("f"),
			{Allowed: &or.AllowedTools{Tools: []or.ToolReference{or.FunctionReference("f")}, Mode: or.ToolChoiceAuto}},
		} {
			assertSchema(t, "ToolChoiceParam", tc)
		}
	})
}
