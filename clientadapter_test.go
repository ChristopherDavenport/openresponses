package openresponses

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

// proxyChain serves upstream through a Handler, then serves a Handler
// backed by a client of that server, and returns a client of the proxy.
func proxyChain(t *testing.T, upstream Adapter, opts ...HandlerOption) *Client {
	t.Helper()
	origin := httptest.NewServer(NewHandler(upstream))
	t.Cleanup(origin.Close)
	proxy := httptest.NewServer(NewHandler(NewClient(origin.URL).AsAdapter(), opts...))
	t.Cleanup(proxy.Close)
	return NewClient(proxy.URL)
}

func TestClientAdapterProxy(t *testing.T) {
	ctx := testContext(t)
	c := proxyChain(t, echoAdapter{})

	resp, err := c.Create(ctx, Request{Model: "m", Input: Items{UserText("hello")}})
	if err != nil || resp.OutputText() != "hello" {
		t.Fatalf("Create: %v %+v", err, resp)
	}

	stream, err := c.CreateStream(ctx, Request{Model: "m", Input: Items{UserText("streamed")}})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	var seq int64
	n := 0
	for ev := range stream.Events() {
		if ev.Sequence() != seq {
			t.Errorf("sequence %d, want %d", ev.Sequence(), seq)
		}
		seq++
		n++
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	if n != 4 || stream.Response().OutputText() != "streamed" {
		t.Errorf("n=%d response=%+v", n, stream.Response())
	}

	compact, err := c.Compact(ctx, CompactRequest{Model: "m", Input: Items{UserText("x")}})
	if err != nil || len(compact.Output) != 1 {
		t.Fatalf("Compact: %v %+v", err, compact)
	}

}

func TestClientAdapterErrorFidelity(t *testing.T) {
	ctx := testContext(t)
	limited := &fakeAdapter{
		create: func(context.Context, Request) (*Response, error) {
			return nil, TooManyRequests("rate_limited", "slow down").WithHeader("Retry-After", "7")
		},
		stream: func(_ context.Context, req Request, sink EventSink) error {
			if err := sink.Send(&ResponseCreatedEvent{Response: NewResponse(req)}); err != nil {
				return err
			}
			return ModelError("upstream_down", "model unavailable")
		},
	}
	c := proxyChain(t, limited)

	_, err := c.Create(ctx, Request{Model: "m", Input: Items{UserText("x")}})
	var e *Error
	if !errors.As(err, &e) || e.StatusCode != 429 || e.Code != "rate_limited" || e.Headers.Get("Retry-After") != "7" {
		t.Fatalf("Create error = %v", err)
	}

	// A mid-stream upstream failure arrives downstream as exactly one
	// error event followed by response.failed.
	stream, err := c.CreateStream(ctx, Request{Model: "m", Input: Items{UserText("x")}})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	var types []string
	for ev := range stream.Events() {
		types = append(types, ev.EventType())
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{EventResponseCreated, EventError, EventResponseFailed}
	if len(types) != len(want) {
		t.Fatalf("events = %v", types)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Errorf("event %d = %s, want %s", i, types[i], want[i])
		}
	}
	if final := stream.Response(); final.Error == nil || final.Error.Code != "upstream_down" || final.Error.Type != ErrorTypeModelError {
		t.Errorf("final = %+v", final)
	}
}

func TestClientAdapterTruncatedUpstream(t *testing.T) {
	ctx := testContext(t)
	// An upstream that emits an error event and drops the connection.
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"r\"}}\n\n")
		_, _ = io.WriteString(w, "event: error\ndata: {\"type\":\"error\",\"sequence_number\":1,\"error\":{\"type\":\"too_many_requests\",\"code\":\"quota\",\"message\":\"out\",\"param\":null}}\n\n")
	}))
	defer origin.Close()
	proxy := httptest.NewServer(NewHandler(NewClient(origin.URL).AsAdapter()))
	defer proxy.Close()

	stream, err := NewClient(proxy.URL).CreateStream(ctx, Request{Model: "m", Input: Items{UserText("x")}})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	final, err := stream.Wait()
	if !errors.Is(err, &Error{Code: "quota"}) {
		t.Fatalf("err = %v", err)
	}
	if final == nil || final.Status != ResponseStatusFailed || final.Error.Code != "quota" {
		t.Errorf("final = %+v", final)
	}
	// The downstream stream is still well-formed: it ends with
	// response.failed and [DONE].
	res, err := http.Post(proxy.URL+"/responses", "application/json", jsonBody(Request{Model: "m", Input: Items{UserText("x")}, Stream: true}))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	frames, done := readFrames(t, string(body))
	if !done || frames[len(frames)-1].Event != EventResponseFailed {
		t.Errorf("frames = %+v done=%v", frames, done)
	}
}

