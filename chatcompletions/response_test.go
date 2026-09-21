package chatcompletions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ChristopherDavenport/openresponses"
	"github.com/ChristopherDavenport/openresponses/streamtest"
)

// newAdapter serves handler over an in-process server and returns an
// adapter pointed at its /v1.
func newAdapter(t *testing.T, handler http.Handler, opts ...Option) *Adapter {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return New(openresponses.NewClient(srv.URL+"/v1", openresponses.WithAPIKey("k")), opts...)
}

// serve serves each chunk as a data frame and ends with [DONE].
func serve(chunks ...string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, c := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", c)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	})
}

func run(t *testing.T, req openresponses.Request, chunks ...string) *streamtest.Sink {
	t.Helper()
	sink, err := streamtest.Run(context.Background(), newAdapter(t, serve(chunks...)), req)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	return sink
}

func hello() openresponses.Request {
	return openresponses.Request{Model: "deepseek-chat", Input: openresponses.Items{openresponses.UserText("Hi")}}
}

func content(text string) string {
	return fmt.Sprintf(`{"id":"c1","model":"deepseek-chat-2026","choices":[{"index":0,"delta":{"content":%q},"finish_reason":null}]}`, text)
}

func finish(reason string) string {
	return fmt.Sprintf(`{"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":%q}]}`, reason)
}

const (
	roleChunk  = `{"id":"c1","model":"deepseek-chat-2026","choices":[{"index":0,"delta":{"role":"assistant","content":""},"finish_reason":null}]}`
	usageChunk = `{"id":"c1","choices":[],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10,"prompt_tokens_details":{"cached_tokens":2},"completion_tokens_details":{"reasoning_tokens":1}}}`
)

func TestStreamText(t *testing.T) {
	sink := run(t, hello(), roleChunk, content("Hel"), content("lo"), finish("stop"), usageChunk)
	resp := sink.Response()
	if resp.Status != openresponses.ResponseStatusCompleted || resp.OutputText() != "Hello" || len(resp.Output) != 1 {
		t.Fatalf("resp = %+v", resp)
	}
	if resp.Model != "deepseek-chat-2026" {
		t.Fatalf("model = %q", resp.Model)
	}
	u := resp.Usage
	if u == nil || u.InputTokens != 7 || u.OutputTokens != 3 || u.TotalTokens != 10 || u.InputTokensDetails.CachedTokens != 2 || u.OutputTokensDetails.ReasoningTokens != 1 {
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

func TestRequestWire(t *testing.T) {
	var got *http.Request
	var body map[string]any
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(context.Background())
		_ = json.NewDecoder(r.Body).Decode(&body)
		serve(content("ok"), finish("stop")).ServeHTTP(w, r)
	})
	resp, err := newAdapter(t, h).Create(context.Background(), hello())
	if err != nil {
		t.Fatal(err)
	}
	if resp.OutputText() != "ok" {
		t.Fatalf("resp = %+v", resp)
	}
	if got.URL.Path != "/v1/chat/completions" || got.Header.Get("Authorization") != "Bearer k" || got.Header.Get("Accept") != "text/event-stream" {
		t.Fatalf("request = %s %s %v", got.Method, got.URL.Path, got.Header)
	}
	if body["stream"] != true || body["model"] != "deepseek-chat" {
		t.Fatalf("body = %v", body)
	}
}

