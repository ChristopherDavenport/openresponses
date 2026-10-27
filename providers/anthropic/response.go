package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/ChristopherDavenport/openresponses"
	sdk "github.com/anthropics/anthropic-sdk-go"
)

// CreateStream runs one Messages call and streams it to sink. Content
// blocks map onto the Emitter's items in order: text blocks open or
// extend a message, thinking blocks are reasoning items, tool_use blocks
// are function calls streamed argument by argument, and every other
// block becomes an anthropic.* extension item. A turn Claude pauses
// (pause_turn) is resumed with its own output replayed, up to the
// configured number of continuations.
func (a *Adapter) CreateStream(ctx context.Context, req openresponses.Request, sink openresponses.EventSink) error {
	params, err := encodeRequest(req, a.config)
	if err != nil {
		return err
	}
	d := &decoder{}
	d.em = openresponses.NewEmitter(openresponses.EventSinkFunc(func(ev openresponses.StreamEvent) error {
		d.annotate(ev)
		return sink.Send(ev)
	}), openresponses.NewResponse(req))
	turnStart := 0
	for turn := 0; ; turn++ {
		if err := d.consume(a.messages.NewStreaming(ctx, params)); err != nil {
			return err
		}
		if d.stop != sdk.StopReasonPauseTurn || turn >= a.continuations {
			break
		}
		// Resume: what Claude produced so far goes back as the assistant
		// message, and the next turn continues into the same response.
		produced, _, err := encodeInput(d.em.Response().Output[turnStart:])
		if err != nil {
			return openresponses.ServerError("continuation_failed", err.Error())
		}
		params.Messages = append(params.Messages, produced...)
		turnStart = len(d.em.Response().Output)
		d.stop = ""
	}
	return d.finish()
}

// issuer names this adapter as the issuer of the reasoning items it
// emits, whose encrypted_content is Claude's thinking signature
// (docs/extensions.md, section 4).
const issuer = "anthropic"

// annotate sets the members the Emitter has no call for on an event's
// item or response before the event is sent, so the event carries them
// as the final response does.
func (d *decoder) annotate(ev openresponses.StreamEvent) {
	switch ev := ev.(type) {
	case *openresponses.OutputItemAddedEvent:
		if rs, ok := ev.Item.(*openresponses.ReasoningItem); ok {
			rs.SetIssuer(issuer)
		}
	case *openresponses.ResponseIncompleteEvent:
		if details := ev.Response.IncompleteDetails; details != nil && d.refusalCategory != "" {
			if details.Extra == nil {
				details.Extra = map[string]any{}
			}
			details.Extra[refusalCategoryMember] = d.refusalCategory
		}
	}
}

// decoder folds Messages stream events into Emitter calls. At most one
// writer is open at a time, matching the API's sequential blocks.
type decoder struct {
	em        *openresponses.Emitter
	msg       *openresponses.MessageWriter
	reasoning *openresponses.ReasoningWriter
	call      *openresponses.FunctionCallWriter
	callWrote bool
	raw       *rawBlock
	// Citations for the open text block, emitted when it closes so
	// end_index is known; blockStart is the character offset of the
	// block within the message text.
	citations  []pendingCitation
	blockStart int

	usage       openresponses.Usage // across turns
	turn        openresponses.Usage // this turn, replaced by message_delta
	split       *sdk.CacheCreation  // this turn's cache writes by TTL, from message_start
	stop        sdk.StopReason
	stopDetails sdk.RefusalStopDetails
	seen        bool
	// refusalCategory is the category of a refusal stop, set on
	// incomplete_details as [refusalCategoryMember].
	refusalCategory string
}

// refusalCategoryMember is the incomplete_details member carrying the
// category Claude gives a refusal stop (cyber, bio, ...). Categories
// differ by provider, so it is under this adapter's slug.
const refusalCategoryMember = slug + "refusal_category"

// rawBlock accumulates a block the specification has no item for, so it
// can be re-emitted whole when it stops: the start event's JSON plus any
// input or text deltas.
type rawBlock struct {
	typ    string
	fields map[string]json.RawMessage
	input  strings.Builder
	text   strings.Builder
}