func jsonBody(v any) io.Reader {
	data, _ := json.Marshal(v)
	return bytesReader(data)
}

// TestClientAdapterFiltersForwardedErrorHeaders checks that an upstream
// error event reaches the sink with the same header filter every *Error
// path applies: only failure headers in headers, and no other spelling
// of headers in the payload's Extra.
func TestClientAdapterFiltersForwardedErrorHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: error\ndata: "+
			`{"type":"error","sequence_number":0,"error":{"type":"too_many_requests","code":"c","message":"m","param":null,`+
			`"headers":{"Retry-After":"3","Set-Cookie":"secret"},"Headers":{"set-cookie":"secret"},"x:hint":"kept"}}`+"\n\n")
	}))
	defer upstream.Close()
	var got *ErrorEvent
	_ = NewClient(upstream.URL).AsAdapter().CreateStream(testContext(t), Request{Model: "m"}, EventSinkFunc(func(ev StreamEvent) error {
		if e, ok := ev.(*ErrorEvent); ok {
			got = e
		}
		return nil
	}))
	if got == nil {
		t.Fatal("no error event forwarded")
	}
	out, err := json.Marshal(got.Error)
	if err != nil {
		t.Fatal(err)
	}
	assertSameJSON(t, []byte(`{"type":"too_many_requests","code":"c","message":"m","param":null,"headers":{"Retry-After":"3"},"x:hint":"kept"}`), out)
}

// captureUpstream serves a server that records the input of every
// request it receives and refuses each one, and returns a client of it
// and the inputs it saw.
func captureUpstream(t *testing.T) (*Client, *[]json.RawMessage) {
	t.Helper()
	var seen []json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Input json.RawMessage `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		seen = append(seen, body.Input)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"type":"invalid_request","code":"seen","message":"seen","param":null}}`)
	}))
	t.Cleanup(srv.Close)
	return NewClient(srv.URL), &seen
}

// adapterCalls runs input through each of a ClientAdapter's three
// methods.
var adapterCalls = []struct {
	name string
	call func(ctx context.Context, a *ClientAdapter, input Items) error
}{
	{"Create", func(ctx context.Context, a *ClientAdapter, input Items) error {
		_, err := a.Create(ctx, Request{Model: "m", Input: input})
		return err
	}},
	{"CreateStream", func(ctx context.Context, a *ClientAdapter, input Items) error {
		return a.CreateStream(ctx, Request{Model: "m", Input: input}, EventSinkFunc(func(StreamEvent) error { return nil }))
	}},
	{"Compact", func(ctx context.Context, a *ClientAdapter, input Items) error {
		_, err := a.Compact(ctx, CompactRequest{Model: "m", Input: input})
		return err
	}},
}

