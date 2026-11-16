package gemini

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/ChristopherDavenport/openresponses"
	"github.com/ChristopherDavenport/openresponses/streamtest"
)

// hops serves one streamed response per request, in order, and records
// each request body. A request past the last hop is a test failure.
type hops struct {
	t      *testing.T
	mu     sync.Mutex
	chunks [][]string
	bodies []map[string]any
}

func (h *hops) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(r.Body)
	if err != nil {
		h.t.Errorf("read body: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(data, &body); err != nil {
		h.t.Errorf("decode body: %v", err)
	}
	h.mu.Lock()
	n := len(h.bodies)
	h.bodies = append(h.bodies, body)
	h.mu.Unlock()
	if n >= len(h.chunks) {
		h.t.Errorf("request %d, want at most %d", n+1, len(h.chunks))
		http.Error(w, "no more hops", http.StatusInternalServerError)
		return
	}
	sse(h.chunks[n]...).ServeHTTP(w, r)
}

func (h *hops) requests() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.bodies)
}

// continuationChunk cuts the response with finish reason CONTINUATION
// after text, carrying token (base64, as on the wire; empty omits it)
// and the hop's usage: 3 input tokens, 2 of them cached, and 2 output
// tokens, 1 of them thinking.
func continuationChunk(text, token string) string {
	tok := ""
	if token != "" {
		tok = `,"continuationToken":"` + token + `"`
	}
	return `{"candidates":[{"content":{"parts":[{"text":"` + text + `"}],"role":"model"},"finishReason":"CONTINUATION"` + tok + `,"index":0}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":1,"cachedContentTokenCount":2,"thoughtsTokenCount":1,"totalTokenCount":5}}`
}

// The second hop: the rest of the text, a function call the response
// goes on to make, and the finish.
var secondHop = []string{
	`{"candidates":[{"content":{"parts":[{"text":"lo"}],"role":"model"},"index":0}],"modelVersion":"gemini-2.5-pro-002"}`,
	`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"lookup","args":{"q":"x"}}}],"role":"model"},"finishReason":"STOP","index":0}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"cachedContentTokenCount":1,"thoughtsTokenCount":1,"totalTokenCount":6}}`,
}

// checkTwoHops asserts a response continued once reads as one: the
// text of both hops in one message, the second hop's call after it,
// usage summed over both requests, and the second request carrying the
// first's continuation token and the same contents.
func checkTwoHops(t *testing.T, h *hops, resp *openresponses.Response) {
	t.Helper()
	if n := h.requests(); n != 2 {
		t.Fatalf("requests = %d, want 2", n)
	}
	if got := h.bodies[1]["continuationToken"]; got != "dG9rZW4=" {
		t.Fatalf("second request continuationToken = %v, want dG9rZW4= (body %v)", got, h.bodies[1])
	}
	first, _ := json.Marshal(h.bodies[0]["contents"])
	second, _ := json.Marshal(h.bodies[1]["contents"])
	if string(first) != string(second) {
		t.Fatalf("second request contents = %s, want %s", second, first)
	}
	if resp.Status != openresponses.ResponseStatusCompleted {
		t.Fatalf("status = %s (%+v)", resp.Status, resp.IncompleteDetails)
	}
	if len(resp.Output) != 2 {
		t.Fatalf("output = %d items, want the message and the call", len(resp.Output))
	}
	msg, ok := resp.Output[0].(*openresponses.Message)
	if !ok || msg.Text() != "Hello" || msg.Status != openresponses.StatusCompleted {
		t.Fatalf("output[0] = %+v, want one completed message Hello", resp.Output[0])
	}
	if call, ok := resp.Output[1].(*openresponses.FunctionCall); !ok || call.Name != "lookup" {
		t.Fatalf("output[1] = %+v, want the function call", resp.Output[1])
	}
	if resp.Model != "gemini-2.5-pro-001" {
		t.Fatalf("model = %q, want the first hop's version", resp.Model)
	}
	// Input 3+3, output (1+1)+(2+1), total 5+6, cached 2+1, reasoning
	// 1+1: every count is summed over the requests.
	u := resp.Usage
	if u == nil || u.InputTokens != 6 || u.OutputTokens != 5 || u.TotalTokens != 11 ||
		u.InputTokensDetails.CachedTokens != 3 || u.OutputTokensDetails.ReasoningTokens != 2 {
		t.Fatalf("usage = %+v, want both hops summed", u)
	}
}

