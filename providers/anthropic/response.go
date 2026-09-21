package anthropic

import (
	"context"
	"encoding/json"
	"errors"
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
	params, err := encodeRequest(req, a.maxTokens)
	if err != nil {
		return err
	}
	d := &decoder{em: openresponses.NewEmitter(sink, openresponses.NewResponse(req))}
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
	stop        sdk.StopReason
	stopDetails sdk.RefusalStopDetails
	seen        bool
}

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
		d.turn = usage(v.Message.Usage.InputTokens, v.Message.Usage.CacheReadInputTokens, v.Message.Usage.CacheCreationInputTokens,
			v.Message.Usage.OutputTokens, v.Message.Usage.OutputTokensDetails.ThinkingTokens)
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
			d.turn = usage(v.Usage.InputTokens, v.Usage.CacheReadInputTokens, v.Usage.CacheCreationInputTokens,
				v.Usage.OutputTokens, v.Usage.OutputTokensDetails.ThinkingTokens)
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

func (d *decoder) blockStop() error {
	switch {
	case d.call != nil:
		w := d.call
		d.call = nil
		if !d.callWrote {
			if err := w.Arguments("{}"); err != nil {
				return err
			}
		}
		return w.Close()
	case d.reasoning != nil:
		r := d.reasoning
		d.reasoning = nil
		return r.Close()
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
		m, err := d.openMessage()
		if err != nil {
			return err
		}
		text := d.stopDetails.Explanation
		if text == "" {
			text = "The request was refused"
			if d.stopDetails.Category != "" {
				text += " (" + string(d.stopDetails.Category) + ")"
			}
			text += "."
		}
		if err := m.Refusal(text); err != nil {
			return err
		}
		return d.em.Complete()
	case "":
		return openresponses.ServerError("truncated_stream", "the Messages stream ended without a stop reason")
	}
	return openresponses.ModelError(string(d.stop), "Claude stopped generating: "+string(d.stop))
}

// usage maps token counts. Open Responses counts every input token,
// cached ones included, in input_tokens.
func usage(input, cacheRead, cacheCreation, output, thinking int64) openresponses.Usage {
	u := openresponses.Usage{
		InputTokens:  int(input + cacheRead + cacheCreation),
		OutputTokens: int(output),
	}
	u.InputTokensDetails.CachedTokens = int(cacheRead)
	u.OutputTokensDetails.ReasoningTokens = int(thinking)
	u.TotalTokens = u.InputTokens + u.OutputTokens
	return u
}

func addUsage(a, b openresponses.Usage) openresponses.Usage {
	a.InputTokens += b.InputTokens
	a.OutputTokens += b.OutputTokens
	a.TotalTokens += b.TotalTokens
	a.InputTokensDetails.CachedTokens += b.InputTokensDetails.CachedTokens
	a.OutputTokensDetails.ReasoningTokens += b.OutputTokensDetails.ReasoningTokens
	return a
}

// mapError converts an SDK error into the matching *openresponses.Error.
// The HTTP status decides the type, the API's error type is the code,
// and the headers that describe the failure (Retry-After, the rate-limit
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
	var out *openresponses.Error
	switch {
	case apiErr.StatusCode == http.StatusNotFound:
		out = openresponses.NotFound(code, message, "")
	case apiErr.StatusCode == http.StatusTooManyRequests:
		out = openresponses.TooManyRequests(code, message)
	case apiErr.StatusCode >= 400 && apiErr.StatusCode < 500:
		out = openresponses.InvalidRequest(code, message, "")
	default:
		out = openresponses.ServerError(code, message)
	}
	out.StatusCode = apiErr.StatusCode
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
