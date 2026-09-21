package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ChristopherDavenport/openresponses"
	"github.com/ChristopherDavenport/openresponses/streamtest"
	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

// newAdapter returns an adapter whose client talks to handler over an
// in-process server, without the SDK's retries.
func newAdapter(t *testing.T, handler http.Handler, opts ...Option) *Adapter {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client := sdk.NewClient(option.WithAPIKey("test"), option.WithBaseURL(srv.URL), option.WithMaxRetries(0))
	return New(client.Messages, opts...)
}

// sse serves each event, the event name taken from its type field, the
// way the Messages API streams.
func sse(events ...string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		writeSSE(w, events...)
	})
}

func writeSSE(w http.ResponseWriter, events ...string) {
	for _, ev := range events {
		var head struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal([]byte(ev), &head)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", head.Type, ev)
	}
}

func apiError(status int, body string, headers map[string]string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		for k, v := range headers {
			w.Header().Set(k, v)
		}
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	})
}

func run(t *testing.T, req openresponses.Request, events ...string) *streamtest.Sink {
	t.Helper()
	sink, err := streamtest.Run(context.Background(), newAdapter(t, sse(events...)), req)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	return sink
}

func hello() openresponses.Request {
	return openresponses.Request{Model: "claude-opus-5", Input: openresponses.Items{openresponses.UserText("Hi")}}
}

const (
	msgStart  = `{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5-20260401","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":10,"output_tokens":1,"cache_read_input_tokens":4,"cache_creation_input_tokens":2}}}`
	textStart = `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`
	textStop  = `{"type":"content_block_stop","index":0}`
	endTurn   = `{"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":5,"output_tokens_details":{"thinking_tokens":2}}}`
	msgStop   = `{"type":"message_stop"}`
)

func textDelta(index int, text string) string {
	return fmt.Sprintf(`{"type":"content_block_delta","index":%d,"delta":{"type":"text_delta","text":%q}}`, index, text)
}

func TestStreamText(t *testing.T) {
	sink := run(t, hello(), msgStart, textStart, textDelta(0, "Hel"), textDelta(0, "lo"), textStop, endTurn, msgStop)
	resp := sink.Response()
	if resp.Status != openresponses.ResponseStatusCompleted || resp.OutputText() != "Hello" || len(resp.Output) != 1 {
		t.Fatalf("resp = %+v", resp)
	}
	if resp.Model != "claude-opus-5-20260401" {
		t.Fatalf("model = %q, want the served version", resp.Model)
	}
	u := resp.Usage
	if u == nil || u.InputTokens != 16 || u.InputTokensDetails.CachedTokens != 4 || u.OutputTokens != 5 || u.OutputTokensDetails.ReasoningTokens != 2 || u.TotalTokens != 21 {
		t.Fatalf("usage = %+v", u)
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
	resp, err := newAdapter(t, sse(msgStart, textStart, textDelta(0, "Hello"), textStop, endTurn, msgStop)).Create(context.Background(), hello())
	if err != nil {
		t.Fatal(err)
	}
	if resp.OutputText() != "Hello" || resp.Status != openresponses.ResponseStatusCompleted {
		t.Fatalf("resp = %+v", resp)
	}
}

func TestStreamToolUse(t *testing.T) {
	sink := run(t, hello(), msgStart,
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"weather","input":{}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"city\":"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"Oslo\"}"}}`,
		textStop,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_2","name":"ping","input":{}}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":9}}`, msgStop)
	calls := sink.Response().FunctionCalls()
	if len(calls) != 2 || calls[0].CallID != "toolu_1" || calls[0].Name != "weather" || calls[0].Arguments != `{"city":"Oslo"}` {
		t.Fatalf("calls = %+v", calls)
	}
	if calls[1].CallID != "toolu_2" || calls[1].Arguments != "{}" {
		t.Fatalf("empty call = %+v", calls[1])
	}
	var deltas []string
	for _, ev := range sink.Events() {
		if d, ok := ev.(*openresponses.FunctionCallArgumentsDeltaEvent); ok {
			deltas = append(deltas, d.Delta)
		}
	}
	if len(deltas) != 3 {
		t.Fatalf("argument deltas = %q", deltas)
	}
}