type pendingCitation struct {
	url   *openresponses.URLCitation
	other *openresponses.UnknownAnnotation
}

// consume drains one stream into the emitter.
func (d *decoder) consume(stream interface {
	Next() bool
	Current() sdk.MessageStreamEventUnion
	Err() error
	Close() error
}) error {
	defer stream.Close()
	for stream.Next() {
		if err := d.event(stream.Current()); err != nil {
			return err
		}
	}
	return mapError(stream.Err())
}

func (d *decoder) event(ev sdk.MessageStreamEventUnion) error {
	switch v := ev.AsAny().(type) {
	case sdk.MessageStartEvent:
		if !d.seen {
			d.seen = true
			// Before the first event, so response.created carries it.
			if v.Message.Model != "" {
				d.em.Response().Model = string(v.Message.Model)
			}
		}
		d.split = nil
		if v.Message.Usage.JSON.CacheCreation.Valid() {
			split := v.Message.Usage.CacheCreation
			d.split = &split
		}
		d.turn = usage(v.Message.Usage.InputTokens, v.Message.Usage.CacheReadInputTokens, v.Message.Usage.CacheCreationInputTokens,
			d.split, v.Message.Usage.OutputTokens, v.Message.Usage.OutputTokensDetails.ThinkingTokens)
		return nil
	case sdk.ContentBlockStartEvent:
		return d.startBlock(v)
	case sdk.ContentBlockDeltaEvent:
		return d.delta(v)
	case sdk.ContentBlockStopEvent:
		return d.blockStop()
	case sdk.MessageDeltaEvent:
		d.stop = v.Delta.StopReason
		d.stopDetails = v.Delta.StopDetails
		if v.Usage.InputTokens > 0 {
			// message_delta has no split by TTL; the one from message_start
			// stands if it still sums to the count.
			d.turn = usage(v.Usage.InputTokens, v.Usage.CacheReadInputTokens, v.Usage.CacheCreationInputTokens,
				d.split, v.Usage.OutputTokens, v.Usage.OutputTokensDetails.ThinkingTokens)
		} else {
			d.turn.OutputTokens = int(v.Usage.OutputTokens)
			d.turn.OutputTokensDetails.ReasoningTokens = int(v.Usage.OutputTokensDetails.ThinkingTokens)
			d.turn.TotalTokens = d.turn.InputTokens + d.turn.OutputTokens
		}
		return nil
	case sdk.MessageStopEvent:
		d.usage = addUsage(d.usage, d.turn)
		u := d.usage
		d.em.Response().Usage = &u
		return nil
	}
	return nil
}

func (d *decoder) startBlock(ev sdk.ContentBlockStartEvent) error {
	switch ev.ContentBlock.Type {
	case "text":
		m, err := d.openMessage()
		if err != nil {
			return err
		}
		d.blockStart = utf8.RuneCountInString(messageText(m))
		if ev.ContentBlock.Text != "" {
			return m.Text(ev.ContentBlock.Text)
		}
		return nil
	case "tool_use":
		if err := d.closeOpen(); err != nil {
			return err
		}
		w, err := d.em.FunctionCall(ev.ContentBlock.ID, ev.ContentBlock.Name)
		if err != nil {
			return err
		}
		d.call, d.callWrote = w, false
		return nil
	case "thinking":
		if err := d.closeOpen(); err != nil {
			return err
		}
		r, err := d.em.Reasoning()
		if err != nil {
			return err
		}
		d.reasoning = r
		if ev.ContentBlock.Thinking != "" {
			if err := r.Summary(ev.ContentBlock.Thinking); err != nil {
				return err
			}
		}
		if ev.ContentBlock.Signature != "" {
			r.EncryptedContent(ev.ContentBlock.Signature)
		}
		return nil
	}
	// redacted_thinking, server_tool_use, the tool result blocks and
	// anything newer: kept whole and passed through.
	if err := d.closeOpen(); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(ev.ContentBlock.RawJSON()), &fields); err != nil {
		return openresponses.ServerError("invalid_upstream_response", ev.ContentBlock.Type+" block: "+err.Error())
	}
	d.raw = &rawBlock{typ: ev.ContentBlock.Type, fields: fields}
	return nil
}

