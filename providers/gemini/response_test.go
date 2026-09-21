package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ChristopherDavenport/openresponses"
	"github.com/ChristopherDavenport/openresponses/streamtest"
	"google.golang.org/genai"
)

// newAdapter returns an adapter whose client talks to handler over an
// in-process server, as the Gemini API backend.
func newAdapter(t *testing.T, handler http.Handler) *Adapter {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		APIKey:      "test",
		HTTPOptions: genai.HTTPOptions{BaseURL: srv.URL},
	})
	if err != nil {
		t.Fatal(err)
	}
	return New(client)
}

// sse serves each chunk as one server-sent event, the way
// streamGenerateContent?alt=sse does.
func sse(chunks ...string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, c := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", c)
		}
	})
}

// apiError serves a Gemini error envelope with the given status.
func apiError(status int, body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	})
}

// run streams req through an adapter replaying chunks and validates the
// event sequence.
func run(t *testing.T, req openresponses.Request, chunks ...string) *streamtest.Sink {
	t.Helper()
	sink, err := streamtest.Run(context.Background(), newAdapter(t, sse(chunks...)), req)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	return sink
}

func hello() openresponses.Request {
	return openresponses.Request{Model: "gemini-2.5-pro", Input: openresponses.Items{openresponses.UserText("Hi")}}
}

const (
	textChunk   = `{"candidates":[{"content":{"parts":[{"text":"Hel"}],"role":"model"},"index":0}],"responseId":"r1","modelVersion":"gemini-2.5-pro-001"}`
	finishChunk = `{"candidates":[{"content":{"parts":[{"text":"lo"}],"role":"model"},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"totalTokenCount":5}}`
)

func TestStreamText(t *testing.T) {
	sink := run(t, hello(), textChunk, finishChunk)
	resp := sink.Response()
	if resp.Status != openresponses.ResponseStatusCompleted {
		t.Fatalf("status = %s", resp.Status)
	}
	if resp.OutputText() != "Hello" {
		t.Fatalf("text = %q", resp.OutputText())
	}
	if resp.Model != "gemini-2.5-pro-001" {
		t.Fatalf("model = %q, want the served version", resp.Model)
	}
	if len(resp.Output) != 1 {
		t.Fatalf("output = %d items", len(resp.Output))
	}
	if u := resp.Usage; u == nil || u.InputTokens != 3 || u.OutputTokens != 2 || u.TotalTokens != 5 {
		t.Fatalf("usage = %+v", resp.Usage)
	}
	var deltas []string
	for _, ev := range sink.Events() {
		if d, ok := ev.(*openresponses.OutputTextDeltaEvent); ok {
			deltas = append(deltas, d.Delta)
		}
	}
	if strings.Join(deltas, "|") != "Hel|lo" {
		t.Fatalf("deltas = %q", deltas)
	}
}