func TestThinkingRoundTrip(t *testing.T) {
	sink := run(t, hello(), msgStart,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me "}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"think."}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"c2lnbmF0dXJl"}}`,
		textStop,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"f","input":{}}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use","stop_sequence":null},"usage":{"output_tokens":9}}`, msgStop)
	out := sink.Response().Output
	if len(out) != 2 {
		t.Fatalf("output = %d items", len(out))
	}
	rs, ok := out[0].(*openresponses.ReasoningItem)
	if !ok || rs.Summary.Text() != "Let me think." || rs.EncryptedContent != "c2lnbmF0dXJl" {
		t.Fatalf("reasoning = %+v", out[0])
	}
	messages, _, err := encodeInput(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || len(messages[0].Content) != 2 {
		t.Fatalf("messages = %+v", messages)
	}
	th := messages[0].Content[0].OfThinking
	if th == nil || th.Thinking != "Let me think." || th.Signature != "c2lnbmF0dXJl" {
		t.Fatalf("thinking block = %+v", messages[0].Content[0])
	}
	if messages[0].Content[1].OfToolUse == nil {
		t.Fatalf("tool use = %+v", messages[0].Content[1])
	}
}

func TestStreamExtensionBlocks(t *testing.T) {
	sink := run(t, hello(), msgStart,
		`{"type":"content_block_start","index":0,"content_block":{"type":"redacted_thinking","data":"opaque"}}`,
		textStop,
		`{"type":"content_block_start","index":1,"content_block":{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{}}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"go\"}"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"web_search_tool_result","tool_use_id":"srvtoolu_1","content":[{"type":"web_search_result","url":"https://go.dev","title":"Go"}]}}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"content_block_start","index":3,"content_block":{"type":"text","text":""}}`,
		textDelta(3, "Go is a language."),
		`{"type":"content_block_stop","index":3}`,
		endTurn, msgStop)
	out := sink.Response().Output
	var types []string
	for _, item := range out {
		types = append(types, item.ItemType())
	}
	if strings.Join(types, ",") != "anthropic.redacted_thinking,anthropic.server_tool_use,anthropic.web_search_tool_result,message" {
		t.Fatalf("items = %v", types)
	}
	var wire struct {
		Block map[string]any `json:"block"`
	}
	if err := json.Unmarshal(out[1].(*openresponses.UnknownItem).Raw, &wire); err != nil {
		t.Fatal(err)
	}
	if wire.Block["type"] != "server_tool_use" || wire.Block["input"].(map[string]any)["query"] != "go" {
		t.Fatalf("server_tool_use block = %v, want the input from the deltas", wire.Block)
	}

	// The whole output replays as one assistant message in the API's own
	// block shapes.
	messages, _, err := encodeInput(out)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(messages)
	if err != nil {
		t.Fatal(err)
	}
	var replayed []map[string]any
	if err := json.Unmarshal(data, &replayed); err != nil {
		t.Fatal(err)
	}
	blocks := replayed[0]["content"].([]any)
	if len(replayed) != 1 || replayed[0]["role"] != "assistant" || len(blocks) != 4 {
		t.Fatalf("replayed = %s", data)
	}
	if b := blocks[0].(map[string]any); b["type"] != "redacted_thinking" || b["data"] != "opaque" {
		t.Fatalf("redacted thinking = %v", b)
	}
	if b := blocks[2].(map[string]any); b["type"] != "web_search_tool_result" || b["tool_use_id"] != "srvtoolu_1" {
		t.Fatalf("tool result = %v", b)
	}
}

func TestStreamCitations(t *testing.T) {
	sink := run(t, hello(), msgStart,
		textStart, textDelta(0, "Café: "), textStop,
		`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		textDelta(1, "nice place"),
		`{"type":"content_block_delta","index":1,"delta":{"type":"citations_delta","citation":{"type":"web_search_result_location","url":"https://example.com/a","title":"A","cited_text":"nice place","encrypted_index":"x"}}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"citations_delta","citation":{"type":"char_location","document_index":0,"document_title":"Doc","cited_text":"nice place","start_char_index":3,"end_char_index":13}}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`,
		textDelta(2, "."),
		`{"type":"content_block_stop","index":2}`,
		endTurn, msgStop)
	out := sink.Response().Output
	if len(out) != 1 {
		t.Fatalf("text blocks should merge into one message, got %d items", len(out))
	}
	text := out[0].(*openresponses.Message).Content[0].(*openresponses.OutputText)
	if text.Text != "Café: nice place." || len(text.Annotations) != 2 {
		t.Fatalf("text = %q annotations = %d", text.Text, len(text.Annotations))
	}
	url, ok := text.Annotations[0].(*openresponses.URLCitation)
	if !ok || url.URL != "https://example.com/a" || url.Title != "A" || url.StartIndex != 6 || url.EndIndex != 16 {
		t.Fatalf("url citation = %+v", text.Annotations[0])
	}
	if runes := []rune(text.Text); string(runes[6:16]) != "nice place" {
		t.Fatalf("offsets select %q", string(runes[6:16]))
	}
	other, ok := text.Annotations[1].(*openresponses.UnknownAnnotation)
	if !ok || other.Type != "anthropic.char_location" || !strings.Contains(string(other.Raw), `"document_title":"Doc"`) {
		t.Fatalf("document citation = %+v", text.Annotations[1])
	}
}

func TestStreamStopReasons(t *testing.T) {
	sink := run(t, hello(), msgStart, textStart, textDelta(0, "Once upon"), textStop,
		`{"type":"message_delta","delta":{"stop_reason":"max_tokens","stop_sequence":null},"usage":{"output_tokens":5}}`, msgStop)
	resp := sink.Response()
	if resp.Status != openresponses.ResponseStatusIncomplete || resp.IncompleteDetails.Reason != openresponses.IncompleteReasonMaxOutputTokens {
		t.Fatalf("max_tokens = %+v", resp.IncompleteDetails)
	}
	if msg := resp.Output[0].(*openresponses.Message); msg.Status != openresponses.StatusIncomplete {
		t.Fatalf("message = %+v", msg)
	}

	sink = run(t, hello(), msgStart,
		`{"type":"message_delta","delta":{"stop_reason":"refusal","stop_sequence":null,"stop_details":{"type":"refusal","category":"cyber","explanation":"No."}},"usage":{"output_tokens":1}}`, msgStop)
	resp = sink.Response()
	msg := resp.Output[0].(*openresponses.Message)
	if resp.Status != openresponses.ResponseStatusCompleted || len(msg.Content) != 1 {
		t.Fatalf("refusal = %+v", resp)
	}
	if r, ok := msg.Content[0].(*openresponses.Refusal); !ok || r.Refusal != "No." {
		t.Fatalf("refusal part = %+v", msg.Content[0])
	}

	_, err := streamtest.Run(context.Background(), newAdapter(t, sse(msgStart, textStart, textDelta(0, "x"), textStop)), hello())
	wantErr(t, err, "truncated_stream", "")
}

func TestPauseTurnContinues(t *testing.T) {
	var mu sync.Mutex
	var bodies []map[string]any
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		bodies = append(bodies, body)
		n := len(bodies)
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			writeSSE(w, msgStart,
				`{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{"query":"go"}}}`,
				textStop,
				`{"type":"message_delta","delta":{"stop_reason":"pause_turn","stop_sequence":null},"usage":{"output_tokens":3}}`, msgStop)
			return
		}
		writeSSE(w, msgStart, textStart, textDelta(0, "Done."), textStop, endTurn, msgStop)
	})
	sink, err := streamtest.Run(context.Background(), newAdapter(t, h), hello())
	if err != nil {
		t.Fatal(err)
	}
	resp := sink.Response()
	if len(bodies) != 2 {
		t.Fatalf("got %d requests, want 2", len(bodies))
	}
	messages := bodies[1]["messages"].([]any)
	last := messages[len(messages)-1].(map[string]any)
	block := last["content"].([]any)[0].(map[string]any)
	if last["role"] != "assistant" || block["type"] != "server_tool_use" || block["id"] != "srvtoolu_1" {
		t.Fatalf("second request should replay the paused turn, got %v", last)
	}
	if resp.Status != openresponses.ResponseStatusCompleted || len(resp.Output) != 2 || resp.OutputText() != "Done." {
		t.Fatalf("resp = %+v", resp)
	}
	if resp.Usage.OutputTokens != 8 || resp.Usage.InputTokens != 32 {
		t.Fatalf("usage should sum both turns: %+v", resp.Usage)
	}

	// With continuations disabled the paused output completes as-is.
	sink, err = streamtest.Run(context.Background(), newAdapter(t, sse(msgStart,
		`{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search","input":{}}}`,
		textStop,
		`{"type":"message_delta","delta":{"stop_reason":"pause_turn","stop_sequence":null},"usage":{"output_tokens":3}}`, msgStop), WithContinuations(0)), hello())
	if err != nil {
		t.Fatal(err)
	}
	if r := sink.Response(); r.Status != openresponses.ResponseStatusCompleted || len(r.Output) != 1 {
		t.Fatalf("resp = %+v", r)
	}
}

