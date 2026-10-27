package chatcompletions

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/ChristopherDavenport/openresponses"
	"github.com/ChristopherDavenport/openresponses/internal/jsonx"
	"github.com/ChristopherDavenport/openresponses/internal/sse"
)

// maxErrorBody caps how much of an error response is read.
const maxErrorBody = 1 << 20

// CreateStream posts the request with stream: true and folds the chunks
// into items. Reasoning fields precede content in every provider that
// sends them, content extends a message, tool calls stream by index,
// and the choice's finish_reason ends the response once the stream is
// drained, since usage arrives after it.
func (a *Adapter) CreateStream(ctx context.Context, req openresponses.Request, sink openresponses.EventSink) error {
	b, err := a.encodeRequest(req)
	if err != nil {
		return err
	}
	res, err := a.post(ctx, b)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	d := &decoder{em: openresponses.NewEmitter(sink, openresponses.NewResponse(req))}
	scanner := sse.NewScanner(res.Body)
	for {
		frame, ok := scanner.Next()
		if !ok {
			break
		}
		if frame.Data == sse.Done {
			break
		}
		var c chunk
		if err := json.Unmarshal([]byte(frame.Data), &c); err != nil {
			return openresponses.ServerError("invalid_upstream_response", "chunk: "+err.Error())
		}
		if c.Error != nil {
			// Some providers report a mid-stream failure as a data frame,
			// after the stream's 2xx status.
			return c.Error.toError(c.Error.status(), nil)
		}
		if err := d.chunk(&c); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return openresponses.ServerError("upstream_error", err.Error())
	}
	return d.finish()
}

// post sends the body through the client's transport with its headers.
func (a *Adapter) post(ctx context.Context, b *body) (*http.Response, error) {
	data, err := json.Marshal(b)
	if err != nil {
		return nil, openresponses.ServerError("encode_failed", err.Error())
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.client.BaseURL()+"/chat/completions", bytes.NewReader(data))
	if err != nil {
		return nil, openresponses.ServerError("encode_failed", err.Error())
	}
	for name, values := range a.client.RequestHeaders() {
		httpReq.Header[name] = values
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "text/event-stream")
	res, err := a.client.HTTPClient().Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, openresponses.ServerError("upstream_error", err.Error())
	}
	if res.StatusCode/100 != 2 {
		defer res.Body.Close()
		return nil, mapError(res)
	}
	return res, nil
}

// Wire shapes of a streamed chunk. Only what the mapping reads is typed.
type chunk struct {
	Model   string    `json:"model"`
	Choices []choice  `json:"choices"`
	Usage   *usage    `json:"usage"`
	Error   *apiError `json:"error"`
}

type choice struct {
	Index        int       `json:"index"`
	Delta        delta     `json:"delta"`
	FinishReason string    `json:"finish_reason"`
	Logprobs     *logprobs `json:"logprobs"`
}

type delta struct {
	Content          *string         `json:"content"`
	Refusal          *string         `json:"refusal"`
	ReasoningContent *string         `json:"reasoning_content"` // DeepSeek
	Reasoning        json.RawMessage `json:"reasoning"`         // OpenRouter, Groq and others; a string when present
	ToolCalls        []toolCallDelta `json:"tool_calls"`
	Annotations      []annotation    `json:"annotations"`
}

type toolCallDelta struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type annotation struct {
	Type        string `json:"type"`
	URLCitation *struct {
		URL        string `json:"url"`
		Title      string `json:"title"`
		StartIndex int    `json:"start_index"`
		EndIndex   int    `json:"end_index"`
	} `json:"url_citation"`
}

type logprobs struct {
	Content []logprob `json:"content"`
}

type logprob struct {
	Token       string       `json:"token"`
	Logprob     float64      `json:"logprob"`
	Bytes       []int        `json:"bytes"`
	TopLogprobs []topLogprob `json:"top_logprobs"`
}

type topLogprob struct {
	Token   string  `json:"token"`
	Logprob float64 `json:"logprob"`
	Bytes   []int   `json:"bytes"`
}

// usage and its details keep the members the mapping does not read, so
// they reach the response's usage at the same depth.
type usage struct {
	PromptTokens            int                `json:"prompt_tokens"`
	CompletionTokens        int                `json:"completion_tokens"`
	TotalTokens             int                `json:"total_tokens"`
	PromptTokensDetails     *promptDetails     `json:"prompt_tokens_details"`
	CompletionTokensDetails *completionDetails `json:"completion_tokens_details"`
	extra                   map[string]any
}