func twoHops(t *testing.T) *hops {
	return &hops{t: t, chunks: [][]string{
		{textChunk, continuationChunk("", "dG9rZW4=")},
		secondHop,
	}}
}

func TestStreamContinuation(t *testing.T) {
	h := twoHops(t)
	sink, err := streamtest.Run(context.Background(), newAdapter(t, h), hello())
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	checkTwoHops(t, h, sink.Response())
	var created, terminal int
	for _, ev := range sink.Events() {
		switch ev.(type) {
		case *openresponses.ResponseCreatedEvent:
			created++
		case *openresponses.ResponseCompletedEvent, *openresponses.ResponseIncompleteEvent, *openresponses.ResponseFailedEvent:
			terminal++
		}
	}
	if created != 1 || terminal != 1 {
		t.Fatalf("created = %d, terminal = %d, want one of each", created, terminal)
	}
}

func TestCreateContinuation(t *testing.T) {
	h := twoHops(t)
	resp, err := newAdapter(t, h).Create(context.Background(), hello())
	if err != nil {
		t.Fatal(err)
	}
	checkTwoHops(t, h, resp)
}

// TestContinuationLimit: at the limit the response ends incomplete with
// what the hops produced, the cut message marked incomplete.
func TestContinuationLimit(t *testing.T) {
	h := &hops{t: t, chunks: [][]string{
		{continuationChunk("Hel", "dG9rZW4=")},
		{continuationChunk("lo", "dG9rZW4y")},
	}}
	sink, err := streamtest.Run(context.Background(), newAdapter(t, h, WithContinuations(1)), hello())
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if n := h.requests(); n != 2 {
		t.Fatalf("requests = %d, want 2", n)
	}
	resp := sink.Response()
	checkCut(t, resp, "Hello")
	if u := resp.Usage; u == nil || u.InputTokens != 6 || u.OutputTokens != 4 || u.TotalTokens != 10 ||
		u.InputTokensDetails.CachedTokens != 4 || u.OutputTokensDetails.ReasoningTokens != 2 {
		t.Fatalf("usage = %+v, want both hops summed", u)
	}

	// The unary path carries the cut the same way.
	h = &hops{t: t, chunks: [][]string{
		{continuationChunk("Hel", "dG9rZW4=")},
		{continuationChunk("lo", "dG9rZW4y")},
	}}
	created, err := newAdapter(t, h, WithContinuations(1)).Create(context.Background(), hello())
	if err != nil {
		t.Fatal(err)
	}
	checkCut(t, created, "Hello")
}

// TestContinuationInsideThought: a cut inside a thought leaves the
// reasoning item open, and the next request's thought extends it before
// the answer opens a message.
func TestContinuationInsideThought(t *testing.T) {
	h := &hops{t: t, chunks: [][]string{
		{`{"candidates":[{"content":{"parts":[{"text":"Let me ","thought":true}],"role":"model"},"finishReason":"CONTINUATION","continuationToken":"dG9rZW4=","index":0}]}`},
		{`{"candidates":[{"content":{"parts":[{"text":"think.","thought":true}],"role":"model"},"index":0}]}`,
			`{"candidates":[{"content":{"parts":[{"text":"Answer."}],"role":"model"},"finishReason":"STOP","index":0}]}`},
	}}
	sink, err := streamtest.Run(context.Background(), newAdapter(t, h), hello())
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	resp := sink.Response()
	if len(resp.Output) != 2 {
		t.Fatalf("output = %+v, want one reasoning item and one message", resp.Output)
	}
	rs, ok := resp.Output[0].(*openresponses.ReasoningItem)
	if !ok || len(rs.Summary) == 0 {
		t.Fatalf("output[0] = %+v, want the reasoning item", resp.Output[0])
	}
	var summary string
	for _, part := range rs.Summary {
		if p, ok := part.(*openresponses.SummaryText); ok {
			summary += p.Text
		}
	}
	if summary != "Let me think." {
		t.Fatalf("reasoning summary = %q, want both requests' thought", summary)
	}
	if resp.OutputText() != "Answer." {
		t.Fatalf("output text = %q", resp.OutputText())
	}
}