func (d *decoder) delta(ev sdk.ContentBlockDeltaEvent) error {
	switch ev.Delta.Type {
	case "text_delta":
		if d.raw != nil {
			d.raw.text.WriteString(ev.Delta.Text)
			return nil
		}
		m, err := d.openMessage()
		if err != nil {
			return err
		}
		return m.Text(ev.Delta.Text)
	case "input_json_delta":
		if d.call != nil {
			d.callWrote = true
			return d.call.Arguments(ev.Delta.PartialJSON)
		}
		if d.raw != nil {
			d.raw.input.WriteString(ev.Delta.PartialJSON)
		}
		return nil
	case "thinking_delta":
		if d.reasoning == nil {
			r, err := d.em.Reasoning()
			if err != nil {
				return err
			}
			d.reasoning = r
		}
		return d.reasoning.Summary(ev.Delta.Thinking)
	case "signature_delta":
		if d.reasoning != nil {
			d.reasoning.EncryptedContent(d.reasoning.Item().EncryptedContent + ev.Delta.Signature)
		}
		return nil
	case "citations_delta":
		return d.cite(ev.Delta.Citation)
	}
	return nil
}

// cite records a citation for the open text block. Web search results
// are url_citation annotations; document citations have no standard
// shape and pass through as anthropic.* annotations.
func (d *decoder) cite(c sdk.CitationsDeltaCitationUnion) error {
	if c.Type == "web_search_result_location" {
		d.citations = append(d.citations, pendingCitation{url: &openresponses.URLCitation{URL: c.URL, Title: c.Title, StartIndex: d.blockStart}})
		return nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(c.RawJSON()), &fields); err != nil {
		return openresponses.ServerError("invalid_upstream_response", "citation: "+err.Error())
	}
	fields["type"] = json.RawMessage(strconvQuote(slug + c.Type))
	raw, err := json.Marshal(fields)
	if err != nil {
		return err
	}
	d.citations = append(d.citations, pendingCitation{other: &openresponses.UnknownAnnotation{Type: slug + c.Type, Raw: raw}})
	return nil
}

// blockStop ends a content block. A tool_use or thinking block stays
// open until the next block starts or the response ends: Claude sends
// content_block_stop before the message_delta carrying the stop reason,
// and a block the token limit cut off must still be open when Incomplete
// marks it.
func (d *decoder) blockStop() error {
	switch {
	case d.call != nil:
		if !d.callWrote {
			d.callWrote = true
			return d.call.Arguments("{}")
		}
		return nil
	case d.reasoning != nil:
		return nil
	case d.raw != nil:
		raw := d.raw
		d.raw = nil
		return d.extension(raw)
	case d.msg != nil && len(d.citations) > 0:
		// The message stays open so the next text block extends it; only
		// the citations need the block boundary.
		end := utf8.RuneCountInString(messageText(d.msg))
		for _, c := range d.citations {
			var err error
			if c.url != nil {
				c.url.EndIndex = end
				err = d.msg.Annotation(c.url)
			} else {
				err = d.msg.Annotation(c.other)
			}
			if err != nil {
				return err
			}
		}
		d.citations = nil
	}
	return nil
}

// extension emits a block the specification has no item for as
// {"type": "anthropic.<block type>", "id", "status", "block": <block>},
// the shape the request encoder replays.
func (d *decoder) extension(raw *rawBlock) error {
	if raw.input.Len() > 0 {
		input := raw.input.String()
		if !json.Valid([]byte(input)) {
			return openresponses.ServerError("invalid_upstream_response", raw.typ+" block: input is not valid JSON")
		}
		raw.fields["input"] = json.RawMessage(input)
	}
	if raw.text.Len() > 0 {
		var text string
		_ = json.Unmarshal(raw.fields["text"], &text)
		raw.fields["text"] = json.RawMessage(strconvQuote(text + raw.text.String()))
	}
	block, err := json.Marshal(raw.fields)
	if err != nil {
		return err
	}
	item := &openresponses.UnknownItem{Type: slug + raw.typ, ID: openresponses.NewID("ax"), Status: openresponses.StatusCompleted}
	item.Raw, err = json.Marshal(struct {
		Type   string               `json:"type"`
		ID     string               `json:"id"`
		Status openresponses.Status `json:"status"`
		Block  json.RawMessage      `json:"block"`
	}{item.Type, item.ID, item.Status, block})
	if err != nil {
		return err
	}
	return d.em.Item(item)
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
	d.blockStart = 0
	return m, nil
}