func TestStreamToolCalls(t *testing.T) {
	sink := run(t, hello(), roleChunk,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"weather","arguments":""}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"city\":"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"Oslo\"}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_2","type":"function","function":{"name":"time","arguments":""}}]}}]}`,
		finish("tool_calls"))
	calls := sink.Response().FunctionCalls()
	if len(calls) != 2 || calls[0].CallID != "call_1" || calls[0].Name != "weather" || calls[0].Arguments != `{"city":"Oslo"}` {
		t.Fatalf("calls = %+v", calls)
	}
	if calls[1].CallID != "call_2" || calls[1].Arguments != "{}" {
		t.Fatalf("empty call = %+v", calls[1])
	}
	var deltas int
	for _, ev := range sink.Events() {
		if _, ok := ev.(*openresponses.FunctionCallArgumentsDeltaEvent); ok {
			deltas++
		}
	}
	if deltas != 3 {
		t.Fatalf("argument deltas = %d, want 3 (two streamed, one synthesized {})", deltas)
	}

	// A call whose name arrives after its first arguments is buffered
	// until the name is known, and one without an id gets one.
	sink = run(t, hello(),
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"a\":"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"late","arguments":"1}"}}]}}]}`,
		finish("tool_calls"))
	calls = sink.Response().FunctionCalls()
	if len(calls) != 1 || calls[0].Name != "late" || calls[0].Arguments != `{"a":1}` || calls[0].CallID == "" {
		t.Fatalf("late-named call = %+v", calls)
	}

	_, err := streamtest.Run(context.Background(), newAdapter(t, serve(
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"b","function":{"name":"b","arguments":"{}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"a","arguments":"{}"}}]}}]}`,
		finish("tool_calls"))), hello())
	wantErr(t, err, "invalid_upstream_response", "")
}

func TestStreamReasoning(t *testing.T) {
	sink := run(t, hello(), roleChunk,
		`{"choices":[{"index":0,"delta":{"reasoning_content":"Think "}}]}`,
		`{"choices":[{"index":0,"delta":{"reasoning_content":"hard."}}]}`,
		content("Answer."), finish("stop"))
	out := sink.Response().Output
	if len(out) != 2 {
		t.Fatalf("output = %d items", len(out))
	}
	rs, ok := out[0].(*openresponses.ReasoningItem)
	if !ok || rs.Content.Text() != "Think hard." || len(rs.Summary) != 0 || rs.EncryptedContent != "" {
		t.Fatalf("reasoning = %+v", out[0])
	}
	if out[1].(*openresponses.Message).Text() != "Answer." {
		t.Fatalf("message = %+v", out[1])
	}

	sink = run(t, hello(), `{"choices":[{"index":0,"delta":{"reasoning":"Hmm."}}]}`, content("Yes."), finish("stop"))
	if rs := sink.Response().Output[0].(*openresponses.ReasoningItem); rs.Content.Text() != "Hmm." {
		t.Fatalf("reasoning field = %+v", rs)
	}

	// A provider that sends reasoning as something other than a string is
	// not mistaken for reasoning text.
	sink = run(t, hello(), `{"choices":[{"index":0,"delta":{"reasoning":{"tokens":3},"content":"Yes."}}]}`, finish("stop"))
	if out := sink.Response().Output; len(out) != 1 || out[0].ItemType() != "message" {
		t.Fatalf("output = %+v", out)
	}
}

func TestStreamRefusalAnnotationsLogprobs(t *testing.T) {
	sink := run(t, hello(),
		`{"choices":[{"index":0,"delta":{"content":"See "},"logprobs":{"content":[{"token":"See","logprob":-0.1,"bytes":[83,101,101],"top_logprobs":[{"token":"See","logprob":-0.1,"bytes":[83,101,101]},{"token":"Look","logprob":-2,"bytes":[76]}]}]}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"the docs.","annotations":[{"type":"url_citation","url_citation":{"url":"https://example.com","title":"Docs","start_index":4,"end_index":12}}]}}]}`,
		finish("stop"))
	text := sink.Response().Output[0].(*openresponses.Message).Content[0].(*openresponses.OutputText)
	if text.Text != "See the docs." || len(text.Annotations) != 1 || len(text.Logprobs) != 1 {
		t.Fatalf("text = %+v", text)
	}
	if a := text.Annotations[0].(*openresponses.URLCitation); a.URL != "https://example.com" || a.StartIndex != 4 || a.EndIndex != 12 {
		t.Fatalf("annotation = %+v", a)
	}
	if lp := text.Logprobs[0]; lp.Token != "See" || len(lp.TopLogprobs) != 2 || lp.TopLogprobs[1].Token != "Look" {
		t.Fatalf("logprobs = %+v", lp)
	}

	sink = run(t, hello(), `{"choices":[{"index":0,"delta":{"refusal":"I cannot."}}]}`, finish("stop"))
	msg := sink.Response().Output[0].(*openresponses.Message)
	if r, ok := msg.Content[0].(*openresponses.Refusal); !ok || r.Refusal != "I cannot." {
		t.Fatalf("refusal = %+v", msg.Content)
	}
}