// TestContinuationsZero: zero never continues, as WithContinuations(0)
// in the Anthropic adapter never resumes; the cut ends the response as
// at the limit, after one request.
func TestContinuationsZero(t *testing.T) {
	h := &hops{t: t, chunks: [][]string{{continuationChunk("Hel", "dG9rZW4=")}}}
	sink, err := streamtest.Run(context.Background(), newAdapter(t, h, WithContinuations(0)), hello())
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if n := h.requests(); n != 1 {
		t.Fatalf("requests = %d, want 1", n)
	}
	checkCut(t, sink.Response(), "Hel")
}

// TestContinuationWithoutToken: a CONTINUATION finish with no token
// cannot be continued, and ends the response as at the limit.
func TestContinuationWithoutToken(t *testing.T) {
	h := &hops{t: t, chunks: [][]string{{continuationChunk("Hel", "")}}}
	sink, err := streamtest.Run(context.Background(), newAdapter(t, h), hello())
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if n := h.requests(); n != 1 {
		t.Fatalf("requests = %d, want 1", n)
	}
	checkCut(t, sink.Response(), "Hel")
}

func checkCut(t *testing.T, resp *openresponses.Response, text string) {
	t.Helper()
	if resp.Status != openresponses.ResponseStatusIncomplete || resp.IncompleteDetails == nil ||
		resp.IncompleteDetails.Reason != openresponses.IncompleteReasonMaxOutputTokens {
		t.Fatalf("resp = %s %+v, want incomplete max_output_tokens", resp.Status, resp.IncompleteDetails)
	}
	if got := resp.IncompleteDetails.Extra[slug+"finish_reason"]; got != "CONTINUATION" {
		t.Fatalf("incomplete_details = %+v, want gemini.finish_reason CONTINUATION", resp.IncompleteDetails)
	}
	data, err := json.Marshal(resp.IncompleteDetails)
	if err != nil || !strings.Contains(string(data), `"gemini.finish_reason":"CONTINUATION"`) {
		t.Fatalf("incomplete_details JSON = %s (%v)", data, err)
	}
	if len(resp.Output) != 1 {
		t.Fatalf("output = %+v, want one message", resp.Output)
	}
	if msg, ok := resp.Output[0].(*openresponses.Message); !ok || msg.Text() != text || msg.Status != openresponses.StatusIncomplete {
		t.Fatalf("output[0] = %+v, want an incomplete message %q", resp.Output[0], text)
	}
}

// TestContinuationErrorOnSecondRequest: a continued request that fails
// fails the response, as any mid-stream failure does. Through the
// handler the caller sees one response.created, the first request's
// text, and one terminal event, response.failed; nothing reads as
// incomplete or completed.
func TestContinuationErrorOnSecondRequest(t *testing.T) {
	// failing serves a cut first request and a second that fails after
	// one chunk, inside the stream, so the SDK's HTTP retry cannot hide it.
	failing := func() (*hops, *Adapter) {
		h := &hops{t: t, chunks: [][]string{{continuationChunk("First", "dG9rZW4=")}}}
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if h.requests() == 0 {
				h.ServeHTTP(w, r)
				return
			}
			h.mu.Lock()
			h.bodies = append(h.bodies, nil)
			h.mu.Unlock()
			streamError(`{"error":{"code":500,"message":"boom","status":"INTERNAL"}}`).ServeHTTP(w, r)
		})
		return h, newAdapter(t, handler)
	}

	h, adapter := failing()
	_, err := streamtest.Run(context.Background(), adapter, hello())
	var oerr *openresponses.Error
	if !errorsAs(err, &oerr) || oerr.Type != openresponses.ErrorTypeServerError || oerr.Code != "internal" {
		t.Fatalf("err = %v, want the second request's server_error", err)
	}
	if n := h.requests(); n != 2 {
		t.Fatalf("requests = %d, want 2", n)
	}

	h, adapter = failing()
	srv := httptest.NewServer(openresponses.NewHandler(adapter))
	t.Cleanup(srv.Close)
	req := hello()
	req.Stream = true
	body, _ := json.Marshal(req)
	resp, err := http.Post(srv.URL+"/v1/responses", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if n := h.requests(); n != 2 {
		t.Fatalf("handler run: requests = %d, want 2", n)
	}
	stream := string(data)
	count := func(event string) int { return strings.Count(stream, "event: "+event+"\n") }
	if count("response.created") != 1 || count("response.failed") != 1 ||
		count("response.incomplete") != 0 || count("response.completed") != 0 {
		t.Fatalf("stream = %s, want one created and one failed", stream)
	}
	if !strings.Contains(stream, `"delta":"First"`) {
		t.Fatalf("stream = %s, want the first request's text", stream)
	}
}