func (d *decoder) closeOpen() error {
	switch {
	case d.msg != nil:
		m := d.msg
		d.msg = nil
		d.citations = nil
		return m.Close()
	case d.reasoning != nil:
		r := d.reasoning
		d.reasoning = nil
		return r.Close()
	case d.call != nil:
		w := d.call
		d.call = nil
		return w.Close()
	}
	return nil
}

func messageText(m *openresponses.MessageWriter) string {
	content := m.Item().Content
	if n := len(content); n > 0 {
		if t, ok := content[n-1].(*openresponses.OutputText); ok {
			return t.Text
		}
	}
	return ""
}

// finish ends the response according to the last stop reason.
func (d *decoder) finish() error {
	switch d.stop {
	case sdk.StopReasonEndTurn, sdk.StopReasonToolUse, sdk.StopReasonStopSequence, sdk.StopReasonPauseTurn:
		// pause_turn reaches here only when the continuations ran out;
		// what was produced is a valid response.
		return d.em.Complete()
	case sdk.StopReasonMaxTokens, sdk.StopReasonModelContextWindowExceeded:
		return d.em.Incomplete(openresponses.IncompleteReasonMaxOutputTokens)
	case sdk.StopReasonRefusal:
		// A safety classifier ended the generation, which is a content
		// filter. Claude's explanation, when it sends one, is the refusal
		// part; the adapter writes none of its own.
		if text := d.stopDetails.Explanation; text != "" {
			m, err := d.openMessage()
			if err != nil {
				return err
			}
			if err := m.Refusal(text); err != nil {
				return err
			}
		}
		d.refusalCategory = string(d.stopDetails.Category)
		return d.em.Incomplete(openresponses.IncompleteReasonContentFilter)
	case "":
		return openresponses.ServerError("truncated_stream", "the Messages stream ended without a stop reason")
	}
	return openresponses.ModelError(string(d.stop), "Claude stopped generating: "+string(d.stop))
}

// usage maps token counts. Open Responses counts every input token,
// cached ones included, in input_tokens. Cache writes are reported as
// the shared cache_write_tokens member, split by TTL when Claude's split
// sums to its count.
func usage(input, cacheRead, cacheCreation int64, split *sdk.CacheCreation, output, thinking int64) openresponses.Usage {
	u := openresponses.Usage{
		InputTokens:  int(input + cacheRead + cacheCreation),
		OutputTokens: int(output),
	}
	u.InputTokensDetails.CachedTokens = int(cacheRead)
	u.OutputTokensDetails.ReasoningTokens = int(thinking)
	u.TotalTokens = u.InputTokens + u.OutputTokens
	if cacheCreation > 0 {
		if split != nil && split.Ephemeral5mInputTokens+split.Ephemeral1hInputTokens == cacheCreation {
			u.InputTokensDetails.SetCacheWriteTokensByTTL(ttlSplit(map[string]int{
				"5m": int(split.Ephemeral5mInputTokens),
				"1h": int(split.Ephemeral1hInputTokens),
			}))
		} else {
			u.InputTokensDetails.SetCacheWriteTokens(int(cacheCreation))
		}
	}
	return u
}

// ttlSplit drops the entries of a split by TTL that wrote nothing.
func ttlSplit(m map[string]int) map[string]int {
	for k, n := range m {
		if n == 0 {
			delete(m, k)
		}
	}
	return m
}