// TestClientAdapterSidecarMedia pins docs/extensions.md section 1.7 for a
// pass-through: a sidecar: reference is refused naming the field, before
// anything reaches the upstream, whatever slugs the adapter accepts.
func TestClientAdapterSidecarMedia(t *testing.T) {
	const ref = "sidecar:sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
	output := func(c Content) Item {
		return &FunctionCallOutput{CallID: "c", Output: FunctionCallOutputData{Parts: Contents{&InputText{Text: "x"}, c}}}
	}
	cases := []struct {
		name  string
		item  Item
		param string
	}{
		{"image_url", UserMessage(&InputImage{ImageURL: ref}), "input[1].content[0].image_url"},
		{"file_url", UserMessage(&InputFile{FileURL: ref}), "input[1].content[0].file_url"},
		{"file_data", UserMessage(&InputFile{Filename: "a.pdf", FileData: ref}), "input[1].content[0].file_data"},
		{"video_url", UserMessage(&InputVideo{VideoURL: ref}), "input[1].content[0].video_url"},
		{"upper-case scheme", UserMessage(&InputImage{ImageURL: "SIDECAR:sha256:00"}), "input[1].content[0].image_url"},
		{"padded image_url", UserMessage(&InputImage{ImageURL: " \t" + ref + "\n"}), "input[1].content[0].image_url"},
		{"padded video_url", UserMessage(&InputVideo{VideoURL: " " + ref}), "input[1].content[0].video_url"},
		{"tool output image", output(&InputImage{ImageURL: ref}), "input[1].output[1].image_url"},
		{"tool output file", output(&InputFile{Filename: "a.pdf", FileData: ref}), "input[1].output[1].file_data"},
	}
	for _, call := range adapterCalls {
		for _, tc := range cases {
			t.Run(call.name+"/"+tc.name, func(t *testing.T) {
				client, seen := captureUpstream(t)
				err := call.call(testContext(t), client.AsAdapter(WithProviderSlugs("anthropic")), Items{UserText("hi"), tc.item})
				var oerr *Error
				if !errors.As(err, &oerr) || oerr.Type != ErrorTypeInvalidRequest || oerr.Code != CodeInvalidValue || oerr.Param != tc.param || !contains(oerr.Message, "sidecar: reference") {
					t.Fatalf("err = %v, want invalid_request/invalid_value on %s naming the sidecar: reference", err, tc.param)
				}
				if len(*seen) != 0 {
					t.Fatalf("upstream received %s", (*seen)[0])
				}
			})
		}
	}
}