type promptDetails struct {
	CachedTokens     *int `json:"cached_tokens"`
	CacheWriteTokens *int `json:"cache_write_tokens"` // OpenRouter
	extra            map[string]any
}

type completionDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
	extra           map[string]any
}

func (u *usage) UnmarshalJSON(data []byte) error {
	type plain usage
	var p plain
	extra, _, err := jsonx.UnmarshalExtra(data, &p)
	if err != nil {
		return err
	}
	*u = usage(p)
	u.extra = extra
	return nil
}

func (d *promptDetails) UnmarshalJSON(data []byte) error {
	type plain promptDetails
	var p plain
	extra, _, err := jsonx.UnmarshalExtra(data, &p)
	if err != nil {
		return err
	}
	*d = promptDetails(p)
	d.extra = extra
	return nil
}

func (d *completionDetails) UnmarshalJSON(data []byte) error {
	type plain completionDetails
	var p plain
	extra, _, err := jsonx.UnmarshalExtra(data, &p)
	if err != nil {
		return err
	}
	*d = completionDetails(p)
	d.extra = extra
	return nil
}

// decoder folds chunks into Emitter calls. At most one writer is open at
// a time; tool calls are streamed in index order.
type decoder struct {
	em        *openresponses.Emitter
	msg       *openresponses.MessageWriter
	reasoning *openresponses.ReasoningWriter
	call      *callState
	finished  string
	seen      bool
}

// callState is the tool call being streamed. Its writer opens once the
// name is known, which is the first delta everywhere in practice; until
// then the arguments are buffered.
type callState struct {
	index    int
	id, name string
	buffered strings.Builder
	w        *openresponses.FunctionCallWriter
}

func (d *decoder) chunk(c *chunk) error {
	if !d.seen {
		d.seen = true
		// Before the first event, so response.created carries it.
		if c.Model != "" {
			d.em.Response().Model = c.Model
		}
	}
	if c.Usage != nil {
		d.em.Response().Usage = mapUsage(c.Usage)
	}
	for i := range c.Choices {
		ch := &c.Choices[i]
		if ch.Index != 0 {
			continue // n is pinned to 1
		}
		if err := d.delta(&ch.Delta); err != nil {
			return err
		}
		if ch.Logprobs != nil && len(ch.Logprobs.Content) > 0 {
			m, err := d.openMessage()
			if err != nil {
				return err
			}
			if err := m.Logprobs(mapLogprobs(ch.Logprobs.Content)...); err != nil {
				return err
			}
		}
		if ch.FinishReason != "" {
			d.finished = ch.FinishReason
		}
	}
	return nil
}

func (d *decoder) delta(dl *delta) error {
	if text := reasoningText(dl); text != "" {
		if d.reasoning == nil {
			if err := d.closeOpen(); err != nil {
				return err
			}
			r, err := d.em.Reasoning()
			if err != nil {
				return err
			}
			d.reasoning = r
		}
		if err := d.reasoning.Text(text); err != nil {
			return err
		}
	}
	if dl.Content != nil && *dl.Content != "" {
		m, err := d.openMessage()
		if err != nil {
			return err
		}
		if err := m.Text(*dl.Content); err != nil {
			return err
		}
	}
	if dl.Refusal != nil && *dl.Refusal != "" {
		m, err := d.openMessage()
		if err != nil {
			return err
		}
		if err := m.Refusal(*dl.Refusal); err != nil {
			return err
		}
	}
	for i := range dl.ToolCalls {
		if err := d.toolCall(&dl.ToolCalls[i]); err != nil {
			return err
		}
	}
	for _, a := range dl.Annotations {
		if a.Type != "url_citation" || a.URLCitation == nil {
			continue
		}
		m, err := d.openMessage()
		if err != nil {
			return err
		}
		if err := m.Annotation(&openresponses.URLCitation{URL: a.URLCitation.URL, Title: a.URLCitation.Title, StartIndex: a.URLCitation.StartIndex, EndIndex: a.URLCitation.EndIndex}); err != nil {
			return err
		}
	}
	return nil
}

// reasoningText returns the reasoning delta under whichever name the
// provider uses.
func reasoningText(dl *delta) string {
	if dl.ReasoningContent != nil {
		return *dl.ReasoningContent
	}
	var s string
	if len(dl.Reasoning) > 0 && json.Unmarshal(dl.Reasoning, &s) == nil {
		return s
	}
	return ""
}