func addUsage(a, b openresponses.Usage) openresponses.Usage {
	a.InputTokens += b.InputTokens
	a.OutputTokens += b.OutputTokens
	a.TotalTokens += b.TotalTokens
	a.InputTokensDetails.CachedTokens += b.InputTokensDetails.CachedTokens
	a.OutputTokensDetails.ReasoningTokens += b.OutputTokensDetails.ReasoningTokens
	aw, aok := a.InputTokensDetails.CacheWriteTokens()
	bw, bok := b.InputTokensDetails.CacheWriteTokens()
	if !aok && !bok {
		return a
	}
	// A fresh map, so the usage already on the response is not changed.
	a.InputTokensDetails.Extra = maps.Clone(a.InputTokensDetails.Extra)
	as, asok := a.InputTokensDetails.CacheWriteTokensByTTL()
	bs, bsok := b.InputTokensDetails.CacheWriteTokensByTTL()
	if (!aok || asok) && (!bok || bsok) {
		sum := map[string]int{}
		for k, n := range as {
			sum[k] += n
		}
		for k, n := range bs {
			sum[k] += n
		}
		a.InputTokensDetails.SetCacheWriteTokensByTTL(sum)
	} else {
		// A turn without the split leaves the sum without one.
		a.InputTokensDetails.SetCacheWriteTokens(aw + bw)
	}
	return a
}

// mapError converts an SDK error into the matching *openresponses.Error.
// The API's error type is the code and decides the type and status, so
// a failure reads the same whether it came as an HTTP status or as an
// error event inside a 200 stream; an error type the table does not
// know falls back to the HTTP status, and a 2xx status never stands.
// The headers that describe the failure (Retry-After, the rate-limit
// family, the request id) travel with it.
func mapError(err error) error {
	if err == nil {
		return nil
	}
	var oerr *openresponses.Error
	if errors.As(err, &oerr) {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var apiErr *sdk.Error
	if !errors.As(err, &apiErr) {
		return openresponses.ServerError("upstream_error", err.Error())
	}
	code := string(apiErr.Type())
	if code == "" {
		code = "api_error"
	}
	message := apiMessage(apiErr)
	status, known := errorStatus[code]
	if !known && apiErr.StatusCode >= 300 {
		status = apiErr.StatusCode
	}
	var out *openresponses.Error
	switch {
	case code == "invalid_request_error" && strings.Contains(strings.ToLower(message), "prompt is too long"):
		out = openresponses.ContextLengthExceeded(message)
	case status == http.StatusNotFound:
		out = openresponses.NotFound(code, message, "")
	case status == http.StatusTooManyRequests:
		out = openresponses.TooManyRequests(code, message)
	case status >= 400 && status < 500:
		// Credential and permission refusals land here too: a retry
		// cannot fix them, so they are never server_error.
		out = openresponses.InvalidRequest(code, message, "")
	default:
		out = openresponses.ServerError(code, message)
	}
	// Zero leaves the status to the type.
	out.StatusCode = status
	if apiErr.Response != nil {
		for name, values := range apiErr.Response.Header {
			if !failureHeader(name) {
				continue
			}
			for _, v := range values {
				out.WithHeader(name, v)
			}
		}
	}
	return out
}

// errorStatus is the HTTP status the Messages API gives each error type
// on a non-streamed response.
var errorStatus = map[string]int{
	"invalid_request_error": http.StatusBadRequest,
	"authentication_error":  http.StatusUnauthorized,
	"billing_error":         http.StatusPaymentRequired,
	"permission_error":      http.StatusForbidden,
	"not_found_error":       http.StatusNotFound,
	"request_too_large":     http.StatusRequestEntityTooLarge,
	"rate_limit_error":      http.StatusTooManyRequests,
	"api_error":             http.StatusInternalServerError,
	"timeout_error":         http.StatusGatewayTimeout,
	"overloaded_error":      529,
}

// failureHeader reports whether a response header describes the failure
// and should travel with the error.
func failureHeader(name string) bool {
	switch strings.ToLower(name) {
	case "retry-after", "request-id", "x-request-id":
		return true
	}
	lower := strings.ToLower(name)
	return strings.HasPrefix(lower, "ratelimit-") || strings.HasPrefix(lower, "x-ratelimit-") || strings.HasPrefix(lower, "anthropic-ratelimit-")
}

// apiMessage is the message inside the API's error envelope, or the
// SDK's rendering when there is none.
func apiMessage(err *sdk.Error) string {
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(err.RawJSON()), &envelope) == nil && envelope.Error.Message != "" {
		return envelope.Error.Message
	}
	return err.Error()
}