func TestStreamFinishReasons(t *testing.T) {
	sink := run(t, hello(), content("Once"), finish("length"))
	resp := sink.Response()
	if resp.Status != openresponses.ResponseStatusIncomplete || resp.IncompleteDetails.Reason != openresponses.IncompleteReasonMaxOutputTokens {
		t.Fatalf("length = %+v", resp)
	}
	if msg := resp.Output[0].(*openresponses.Message); msg.Status != openresponses.StatusIncomplete {
		t.Fatalf("message = %+v", msg)
	}
	sink = run(t, hello(), content("I"), finish("content_filter"))
	if r := sink.Response(); r.IncompleteDetails == nil || r.IncompleteDetails.Reason != openresponses.IncompleteReasonContentFilter {
		t.Fatalf("content_filter = %+v", r)
	}

	_, err := streamtest.Run(context.Background(), newAdapter(t, serve(content("x"))), hello())
	wantErr(t, err, "truncated_stream", "")

	_, err = streamtest.Run(context.Background(), newAdapter(t, serve(content("x"), finish("weird"))), hello())
	wantErr(t, err, "weird", "")
	if !openresponses.AsError(err).Is(&openresponses.Error{Type: openresponses.ErrorTypeModelError}) {
		t.Fatalf("err = %v, want model_error", err)
	}

	_, err = streamtest.Run(context.Background(), newAdapter(t, serve(content("x"), `{"error":{"message":"boom","type":"server_error"}}`)), hello())
	wantErr(t, err, "server_error", "")
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
		param   string
		retry   string
	}{
		{"rate limited", 429, `{"error":{"message":"slow","type":"rate_limit_error","code":"rate_limit_exceeded"}}`, map[string]string{"Retry-After": "9", "x-ratelimit-remaining-requests": "0", "x-request-id": "r1"}, openresponses.ErrorTypeTooManyRequests, "rate_limit_exceeded", "slow", "", "9"},
		{"invalid with param", 400, `{"error":{"message":"bad","type":"invalid_request_error","param":"messages","code":null}}`, nil, openresponses.ErrorTypeInvalidRequest, "invalid_request_error", "bad", "messages", ""},
		{"numeric code", 400, `{"error":{"message":"bad","code":20015}}`, nil, openresponses.ErrorTypeInvalidRequest, "20015", "bad", "", ""},
		{"unauthorized", 401, `{"error":{"message":"key","type":"authentication_error"}}`, nil, openresponses.ErrorTypeInvalidRequest, "authentication_error", "key", "", ""},
		{"not found", 404, `{"error":{"message":"no model","code":"model_not_found"}}`, nil, openresponses.ErrorTypeNotFound, "model_not_found", "no model", "", ""},
		{"plain text", 503, `upstream unavailable`, nil, openresponses.ErrorTypeServerError, "http_503", "upstream unavailable", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				for k, v := range tc.headers {
					w.Header().Set(k, v)
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			})
			_, err := streamtest.Run(context.Background(), newAdapter(t, h), hello())
			var oerr *openresponses.Error
			if !errors.As(err, &oerr) {
				t.Fatalf("err = %v", err)
			}
			if oerr.Type != tc.typ || oerr.Code != tc.code || oerr.Message != tc.message || oerr.Param != tc.param || oerr.HTTPStatus() != tc.status {
				t.Fatalf("err = %+v", oerr)
			}
			if got := oerr.Headers.Get("Retry-After"); got != tc.retry {
				t.Fatalf("Retry-After = %q, want %q", got, tc.retry)
			}
			if tc.headers != nil && (oerr.Headers.Get("X-Ratelimit-Remaining-Requests") != "0" || oerr.Headers.Get("X-Request-Id") != "r1") {
				t.Fatalf("headers = %v", oerr.Headers)
			}
		})
	}
}