func (d *decoder) toolCall(tc *toolCallDelta) error {
	if d.call != nil && tc.Index != d.call.index {
		if tc.Index < d.call.index {
			return openresponses.ServerError("invalid_upstream_response",
				fmt.Sprintf("tool call %d resumed after tool call %d started", tc.Index, d.call.index))
		}
		if err := d.closeCall(); err != nil {
			return err
		}
	}
	if d.call == nil {
		if err := d.closeOpen(); err != nil {
			return err
		}
		d.call = &callState{index: tc.Index}
	}
	c := d.call
	if tc.ID != "" {
		c.id = tc.ID
	}
	if tc.Function.Name != "" {
		c.name += tc.Function.Name
	}
	if c.w == nil {
		c.buffered.WriteString(tc.Function.Arguments)
		if c.name == "" {
			return nil
		}
		return d.openCall()
	}
	if tc.Function.Arguments != "" {
		return c.w.Arguments(tc.Function.Arguments)
	}
	return nil
}

func (d *decoder) openCall() error {
	c := d.call
	w, err := d.em.FunctionCall(c.id, c.name)
	if err != nil {
		return err
	}
	c.w = w
	if c.buffered.Len() > 0 {
		if err := w.Arguments(c.buffered.String()); err != nil {
			return err
		}
		c.buffered.Reset()
	}
	return nil
}

func (d *decoder) closeCall() error {
	w, err := d.endCall()
	if w == nil || err != nil {
		return err
	}
	return w.Close()
}

// endCall finishes the arguments of the call being streamed and returns
// its writer, still open, or nil when there is no call.
func (d *decoder) endCall() (*openresponses.FunctionCallWriter, error) {
	c := d.call
	if c == nil {
		return nil, nil
	}
	d.call = nil
	if c.w == nil {
		if err := d.openCall(); err != nil {
			return nil, err
		}
	}
	if c.w.Item().Arguments == "" {
		if err := c.w.Arguments("{}"); err != nil {
			return nil, err
		}
	}
	return c.w, nil
}

func (d *decoder) openMessage() (*openresponses.MessageWriter, error) {
	if d.msg != nil {
		return d.msg, nil
	}
	if err := d.closeOpen(); err != nil {
		return nil, err
	}
	m, err := d.em.Message(openresponses.PhaseFinalAnswer)
	if err != nil {
		return nil, err
	}
	d.msg = m
	return m, nil
}

func (d *decoder) closeOpen() error {
	switch {
	case d.msg != nil:
		m := d.msg
		d.msg = nil
		return m.Close()
	case d.reasoning != nil:
		r := d.reasoning
		d.reasoning = nil
		return r.Close()
	case d.call != nil:
		return d.closeCall()
	}
	return nil
}

// finish ends the response according to the finish reason. A call still
// streaming is left open for the emitter to close, so that Incomplete
// marks it when the output was cut off.
func (d *decoder) finish() error {
	if _, err := d.endCall(); err != nil {
		return err
	}
	switch d.finished {
	case "stop", "tool_calls", "function_call":
		return d.em.Complete()
	case "length":
		return d.em.Incomplete(openresponses.IncompleteReasonMaxOutputTokens)
	case "content_filter":
		return d.em.Incomplete(openresponses.IncompleteReasonContentFilter)
	case "":
		return openresponses.ServerError("truncated_stream", "the stream ended without a finish reason")
	}
	return openresponses.ModelError(d.finished, "the model stopped generating: "+d.finished)
}

// mapUsage maps the counts, writes a cache-write count as the shared
// member (docs/extensions.md section 5), and keeps every member it does
// not read in Extra at the depth it arrived at. DeepSeek reports cache
// reads as prompt_cache_hit_tokens, read when cached_tokens is absent.
func mapUsage(u *usage) *openresponses.Usage {
	out := &openresponses.Usage{InputTokens: u.PromptTokens, OutputTokens: u.CompletionTokens, TotalTokens: u.TotalTokens, Extra: u.extra}
	cached := false
	if d := u.PromptTokensDetails; d != nil {
		out.InputTokensDetails.Extra = d.extra
		if d.CachedTokens != nil {
			out.InputTokensDetails.CachedTokens = *d.CachedTokens
			cached = true
		}
		if d.CacheWriteTokens != nil && *d.CacheWriteTokens >= 0 {
			out.InputTokensDetails.SetCacheWriteTokens(*d.CacheWriteTokens)
		}
	}
	if n, ok := u.extra["prompt_cache_hit_tokens"].(json.Number); ok && !cached {
		if hit, err := n.Int64(); err == nil && hit >= 0 {
			out.InputTokensDetails.CachedTokens = int(hit)
		}
	}
	if d := u.CompletionTokensDetails; d != nil {
		out.OutputTokensDetails.ReasoningTokens = d.ReasoningTokens
		out.OutputTokensDetails.Extra = d.extra
	}
	if out.TotalTokens == 0 {
		out.TotalTokens = out.InputTokens + out.OutputTokens
	}
	return out
}