func TestMapError(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		headers map[string]string
		typ     openresponses.ErrorType
		code    string
		message string
		retry   string
	}{
		{"rate limited", 429, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`, map[string]string{"Retry-After": "9", "anthropic-ratelimit-requests-remaining": "0", "request-id": "req_1"}, openresponses.ErrorTypeTooManyRequests, "rate_limit_error", "slow down", "9"},
		{"invalid", 400, `{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`, nil, openresponses.ErrorTypeInvalidRequest, "invalid_request_error", "bad", ""},
		{"authentication", 401, `{"type":"error","error":{"type":"authentication_error","message":"key"}}`, nil, openresponses.ErrorTypeInvalidRequest, "authentication_error", "key", ""},
		{"not found", 404, `{"type":"error","error":{"type":"not_found_error","message":"no model"}}`, nil, openresponses.ErrorTypeNotFound, "not_found_error", "no model", ""},
		{"overloaded", 529, `{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`, nil, openresponses.ErrorTypeServerError, "overloaded_error", "busy", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := streamtest.Run(context.Background(), newAdapter(t, apiError(tc.status, tc.body, tc.headers)), hello())
			var oerr *openresponses.Error
			if !errorsAs(err, &oerr) {
				t.Fatalf("err = %v", err)
			}
			if oerr.Type != tc.typ || oerr.Code != tc.code || oerr.Message != tc.message || oerr.HTTPStatus() != tc.status {
				t.Fatalf("err = %+v", oerr)
			}
			if got := oerr.Headers.Get("Retry-After"); got != tc.retry {
				t.Fatalf("Retry-After = %q, want %q", got, tc.retry)
			}
			if tc.headers != nil && (oerr.Headers.Get("Anthropic-Ratelimit-Requests-Remaining") != "0" || oerr.Headers.Get("Request-Id") != "req_1") {
				t.Fatalf("headers = %v", oerr.Headers)
			}
		})
	}
}