func TestCreate(t *testing.T) {
	resp, err := newAdapter(t, sse(textChunk, finishChunk)).Create(context.Background(), hello())
	if err != nil {
		t.Fatal(err)
	}
	if resp.OutputText() != "Hello" || resp.Status != openresponses.ResponseStatusCompleted {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestStreamFunctionCall(t *testing.T) {
	sink := run(t, hello(),
		`{"candidates":[{"content":{"parts":[{"functionCall":{"id":"fc1","name":"weather","args":{"city":"Oslo"}}}],"role":"model"},"finishReason":"STOP"}]}`)
	calls := sink.Response().FunctionCalls()
	if len(calls) != 1 || calls[0].CallID != "fc1" || calls[0].Name != "weather" || calls[0].Arguments != `{"city":"Oslo"}` {
		t.Fatalf("calls = %+v", calls)
	}

	sink = run(t, hello(),
		`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"ping"}}],"role":"model"},"finishReason":"STOP"}]}`)
	calls = sink.Response().FunctionCalls()
	if len(calls) != 1 || calls[0].CallID == "" || calls[0].Arguments != "{}" {
		t.Fatalf("calls without id = %+v", calls)
	}
}

func TestStreamItemsInOrder(t *testing.T) {
	sink := run(t, hello(),
		`{"candidates":[{"content":{"parts":[{"text":"Checking. "}],"role":"model"}}]}`,
		`{"candidates":[{"content":{"parts":[{"functionCall":{"id":"c1","name":"f","args":{}}}],"role":"model"}}]}`,
		`{"candidates":[{"content":{"parts":[{"text":"Done."}],"role":"model"},"finishReason":"STOP"}]}`)
	var types []string
	for _, item := range sink.Response().Output {
		types = append(types, item.ItemType())
	}
	if strings.Join(types, ",") != "message,function_call,message" {
		t.Fatalf("items = %v", types)
	}
}

// TestSignatureRoundTrip covers the Gemini 3 layout: thoughts stream
// first, then the signature rides on the function call. The response
// carries it on a reasoning item, and encoding that output back puts it
// on the function call again.
func TestSignatureRoundTrip(t *testing.T) {
	sink := run(t, hello(),
		`{"candidates":[{"content":{"parts":[{"text":"Need the weather.","thought":true}],"role":"model"}}]}`,
		`{"candidates":[{"content":{"parts":[{"functionCall":{"id":"c1","name":"weather","args":{"city":"Oslo"}},"thoughtSignature":"c2ln"}],"role":"model"},"finishReason":"STOP"}]}`)
	out := sink.Response().Output
	if len(out) != 2 {
		t.Fatalf("output = %d items", len(out))
	}
	rs, ok := out[0].(*openresponses.ReasoningItem)
	if !ok || rs.Summary.Text() != "Need the weather." || rs.EncryptedContent != "c2ln" {
		t.Fatalf("reasoning = %+v", out[0])
	}
	if _, ok := out[1].(*openresponses.FunctionCall); !ok {
		t.Fatalf("second item = %T", out[1])
	}

	contents, _, err := encodeInput(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(contents) != 1 || contents[0].Role != genai.RoleModel || len(contents[0].Parts) != 2 {
		t.Fatalf("contents = %+v", contents)
	}
	thought, call := contents[0].Parts[0], contents[0].Parts[1]
	if !thought.Thought || thought.Text != "Need the weather." || thought.ThoughtSignature != nil {
		t.Fatalf("thought part = %+v", thought)
	}
	if call.FunctionCall == nil || string(call.ThoughtSignature) != "sig" {
		t.Fatalf("call part = %+v, want the signature back on it", call)
	}
}

func TestSignatureWithoutThoughts(t *testing.T) {
	sink := run(t, hello(),
		`{"candidates":[{"content":{"parts":[{"text":"Hi","thoughtSignature":"c2ln"}],"role":"model"},"finishReason":"STOP"}]}`)
	out := sink.Response().Output
	if len(out) != 2 {
		t.Fatalf("output = %d items", len(out))
	}
	rs, ok := out[0].(*openresponses.ReasoningItem)
	if !ok || len(rs.Summary) != 0 || rs.EncryptedContent != "c2ln" {
		t.Fatalf("reasoning = %+v", out[0])
	}
	contents, _, err := encodeInput(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(contents) != 1 || len(contents[0].Parts) != 1 || contents[0].Parts[0].Text != "Hi" || string(contents[0].Parts[0].ThoughtSignature) != "sig" {
		t.Fatalf("contents = %+v", contents[0].Parts[0])
	}
}

func TestSignatureAtEndOfTurn(t *testing.T) {
	// A signature with nothing after it in the model turn gets a thought
	// part of its own, ahead of the next user turn.
	contents, _, err := encodeInput(openresponses.Items{
		&openresponses.ReasoningItem{EncryptedContent: "c2ln"},
		openresponses.UserText("next"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(contents) != 2 || contents[0].Role != genai.RoleModel || !contents[0].Parts[0].Thought || string(contents[0].Parts[0].ThoughtSignature) != "sig" || contents[1].Role != genai.RoleUser {
		t.Fatalf("contents = %+v", contents)
	}
}

func TestStreamIncomplete(t *testing.T) {
	sink := run(t, hello(),
		`{"candidates":[{"content":{"parts":[{"text":"Once upon"}],"role":"model"},"finishReason":"MAX_TOKENS"}]}`)
	resp := sink.Response()
	if resp.Status != openresponses.ResponseStatusIncomplete || resp.IncompleteDetails.Reason != openresponses.IncompleteReasonMaxOutputTokens {
		t.Fatalf("resp = %+v %+v", resp.Status, resp.IncompleteDetails)
	}
	if msg := resp.Output[0].(*openresponses.Message); msg.Status != openresponses.StatusIncomplete || msg.Text() != "Once upon" {
		t.Fatalf("message = %+v", msg)
	}

	sink = run(t, hello(), `{"candidates":[{"content":{"parts":[{"text":"I"}],"role":"model"},"finishReason":"SAFETY"}]}`)
	if r := sink.Response(); r.IncompleteDetails == nil || r.IncompleteDetails.Reason != openresponses.IncompleteReasonContentFilter {
		t.Fatalf("safety = %+v", r.IncompleteDetails)
	}

	sink = run(t, hello(), `{"promptFeedback":{"blockReason":"SAFETY"},"usageMetadata":{"promptTokenCount":3,"totalTokenCount":3}}`)
	r := sink.Response()
	if r.Status != openresponses.ResponseStatusIncomplete || r.IncompleteDetails.Reason != openresponses.IncompleteReasonContentFilter || len(r.Output) != 0 {
		t.Fatalf("blocked prompt = %+v", r)
	}
	if r.Usage == nil || r.Usage.InputTokens != 3 {
		t.Fatalf("blocked prompt usage = %+v", r.Usage)
	}
}

func TestStreamModelError(t *testing.T) {
	// finishMessage only reaches the SDK's Candidate on Vertex AI; the
	// Gemini API converter drops it, so the generic message is expected.
	_, err := streamtest.Run(context.Background(), newAdapter(t, sse(
		`{"candidates":[{"content":{"parts":[{"text":"call("}],"role":"model"},"finishReason":"MALFORMED_FUNCTION_CALL","finishMessage":"bad call"}]}`)), hello())
	wantErr(t, err, "malformed_function_call", "")
	var oerr *openresponses.Error
	if !errorsAs(err, &oerr) || oerr.Type != openresponses.ErrorTypeModelError || !strings.Contains(oerr.Message, "MALFORMED_FUNCTION_CALL") {
		t.Fatalf("err = %v", err)
	}
}

func TestStreamTruncated(t *testing.T) {
	_, err := streamtest.Run(context.Background(), newAdapter(t, sse(textChunk)), hello())
	wantErr(t, err, "truncated_stream", "")
}

func TestStreamGrounding(t *testing.T) {
	sink := run(t, hello(),
		`{"candidates":[{"content":{"parts":[{"text":"Café is nice. "}],"role":"model"}}]}`,
		`{"candidates":[{"content":{"parts":[{"text":"Really nice."}],"role":"model"},"finishReason":"STOP",
		  "groundingMetadata":{
		    "groundingChunks":[{"web":{"uri":"https://example.com/a","title":"A"}},{"web":{"uri":"https://example.com/b","title":"B"}}],
		    "groundingSupports":[
		      {"segment":{"startIndex":0,"endIndex":15,"text":"Café is nice."},"groundingChunkIndices":[0,1]},
		      {"segment":{"startIndex":16,"endIndex":28,"text":"Really nice."},"groundingChunkIndices":[1]}
		    ]}}]}`)
	msg := sink.Response().Output[0].(*openresponses.Message)
	text := msg.Content[0].(*openresponses.OutputText)
	if text.Text != "Café is nice. Really nice." || len(text.Annotations) != 3 {
		t.Fatalf("text = %q annotations = %d", text.Text, len(text.Annotations))
	}
	want := []openresponses.URLCitation{
		{URL: "https://example.com/a", Title: "A", StartIndex: 0, EndIndex: 13},
		{URL: "https://example.com/b", Title: "B", StartIndex: 0, EndIndex: 13},
		{URL: "https://example.com/b", Title: "B", StartIndex: 14, EndIndex: 26},
	}
	for i, a := range text.Annotations {
		got := a.(*openresponses.URLCitation)
		if *got != want[i] {
			t.Fatalf("annotation %d = %+v, want %+v", i, *got, want[i])
		}
	}
	// Character offsets, not bytes: é is two bytes, one character.
	if runes := []rune(text.Text); string(runes[14:26]) != "Really nice." {
		t.Fatalf("offsets do not select the cited text: %q", string(runes[14:26]))
	}
}

func TestStreamLogprobs(t *testing.T) {
	sink := run(t, hello(),
		`{"candidates":[{"content":{"parts":[{"text":"Hi"}],"role":"model"},"finishReason":"STOP",
		  "logprobsResult":{"chosenCandidates":[{"token":"Hi","logProbability":-0.1}],
		    "topCandidates":[{"candidates":[{"token":"Hi","logProbability":-0.1},{"token":"Hey","logProbability":-2.5}]}]}}]}`)
	text := sink.Response().Output[0].(*openresponses.Message).Content[0].(*openresponses.OutputText)
	if len(text.Logprobs) != 1 || text.Logprobs[0].Token != "Hi" || len(text.Logprobs[0].TopLogprobs) != 2 || text.Logprobs[0].TopLogprobs[1].Token != "Hey" {
		t.Fatalf("logprobs = %+v", text.Logprobs)
	}
	if got := text.Logprobs[0].Bytes; len(got) != 2 || got[0] != 'H' {
		t.Fatalf("bytes = %v", got)
	}
}

func TestStreamExtensionParts(t *testing.T) {
	sink := run(t, hello(),
		`{"candidates":[{"content":{"parts":[
		  {"executableCode":{"language":"PYTHON","code":"print(1)"}},
		  {"codeExecutionResult":{"outcome":"OUTCOME_OK","output":"1\n"}},
		  {"text":"It prints 1."}],"role":"model"},"finishReason":"STOP"}]}`)
	out := sink.Response().Output
	if len(out) != 3 || out[0].ItemType() != "gemini.executable_code" || out[1].ItemType() != "gemini.code_execution_result" || out[2].ItemType() != "message" {
		t.Fatalf("output = %+v", out)
	}
	code := out[0].(*openresponses.UnknownItem)
	if code.ID == "" || code.Status != openresponses.StatusCompleted {
		t.Fatalf("item = %+v", code)
	}
	var wire struct {
		Part struct {
			ExecutableCode *genai.ExecutableCode `json:"executableCode"`
		} `json:"part"`
	}
	if err := json.Unmarshal(code.Raw, &wire); err != nil || wire.Part.ExecutableCode == nil || wire.Part.ExecutableCode.Code != "print(1)" {
		t.Fatalf("raw = %s (%v)", code.Raw, err)
	}

	contents, _, err := encodeInput(out)
	if err != nil {
		t.Fatal(err)
	}
	parts := contents[0].Parts
	if len(parts) != 3 || parts[0].ExecutableCode == nil || parts[1].CodeExecutionResult == nil || parts[1].CodeExecutionResult.Output != "1\n" || parts[2].Text != "It prints 1." {
		t.Fatalf("replayed parts = %+v", parts)
	}
}

func TestStreamThoughtsWithSummary(t *testing.T) {
	sink := run(t, hello(),
		`{"candidates":[{"content":{"parts":[{"text":"First, ","thought":true}],"role":"model"}}]}`,
		`{"candidates":[{"content":{"parts":[{"text":"consider.","thought":true}],"role":"model"}}]}`,
		`{"candidates":[{"content":{"parts":[{"text":"Answer."}],"role":"model"},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"thoughtsTokenCount":7,"cachedContentTokenCount":1,"totalTokenCount":12}}`)
	resp := sink.Response()
	rs := resp.Output[0].(*openresponses.ReasoningItem)
	if rs.Summary.Text() != "First, consider." || rs.EncryptedContent != "" {
		t.Fatalf("reasoning = %+v", rs)
	}
	if resp.OutputText() != "Answer." {
		t.Fatalf("text = %q", resp.OutputText())
	}
	u := resp.Usage
	if u.InputTokens != 3 || u.OutputTokens != 9 || u.OutputTokensDetails.ReasoningTokens != 7 || u.InputTokensDetails.CachedTokens != 1 || u.TotalTokens != 12 {
		t.Fatalf("usage = %+v", u)
	}
}

func TestMapError(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		typ    openresponses.ErrorType
		code   string
		retry  string
	}{
		{"rate limited", 429, `{"error":{"code":429,"message":"quota","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"6.2s"}]}}`, openresponses.ErrorTypeTooManyRequests, "resource_exhausted", "7"},
		{"invalid", 400, `{"error":{"code":400,"message":"bad","status":"INVALID_ARGUMENT"}}`, openresponses.ErrorTypeInvalidRequest, "invalid_argument", ""},
		{"not found", 404, `{"error":{"code":404,"message":"no model","status":"NOT_FOUND"}}`, openresponses.ErrorTypeNotFound, "not_found", ""},
		{"unauthenticated", 401, `{"error":{"code":401,"message":"key","status":"UNAUTHENTICATED"}}`, openresponses.ErrorTypeInvalidRequest, "unauthenticated", ""},
		{"unavailable", 503, `{"error":{"code":503,"message":"down","status":"UNAVAILABLE"}}`, openresponses.ErrorTypeServerError, "unavailable", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := streamtest.Run(context.Background(), newAdapter(t, apiError(tc.status, tc.body)), hello())
			var oerr *openresponses.Error
			if !errorsAs(err, &oerr) {
				t.Fatalf("err = %v", err)
			}
			if oerr.Type != tc.typ || oerr.Code != tc.code || oerr.HTTPStatus() != tc.status {
				t.Fatalf("err = %+v", oerr)
			}
			if got := oerr.Headers.Get("Retry-After"); got != tc.retry {
				t.Fatalf("Retry-After = %q, want %q", got, tc.retry)
			}
		})
	}
}

func TestRequestReachesModel(t *testing.T) {
	var path string
	var body map[string]any
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&body)
		sse(finishChunk).ServeHTTP(w, r)
	})
	req := hello()
	req.Instructions = "Be terse."
	if _, err := streamtest.Run(context.Background(), newAdapter(t, h), req); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(path, "/models/gemini-2.5-pro:streamGenerateContent") {
		t.Fatalf("path = %q", path)
	}
	if _, ok := body["systemInstruction"]; !ok {
		t.Fatalf("body lacks systemInstruction: %v", body)
	}
}