func mapLogprobs(in []logprob) []openresponses.LogProb {
	out := make([]openresponses.LogProb, 0, len(in))
	for _, lp := range in {
		o := openresponses.LogProb{Token: lp.Token, Logprob: lp.Logprob, Bytes: lp.Bytes}
		for _, top := range lp.TopLogprobs {
			o.TopLogprobs = append(o.TopLogprobs, openresponses.TopLogProb{Token: top.Token, Logprob: top.Logprob, Bytes: top.Bytes})
		}
		out = append(out, o)
	}
	return out
}

// apiError is the error envelope's payload. code is a string at OpenAI
// and a number at some providers.
type apiError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    any    `json:"code"`
	Param   string `json:"param"`
}

// errorStatus is the HTTP status that OpenAI and the providers that copy
// its envelope send with an error of a given code or type, for an error
// that arrives inside a stream without one.
var errorStatus = map[string]int{
	"invalid_request_error":   http.StatusBadRequest,
	"context_length_exceeded": http.StatusBadRequest,
	"authentication_error":    http.StatusUnauthorized,
	"invalid_api_key":         http.StatusUnauthorized,
	"permission_error":        http.StatusForbidden,
	"permission_denied":       http.StatusForbidden,
	"not_found_error":         http.StatusNotFound,
	"model_not_found":         http.StatusNotFound,
	"rate_limit_error":        http.StatusTooManyRequests,
	"rate_limit_exceeded":     http.StatusTooManyRequests,
	"insufficient_quota":      http.StatusTooManyRequests,
	"overloaded_error":        529,
	"server_error":            http.StatusInternalServerError,
	"api_error":               http.StatusInternalServerError,
}

// code returns the envelope's code as a string.
func (e *apiError) code() string {
	switch v := e.Code.(type) {
	case string:
		return v
	case float64:
		return fmt.Sprintf("%d", int(v))
	}
	return ""
}

// status is the status the error would have on a non-streamed response:
// a numeric code in the HTTP error range is one (OpenRouter sends them as
// numbers, LiteLLM as strings), and otherwise the code, then the type,
// names it. It is 0, leaving the status to the error type, when neither
// is known.
func (e *apiError) status() int {
	if n, err := strconv.Atoi(e.code()); err == nil && n >= 400 && n < 600 {
		return n
	}
	if s, ok := errorStatus[e.code()]; ok {
		return s
	}
	return errorStatus[e.Type]
}

// overflowMessages are the phrases, in lower case, by which upstreams
// that send no code of their own for it refuse a request whose input
// exceeds the model's context window.
var overflowMessages = []string{
	"maximum context length",                       // OpenAI, vLLM, DeepSeek, OpenRouter
	"exceeds the available context size",           // llama.cpp
	"too large for model with",                     // Mistral
	"prompt is too long",                           // Claude's compatible endpoint
	"exceeds the maximum number of tokens allowed", // Gemini's compatible endpoint
}

// The counts a "maximum context length" message carries: the window, and
// either the input or the total requested and the output's share of it.
var (
	windowCount = regexp.MustCompile(`maximum context length is (\d+)`)
	inputCount  = regexp.MustCompile(`(\d+) input tokens|resulted in (\d+) tokens|\((\d+) in the messages`)
	totalCount  = regexp.MustCompile(`requested (?:about )?(\d+) tokens`)
	outputCount = regexp.MustCompile(`(\d+) in the (?:completion|output)`)
)

// inputExceeds reads the counts in a lower-cased overflow message and
// reports whether the input alone exceeds the window. known is false
// when the message does not carry the counts.
func inputExceeds(msg string) (exceeds, known bool) {
	window, ok := firstCount(windowCount, msg)
	if !ok {
		return false, false
	}
	if input, ok := firstCount(inputCount, msg); ok {
		return input > window, true
	}
	total, ok := firstCount(totalCount, msg)
	if !ok {
		return false, false
	}
	output, _ := firstCount(outputCount, msg)
	return total-output > window, true
}