// TestClientAdapterForeignSlug pins docs/extensions.md section 1.6 (and
// section 4's rule 2) for a pass-through: the caller names the slugs its
// upstream accepts, and items, content parts and reasoning under any
// other provider's slug are left out; naming none filters every
// provider's. Without the option the adapter cannot know its upstream
// and forwards everything, as before. Shared and non-provider slugs
// pass unchanged, and the caller's input is not changed.
func TestClientAdapterForeignSlug(t *testing.T) {
	part := func(typ string) Content {
		return &UnknownContent{Type: typ, Raw: json.RawMessage(`{"type":"` + typ + `"}`)}
	}
	item := func(typ string) Item { return &UnknownItem{Type: typ, Raw: json.RawMessage(`{"type":"` + typ + `"}`)} }
	reasoning := func(issuer string) Item {
		r := &ReasoningItem{ID: "rs_" + issuer, Summary: Contents{}, EncryptedContent: "c2ln"}
		r.SetIssuer(issuer)
		return r
	}
	input := func() Items {
		return Items{
			UserMessage(&InputText{Text: "hi"}, part("anthropic.tool_reference"), part("gemini.thought")),
			item("anthropic.redacted_thinking"),
			item("gemini.executable_code"),
			item("chatcompletions.note"),
			item("acme.thing"),
			item("openresponses-experimental.note"),
			reasoning("anthropic"),
			reasoning("anthropic/other"),
			reasoning("gemini"),
			reasoning(""),
			&FunctionCallOutput{CallID: "c", Output: FunctionCallOutputData{Parts: Contents{&InputText{Text: "1"}, part("gemini.code_execution_result")}}},
			UserMessage(part("gemini.thought")),
		}
	}
	// summary names each item the upstream received: its type, a
	// reasoning item's issuer, and the types of a message's or an
	// output's parts.
	summary := func(t *testing.T, raw json.RawMessage) []string {
		t.Helper()
		var items []struct {
			Type    string `json:"type"`
			Issuer  string `json:"openresponses-experimental.issuer"`
			Content []struct {
				Type string `json:"type"`
			} `json:"content"`
			Output json.RawMessage `json:"output"`
		}
		if err := json.Unmarshal(raw, &items); err != nil {
			t.Fatalf("input %s: %v", raw, err)
		}
		var out []string
		for _, it := range items {
			s := it.Type
			if it.Issuer != "" {
				s += "@" + it.Issuer
			}
			for _, c := range it.Content {
				s += " " + c.Type
			}
			var parts []struct {
				Type string `json:"type"`
			}
			if json.Unmarshal(it.Output, &parts) == nil {
				for _, p := range parts {
					s += " " + p.Type
				}
			}
			out = append(out, s)
		}
		return out
	}
	cases := []struct {
		name string
		opts []AdapterOption
		want []string
	}{
		{"no option", nil, []string{
			"message input_text anthropic.tool_reference gemini.thought", "anthropic.redacted_thinking", "gemini.executable_code",
			"chatcompletions.note", "acme.thing", "openresponses-experimental.note",
			"reasoning@anthropic", "reasoning@anthropic/other", "reasoning@gemini", "reasoning",
			"function_call_output input_text gemini.code_execution_result", "message gemini.thought",
		}},
		{"accepts none", []AdapterOption{WithProviderSlugs()}, []string{
			"message input_text", "acme.thing", "openresponses-experimental.note", "reasoning",
			"function_call_output input_text",
		}},
		{"anthropic", []AdapterOption{WithProviderSlugs("anthropic")}, []string{
			"message input_text anthropic.tool_reference", "anthropic.redacted_thinking", "acme.thing", "openresponses-experimental.note",
			"reasoning@anthropic", "reasoning@anthropic/other", "reasoning", "function_call_output input_text",
		}},
		{"a router for both", []AdapterOption{WithProviderSlugs("anthropic.", "gemini")}, []string{
			"message input_text anthropic.tool_reference gemini.thought", "anthropic.redacted_thinking", "gemini.executable_code",
			"acme.thing", "openresponses-experimental.note",
			"reasoning@anthropic", "reasoning@anthropic/other", "reasoning@gemini", "reasoning",
			"function_call_output input_text gemini.code_execution_result", "message gemini.thought",
		}},
	}
	for _, call := range adapterCalls {
		for _, tc := range cases {
			t.Run(call.name+"/"+tc.name, func(t *testing.T) {
				client, seen := captureUpstream(t)
				in := input()
				before, _ := json.Marshal(in)
				_ = call.call(testContext(t), client.AsAdapter(tc.opts...), in)
				if len(*seen) != 1 {
					t.Fatalf("upstream saw %d requests", len(*seen))
				}
				if got := summary(t, (*seen)[0]); !slices.Equal(got, tc.want) {
					t.Fatalf("upstream input =\n%q\nwant\n%q", got, tc.want)
				}
				if after, _ := json.Marshal(in); string(after) != string(before) {
					t.Fatalf("caller's input changed:\n%s\n%s", before, after)
				}
			})
		}
	}
}