// TestContinuationCheckpointToken: chunks before the end of a request
// can carry checkpoint tokens. A CONTINUATION finish without a token of
// its own continues from the latest of them, as the SDK's loop does.
func TestContinuationCheckpointToken(t *testing.T) {
	h := &hops{t: t, chunks: [][]string{
		{`{"candidates":[{"content":{"parts":[{"text":"Hel"}],"role":"model"},"continuationToken":"Y2hlY2sx","index":0}]}`,
			`{"candidates":[{"content":{"parts":[{"text":"l"}],"role":"model"},"continuationToken":"Y2hlY2sy","index":0}]}`,
			continuationChunk("", "")},
		{finishChunk},
	}}
	sink, err := streamtest.Run(context.Background(), newAdapter(t, h), hello())
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if n := h.requests(); n != 2 {
		t.Fatalf("requests = %d, want 2", n)
	}
	if got := h.bodies[1]["continuationToken"]; got != "Y2hlY2sy" {
		t.Fatalf("second request continuationToken = %v, want the latest checkpoint", got)
	}
	if resp := sink.Response(); resp.Status != openresponses.ResponseStatusCompleted || resp.OutputText() != "Helllo" {
		t.Fatalf("resp = %s %q", resp.Status, resp.OutputText())
	}
}

// TestContinuationNoProgress: a request that ends with the token it was
// sent with made no progress, and is not sent again; the response ends
// as at the limit.
func TestContinuationNoProgress(t *testing.T) {
	h := &hops{t: t, chunks: [][]string{
		{continuationChunk("Hel", "dG9rZW4=")},
		{continuationChunk("lo", "dG9rZW4=")},
	}}
	sink, err := streamtest.Run(context.Background(), newAdapter(t, h), hello())
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if n := h.requests(); n != 2 {
		t.Fatalf("requests = %d, want 2", n)
	}
	checkCut(t, sink.Response(), "Hello")
}

// TestContinuationLaterWithoutToken: a later request that ends with
// CONTINUATION and no token at all ends the response as at the limit.
func TestContinuationLaterWithoutToken(t *testing.T) {
	h := &hops{t: t, chunks: [][]string{
		{continuationChunk("Hel", "dG9rZW4=")},
		{continuationChunk("lo", "")},
	}}
	sink, err := streamtest.Run(context.Background(), newAdapter(t, h), hello())
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if n := h.requests(); n != 2 {
		t.Fatalf("requests = %d, want 2", n)
	}
	checkCut(t, sink.Response(), "Hello")
}

// TestContinuationOutputBudget: max_output_tokens bounds the whole
// response, and Gemini counts it across the continued requests: it "is
// cumulative across continuation hops and must be monotonically
// increased or omitted" (API reference, Candidate.finishReason
// CONTINUATION). So every request sends the caller's value unchanged,
// as the SDK's own loop does. Once the requests' output tokens have
// spent it, nothing more can come, and the response ends incomplete as
// any max_output_tokens cut does, without another request and without
// the CONTINUATION member.
func TestContinuationOutputBudget(t *testing.T) {
	maxTokens := func(body map[string]any) any {
		cfg, _ := body["generationConfig"].(map[string]any)
		return cfg["maxOutputTokens"]
	}

	h := twoHops(t)
	req := hello()
	req.MaxOutputTokens = ptr(10)
	if _, err := streamtest.Run(context.Background(), newAdapter(t, h), req); err != nil {
		t.Fatalf("stream: %v", err)
	}
	if n := h.requests(); n != 2 {
		t.Fatalf("requests = %d, want 2", n)
	}
	if first, second := maxTokens(h.bodies[0]), maxTokens(h.bodies[1]); first != float64(10) || second != float64(10) {
		t.Fatalf("maxOutputTokens = %v then %v, want 10 then 10", first, second)
	}

	h = &hops{t: t, chunks: [][]string{{continuationChunk("Hel", "dG9rZW4=")}}}
	req.MaxOutputTokens = ptr(2)
	sink, err := streamtest.Run(context.Background(), newAdapter(t, h), req)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if n := h.requests(); n != 1 {
		t.Fatalf("requests = %d, want 1: the budget was spent", n)
	}
	resp := sink.Response()
	if resp.Status != openresponses.ResponseStatusIncomplete || resp.IncompleteDetails == nil ||
		resp.IncompleteDetails.Reason != openresponses.IncompleteReasonMaxOutputTokens {
		t.Fatalf("resp = %s %+v, want incomplete max_output_tokens", resp.Status, resp.IncompleteDetails)
	}
	if _, ok := resp.IncompleteDetails.Extra[slug+"finish_reason"]; ok {
		t.Fatalf("incomplete_details = %+v, want a plain max_output_tokens cut", resp.IncompleteDetails)
	}
}