// firstCount returns the first group re matched in msg as a number.
func firstCount(re *regexp.Regexp, msg string) (int, bool) {
	m := re.FindStringSubmatch(msg)
	for _, g := range m[min(1, len(m)):] {
		if n, err := strconv.Atoi(g); err == nil {
			return n, true
		}
	}
	return 0, false
}

// contextOverflow reports whether the error refuses a request whose
// input exceeds the model's context window: OpenAI's code, llama.cpp's
// type, or one of overflowMessages in the message. A message whose
// counts show that the input fits is not one, nor is one that refuses
// the output limit without counts: the output limit, not the input, is
// what has to change, and compacting would not help.
func (e *apiError) contextOverflow() bool {
	if e.code() == openresponses.CodeContextLengthExceeded || e.Type == "exceed_context_size_error" {
		return true
	}
	msg := strings.ToLower(e.Message)
	for _, phrase := range overflowMessages {
		if !strings.Contains(msg, phrase) {
			continue
		}
		if exceeds, known := inputExceeds(msg); known {
			return exceeds
		}
		return !strings.Contains(msg, "max_tokens") && !strings.Contains(msg, "max_completion_tokens")
	}
	return false
}

// toError maps the envelope by status as the root Client does, except
// that a refusal of the adapter's credentials (401, 403) is
// invalid_request as providers/PLAN.md requires. A status of 0, from a
// stream frame that names no known error, leaves the status to the type:
// invalid_request for an overflow, server_error otherwise. The
// envelope's type, and a code the adapter replaces, are kept under the
// adapter's slug.
func (e *apiError) toError(status int, headers http.Header) *openresponses.Error {
	code := e.code()
	if code == "" {
		code = e.Type
	}
	if code == "" && status != 0 {
		code = fmt.Sprintf("http_%d", status)
	}
	if code == "" {
		code = "upstream_error"
	}
	var out *openresponses.Error
	switch status {
	case http.StatusNotFound:
		out = openresponses.NotFound(code, e.Message, e.Param)
	case http.StatusTooManyRequests:
		out = openresponses.TooManyRequests(code, e.Message)
	case http.StatusBadRequest, http.StatusUnprocessableEntity, 0,
		http.StatusUnauthorized, http.StatusForbidden:
		switch {
		case status != http.StatusUnauthorized && status != http.StatusForbidden && e.contextOverflow():
			out = openresponses.InvalidRequest(openresponses.CodeContextLengthExceeded, e.Message, e.Param)
		case status == 0:
			out = openresponses.ServerError(code, e.Message)
		default:
			out = openresponses.InvalidRequest(code, e.Message, e.Param)
		}
	default:
		out = openresponses.ServerError(code, e.Message)
	}
	out.StatusCode = status
	if e.Type != "" {
		out.Extra = map[string]any{memberType: e.Type}
	}
	if c := e.code(); c != "" && c != out.Code {
		if out.Extra == nil {
			out.Extra = map[string]any{}
		}
		out.Extra[memberCode] = c
	}
	for name, values := range headers {
		if !failureHeader(name) {
			continue
		}
		for _, v := range values {
			out.WithHeader(name, v)
		}
	}
	return out
}

// Members of an error under the adapter's slug: the upstream envelope's
// type, and its code when the error's code replaces it.
const (
	memberType = "chatcompletions.type"
	memberCode = "chatcompletions.code"
)

// mapError converts a non-2xx response. The status decides the type,
// the envelope's code or type is the code, and the headers that describe
// the failure travel with it.
func mapError(res *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(res.Body, maxErrorBody))
	var envelope struct {
		Error *apiError `json:"error"`
	}
	if json.Unmarshal(data, &envelope) != nil || envelope.Error == nil {
		envelope.Error = &apiError{Message: strings.TrimSpace(string(data))}
		if envelope.Error.Message == "" {
			envelope.Error.Message = res.Status
		}
	}
	return envelope.Error.toError(res.StatusCode, res.Header)
}

func failureHeader(name string) bool {
	lower := strings.ToLower(name)
	switch lower {
	case "retry-after", "request-id", "x-request-id":
		return true
	}
	return strings.HasPrefix(lower, "ratelimit-") || strings.HasPrefix(lower, "x-ratelimit-")
}