// TestClientAdapterProviderMembers pins docs/extensions.md section 1.5
// for a pass-through: once configured, a request member or tool under a
// provider's slug the upstream does not accept is refused naming it,
// and nothing is sent. Its own providers' members and tools, and any
// other member, are forwarded; unconfigured, everything is.
func TestClientAdapterProviderMembers(t *testing.T) {
	tool := func(typ string) Tools {
		return Tools{&UnknownTool{Type: typ, Raw: json.RawMessage(`{"type":"` + typ + `"}`)}}
	}
	cases := []struct {
		name  string
		opts  []AdapterOption
		req   Request
		param string // "" when the request is forwarded
	}{
		{"other provider's member", []AdapterOption{WithProviderSlugs("anthropic")}, Request{Extra: map[string]any{"gemini.cached_content": "c"}}, "gemini.cached_content"},
		{"member when accepting none", []AdapterOption{WithProviderSlugs()}, Request{Extra: map[string]any{"anthropic.top_k": 5}}, "anthropic.top_k"},
		{"other provider's tool", []AdapterOption{WithProviderSlugs("anthropic")}, Request{Tools: tool("gemini.google_search")}, "tools[0].type"},
		{"own member", []AdapterOption{WithProviderSlugs("anthropic")}, Request{Extra: map[string]any{"anthropic.top_k": 5}}, ""},
		{"own tool", []AdapterOption{WithProviderSlugs("anthropic")}, Request{Tools: tool("anthropic.web_search_20260209")}, ""},
		{"member of no provider", []AdapterOption{WithProviderSlugs()}, Request{Extra: map[string]any{"acme.trace": "t", "openresponses-experimental.x": 1}}, ""},
		{"unconfigured", nil, Request{Extra: map[string]any{"gemini.cached_content": "c"}, Tools: tool("gemini.google_search")}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, seen := captureUpstream(t)
			tc.req.Model = "m"
			_, err := client.AsAdapter(tc.opts...).Create(testContext(t), tc.req)
			if tc.param == "" {
				if len(*seen) != 1 {
					t.Fatalf("not forwarded: %v", err)
				}
				return
			}
			var oerr *Error
			if !errors.As(err, &oerr) || oerr.Type != ErrorTypeInvalidRequest || oerr.Code != CodeUnsupportedParameter || oerr.Param != tc.param {
				t.Fatalf("err = %v, want invalid_request/unsupported_parameter on %s", err, tc.param)
			}
			if len(*seen) != 0 {
				t.Fatal("refused request reached the upstream")
			}
		})
	}
	t.Run("Compact", func(t *testing.T) {
		client, seen := captureUpstream(t)
		_, err := client.AsAdapter(WithProviderSlugs("anthropic")).Compact(testContext(t), CompactRequest{Model: "m", Extra: map[string]any{"gemini.x": 1}})
		var oerr *Error
		if !errors.As(err, &oerr) || oerr.Param != "gemini.x" || len(*seen) != 0 {
			t.Fatalf("err = %v, seen %d", err, len(*seen))
		}
	})
}

// TestClientAdapterForeignAnnotation covers an annotation on output_text
// under a provider's slug the upstream does not accept: it is left out
// with the part kept, as section 1.6 leaves out such a part. Others stay,
// and an unconfigured adapter keeps them all.
func TestClientAdapterForeignAnnotation(t *testing.T) {
	ann := func(typ string) Annotation {
		return &UnknownAnnotation{Type: typ, Raw: json.RawMessage(`{"type":"` + typ + `"}`)}
	}
	input := func() Items {
		return Items{&Message{Role: RoleAssistant, Content: Contents{
			&OutputText{Text: "a", Annotations: Annotations{ann("anthropic.char_location"), ann("gemini.grounding"), ann("acme.note")}},
			&OutputText{Text: "b", Annotations: Annotations{ann("anthropic.char_location")}},
		}}}
	}
	cases := []struct {
		name string
		opts []AdapterOption
		want string
	}{
		{"gemini", []AdapterOption{WithProviderSlugs("gemini")}, `[["gemini.grounding","acme.note"],[]]`},
		{"unconfigured", nil, `[["anthropic.char_location","gemini.grounding","acme.note"],["anthropic.char_location"]]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, seen := captureUpstream(t)
			in := input()
			_, _ = client.AsAdapter(tc.opts...).Create(testContext(t), Request{Model: "m", Input: in})
			var items []struct {
				Content []struct {
					Text        string `json:"text"`
					Annotations []struct {
						Type string `json:"type"`
					} `json:"annotations"`
				} `json:"content"`
			}
			if len(*seen) != 1 || json.Unmarshal((*seen)[0], &items) != nil || len(items) != 1 {
				t.Fatalf("upstream saw %s", *seen)
			}
			got := [][]string{}
			for _, c := range items[0].Content {
				types := []string{}
				for _, a := range c.Annotations {
					types = append(types, a.Type)
				}
				got = append(got, types)
			}
			out, _ := json.Marshal(got)
			if string(out) != tc.want {
				t.Fatalf("annotations = %s, want %s", out, tc.want)
			}
			if n := len(in[0].(*Message).Content[0].(*OutputText).Annotations); n != 3 {
				t.Fatalf("caller's annotations changed: %d left", n)
			}
		})
	}
}