// TestContinuationGroundingOffsets: a continued request's grounding
// offsets count from the start of that request's output, so a segment
// located by its offsets alone lands after the text the earlier
// requests put in the message.
func TestContinuationGroundingOffsets(t *testing.T) {
	h := &hops{t: t, chunks: [][]string{
		{`{"candidates":[{"content":{"parts":[{"text":"Café is nice. "}],"role":"model"},"finishReason":"CONTINUATION","continuationToken":"dG9rZW4=","index":0}]}`},
		{`{"candidates":[{"content":{"parts":[{"text":"Really nice."}],"role":"model"},"finishReason":"STOP","index":0,
		  "groundingMetadata":{
		    "groundingChunks":[{"web":{"uri":"https://example.com/a","title":"A"}}],
		    "groundingSupports":[{"segment":{"startIndex":0,"endIndex":6},"groundingChunkIndices":[0]}]}}]}`},
	}}
	sink, err := streamtest.Run(context.Background(), newAdapter(t, h), hello())
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	text := sink.Response().Output[0].(*openresponses.Message).Content[0].(*openresponses.OutputText)
	if text.Text != "Café is nice. Really nice." || len(text.Annotations) != 1 {
		t.Fatalf("text = %q annotations = %d", text.Text, len(text.Annotations))
	}
	got := text.Annotations[0].(*openresponses.URLCitation)
	if got.StartIndex != 14 || got.EndIndex != 20 {
		t.Fatalf("citation = %d..%d, want 14..20 (Really)", got.StartIndex, got.EndIndex)
	}
}

// TestContinuationGroundingPartStart: segment offsets count from the
// start of the Part they name (genai Segment.StartIndex), so the shift is
// where that part's text began in the message, recorded per request by
// part index. A part this request put no text into is not shifted: here
// the continued request carries grounding alone, and its segment reads
// against the message from its start rather than past its end.
func TestContinuationGroundingPartStart(t *testing.T) {
	h := &hops{t: t, chunks: [][]string{
		{`{"candidates":[{"content":{"parts":[{"text":"Café is nice. "}],"role":"model"},"finishReason":"CONTINUATION","continuationToken":"dG9rZW4=","index":0}]}`},
		{`{"candidates":[{"finishReason":"STOP","index":0,
		  "groundingMetadata":{
		    "groundingChunks":[{"web":{"uri":"https://example.com/a","title":"A"}}],
		    "groundingSupports":[{"segment":{"partIndex":0,"startIndex":0,"endIndex":5},"groundingChunkIndices":[0]}]}}]}`},
	}}
	sink, err := streamtest.Run(context.Background(), newAdapter(t, h), hello())
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	text := sink.Response().Output[0].(*openresponses.Message).Content[0].(*openresponses.OutputText)
	if len(text.Annotations) != 1 {
		t.Fatalf("annotations = %d", len(text.Annotations))
	}
	got := text.Annotations[0].(*openresponses.URLCitation)
	if got.StartIndex != 0 || got.EndIndex != 4 {
		t.Fatalf("citation = %d..%d, want 0..4 (Café)", got.StartIndex, got.EndIndex)
	}
}
