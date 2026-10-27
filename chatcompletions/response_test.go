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

// Cache writes become the shared member, and the members the mapping
// does not read are kept at the depth they arrived at.
func TestStreamUsageCacheAndExtra(t *testing.T) {
	// OpenRouter.
	sink := run(t, hello(), content("Hi"), finish("stop"),
		`{"choices":[],"usage":{"prompt_tokens":12000,"completion_tokens":300,"total_tokens":12300,"cost":0.0123,"is_byok":false,"prompt_tokens_details":{"cached_tokens":9600,"cache_write_tokens":2100,"audio_tokens":0},"completion_tokens_details":{"reasoning_tokens":5,"image_tokens":0}}}`)
	u := sink.Response().Usage
	if u.InputTokens != 12000 || u.InputTokensDetails.CachedTokens != 9600 || u.OutputTokensDetails.ReasoningTokens != 5 {
		t.Fatalf("usage = %+v", u)
	}
	sameJSON(t, u, `{
		"input_tokens":12000,"output_tokens":300,"total_tokens":12300,"cost":0.0123,"is_byok":false,
		"input_tokens_details":{"cached_tokens":9600,"audio_tokens":0,"openresponses-experimental.cache_write_tokens":2100},
		"output_tokens_details":{"reasoning_tokens":5,"image_tokens":0}
	}`)
	if n, ok := u.InputTokensDetails.CacheWriteTokens(); !ok || n != 2100 {
		t.Fatalf("cache writes = %d %v", n, ok)
	}

	// DeepSeek reports cache reads only, outside prompt_tokens_details.
	sink = run(t, hello(), content("Hi"), finish("stop"),
		`{"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":10,"total_tokens":110,"prompt_cache_hit_tokens":64,"prompt_cache_miss_tokens":36}}`)
	u = sink.Response().Usage
	if u.InputTokensDetails.CachedTokens != 64 || u.Extra["prompt_cache_miss_tokens"] == nil {
		t.Fatalf("usage = %+v", u)
	}
	if _, ok := u.InputTokensDetails.CacheWriteTokens(); ok {
		t.Fatalf("cache writes reported for DeepSeek: %+v", u.InputTokensDetails)
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

	// A tool call the limit cuts off is incomplete, in output_item.done
	// and in the response; one the model finishes is completed.
	weather := `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"weather","arguments":"{\"city\":\"Os"}}]}}]}`
	for _, tc := range []struct {
		reason string
		want   openresponses.Status
	}{
		{"length", openresponses.StatusIncomplete},
		{"content_filter", openresponses.StatusIncomplete},
		{"tool_calls", openresponses.StatusCompleted},
	} {
		sink = run(t, hello(), roleChunk, weather, finish(tc.reason))
		call := sink.Response().Output[0].(*openresponses.FunctionCall)
		if call.Status != tc.want || call.Arguments != `{"city":"Os` {
			t.Fatalf("%s: call = %+v", tc.reason, call)
		}
		var done openresponses.Status
		for _, ev := range sink.Events() {
			if d, ok := ev.(*openresponses.OutputItemDoneEvent); ok {
				done = d.Item.(*openresponses.FunctionCall).Status
			}
		}
		if done != tc.want {
			t.Fatalf("%s: output_item.done status = %q, want %q", tc.reason, done, tc.want)
		}
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

// A mid-stream error frame maps as the same error would on a
// non-streamed response, never with the stream's 2xx.
func TestStreamErrorFrame(t *testing.T) {
	cases := []struct {
		frame  string
		typ    openresponses.ErrorType
		code   string
		status int
	}{
		{`{"error":{"message":"slow","type":"requests","code":"rate_limit_exceeded"}}`, openresponses.ErrorTypeTooManyRequests, "rate_limit_exceeded", 429},
		{`{"error":{"message":"slow","type":"rate_limit_error"}}`, openresponses.ErrorTypeTooManyRequests, "rate_limit_error", 429},
		{`{"error":{"message":"busy","type":"overloaded_error"}}`, openresponses.ErrorTypeServerError, "overloaded_error", 529},
		{`{"error":{"message":"boom","type":"server_error"}}`, openresponses.ErrorTypeServerError, "server_error", 500},
		{`{"error":{"message":"bad","type":"invalid_request_error","code":"invalid_value"}}`, openresponses.ErrorTypeInvalidRequest, "invalid_value", 400},
		{`{"error":{"message":"key","type":"authentication_error"}}`, openresponses.ErrorTypeInvalidRequest, "authentication_error", 401},
		{`{"error":{"message":"denied","type":"permission_error"}}`, openresponses.ErrorTypeInvalidRequest, "permission_error", 403},
		{`{"error":{"message":"upstream","code":502}}`, openresponses.ErrorTypeServerError, "502", 502},
		{`{"error":{"message":"slow","code":429}}`, openresponses.ErrorTypeTooManyRequests, "429", 429},
		{`{"error":{"message":"what"}}`, openresponses.ErrorTypeServerError, "upstream_error", 500},
		{`{"error":{"message":"This model's maximum context length is 8192 tokens.","type":"invalid_request_error","code":"context_length_exceeded"}}`, openresponses.ErrorTypeInvalidRequest, openresponses.CodeContextLengthExceeded, 400},
		{`{"error":{"message":"This endpoint's maximum context length is 8192 tokens.","code":400}}`, openresponses.ErrorTypeInvalidRequest, openresponses.CodeContextLengthExceeded, 400},
		{`{"error":{"message":"This model's maximum context length is 4096 tokens."}}`, openresponses.ErrorTypeInvalidRequest, openresponses.CodeContextLengthExceeded, 400},
		{`{"error":{"message":"Your request timed out","code":408}}`, openresponses.ErrorTypeServerError, "408", 408},
		{`{"error":{"message":"busy","code":"503"}}`, openresponses.ErrorTypeServerError, "503", 503},
		{`{"error":{"message":"bad","code":"400"}}`, openresponses.ErrorTypeInvalidRequest, "400", 400},
	}
	for _, tc := range cases {
		t.Run(tc.frame, func(t *testing.T) {
			_, err := streamtest.Run(context.Background(), newAdapter(t, serve(content("x"), tc.frame)), hello())
			var oerr *openresponses.Error
			if !errors.As(err, &oerr) {
				t.Fatalf("err = %v", err)
			}
			if oerr.Type != tc.typ || oerr.Code != tc.code || oerr.HTTPStatus() != tc.status {
				t.Fatalf("err = %s/%s %d, want %s/%s %d", oerr.Type, oerr.Code, oerr.HTTPStatus(), tc.typ, tc.code, tc.status)
			}
		})
	}
}

// The envelope's type, and a code the adapter replaced, are kept under
// the adapter's slug.
func TestErrorKeepsUpstreamCodeAndType(t *testing.T) {
	_, err := streamtest.Run(context.Background(), newAdapter(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		fmt.Fprint(w, `{"error":{"code":400,"message":"the request exceeds the available context size, try increasing it","type":"exceed_context_size_error"}}`)
	})), hello())
	oerr := openresponses.AsError(err)
	if oerr.Code != openresponses.CodeContextLengthExceeded || oerr.Extra["chatcompletions.code"] != "400" || oerr.Extra["chatcompletions.type"] != "exceed_context_size_error" {
		t.Fatalf("err = %+v", oerr)
	}
	_, err = streamtest.Run(context.Background(), newAdapter(t, serve(content("x"), `{"error":{"message":"bad","type":"invalid_request_error","code":"invalid_value"}}`)), hello())
	oerr = openresponses.AsError(err)
	if oerr.Code != "invalid_value" || oerr.Extra["chatcompletions.type"] != "invalid_request_error" {
		t.Fatalf("err = %+v", oerr)
	}
	if _, replaced := oerr.Extra["chatcompletions.code"]; replaced {
		t.Fatalf("code kept although not replaced: %+v", oerr.Extra)
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
		param   string
		retry   string
	}{
		{"rate limited", 429, `{"error":{"message":"slow","type":"rate_limit_error","code":"rate_limit_exceeded"}}`, map[string]string{"Retry-After": "9", "x-ratelimit-remaining-requests": "0", "x-request-id": "r1"}, openresponses.ErrorTypeTooManyRequests, "rate_limit_exceeded", "slow", "", "9"},
		{"invalid with param", 400, `{"error":{"message":"bad","type":"invalid_request_error","param":"messages","code":null}}`, nil, openresponses.ErrorTypeInvalidRequest, "invalid_request_error", "bad", "messages", ""},
		{"numeric code", 400, `{"error":{"message":"bad","code":20015}}`, nil, openresponses.ErrorTypeInvalidRequest, "20015", "bad", "", ""},
		{"unauthorized", 401, `{"error":{"message":"key","type":"authentication_error"}}`, nil, openresponses.ErrorTypeInvalidRequest, "authentication_error", "key", "", ""},
		{"forbidden", 403, `{"error":{"message":"model not allowed","type":"permission_error","code":"model_not_allowed"}}`, nil, openresponses.ErrorTypeInvalidRequest, "model_not_allowed", "model not allowed", "", ""},
		{"not found", 404, `{"error":{"message":"no model","code":"model_not_found"}}`, nil, openresponses.ErrorTypeNotFound, "model_not_found", "no model", "", ""},
		{"openai overflow", 400, `{"error":{"message":"This model's maximum context length is 128000 tokens.","type":"invalid_request_error","param":"messages","code":"context_length_exceeded"}}`, nil, openresponses.ErrorTypeInvalidRequest, openresponses.CodeContextLengthExceeded, "This model's maximum context length is 128000 tokens.", "messages", ""},
		{"vllm overflow", 400, `{"object":"error","message":"This model's maximum context length is 4096 tokens. However, you requested 5000 tokens.","type":"BadRequestError","param":null,"code":400}`, nil, openresponses.ErrorTypeInvalidRequest, openresponses.CodeContextLengthExceeded, "", "", ""},
		{"llama.cpp overflow", 400, `{"error":{"code":400,"message":"the request exceeds the available context size, try increasing it","type":"exceed_context_size_error"}}`, nil, openresponses.ErrorTypeInvalidRequest, openresponses.CodeContextLengthExceeded, "the request exceeds the available context size, try increasing it", "", ""},
		{"mistral overflow", 400, `{"object":"error","message":"Prompt contains 40000 tokens and 0 draft tokens, too large for model with 32768 maximum context length","type":"invalid_request_error","param":null,"code":null}`, nil, openresponses.ErrorTypeInvalidRequest, openresponses.CodeContextLengthExceeded, "", "", ""},
		{"claude overflow", 400, `{"error":{"message":"prompt is too long: 210000 tokens > 200000 maximum","type":"invalid_request_error"}}`, nil, openresponses.ErrorTypeInvalidRequest, openresponses.CodeContextLengthExceeded, "prompt is too long: 210000 tokens > 200000 maximum", "", ""},
		{"gemini overflow", 400, `[{"error":{"code":400,"message":"The input token count (1200000) exceeds the maximum number of tokens allowed (1048576).","status":"INVALID_ARGUMENT"}}]`, nil, openresponses.ErrorTypeInvalidRequest, openresponses.CodeContextLengthExceeded, "", "", ""},
		{"vllm overflow by input", 400, `{"object":"error","message":"This model's maximum context length is 4096 tokens. However, your request has 5000 input tokens. Please reduce the length of the input messages.","type":"BadRequestError","param":null,"code":400}`, nil, openresponses.ErrorTypeInvalidRequest, openresponses.CodeContextLengthExceeded, "", "", ""},
		// A request whose input fits but whose output limit does not is
		// not an overflow: compacting the input cannot help it.
		{"vllm max_tokens too large", 400, `{"object":"error","message":"'max_tokens' or 'max_completion_tokens' is too large: 5000. This model's maximum context length is 4096 tokens and your request has 100 input tokens (5000 > 4096 - 100).","type":"BadRequestError","param":null,"code":400}`, nil, openresponses.ErrorTypeInvalidRequest, "http_400", "", "", ""},
		{"vllm completion too large", 400, `{"object":"error","message":"This model's maximum context length is 4096 tokens. However, you requested 5100 tokens (100 in the messages, 5000 in the completion). Please reduce the length of the messages or completion.","type":"BadRequestError","param":null,"code":400}`, nil, openresponses.ErrorTypeInvalidRequest, "http_400", "", "", ""},
		{"openrouter output too large", 400, `{"error":{"message":"This endpoint's maximum context length is 8192 tokens. However, you requested about 9000 tokens (8000 of text input, 1000 in the output). Please reduce the length of either one.","code":400}}`, nil, openresponses.ErrorTypeInvalidRequest, "400", "", "", ""},
		{"deepseek overflow", 400, `{"error":{"message":"This model's maximum context length is 131072 tokens. However, you requested 140000 tokens (135000 in the messages, 5000 in the completion).","type":"invalid_request_error","param":null,"code":"invalid_request_error"}}`, nil, openresponses.ErrorTypeInvalidRequest, openresponses.CodeContextLengthExceeded, "", "", ""},
		// The status decides the type as the root Client decides it, so a
		// timeout stays retryable.
		{"timeout", 408, `{"error":{"message":"Your request timed out","code":408}}`, nil, openresponses.ErrorTypeServerError, "408", "Your request timed out", "", ""},
		{"unprocessable", 422, `{"error":{"message":"bad","type":"invalid_request_error"}}`, nil, openresponses.ErrorTypeInvalidRequest, "invalid_request_error", "bad", "", ""},
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
			if oerr.Type != tc.typ || oerr.Code != tc.code || (tc.message != "" && oerr.Message != tc.message) || oerr.Param != tc.param || oerr.HTTPStatus() != tc.status {
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
