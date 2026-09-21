package gemini

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ChristopherDavenport/openresponses"
	"google.golang.org/genai"
)

// CreateStream runs one GenerateContent call and streams it to sink.
// Gemini's parts map onto the Emitter's items in order: text opens or
// extends a message, thoughts open or extend a reasoning item, a
// function call arrives whole and is emitted in one step, and any other
// part becomes a gemini.* extension item. The candidate's finish reason
// ends the response.
func (a *Adapter) CreateStream(ctx context.Context, req openresponses.Request, sink openresponses.EventSink) error {
	contents, cfg, err := encodeRequest(req)
	if err != nil {
		return err
	}
	d := &decoder{em: openresponses.NewEmitter(sink, openresponses.NewResponse(req))}
	for chunk, err := range a.client.Models.GenerateContentStream(ctx, req.Model, contents, cfg) {
		if err != nil {
			return mapError(err)
		}
		done, err := d.chunk(chunk)
		if err != nil || done {
			return err
		}
	}
	return openresponses.ServerError("truncated_stream", "the Gemini stream ended without a finish reason")
}

// decoder folds GenerateContent chunks into Emitter calls. At most one
// writer is open at a time, matching Gemini's sequential parts.
type decoder struct {
	em        *openresponses.Emitter
	msg       *openresponses.MessageWriter
	reasoning *openresponses.ReasoningWriter
	// cited is the byte offset in the open message's text after the
	// last citation, so grounding segments are located in order.
	cited int
	seen  bool
}

// chunk applies one response chunk. It reports true once a terminal
// event has been sent.
func (d *decoder) chunk(chunk *genai.GenerateContentResponse) (bool, error) {
	if !d.seen {
		d.seen = true
		// Before the first event, so response.created carries it.
		if chunk.ModelVersion != "" {
			d.em.Response().Model = chunk.ModelVersion
		}
	}
	if chunk.UsageMetadata != nil {
		d.em.Response().Usage = usage(chunk.UsageMetadata)
	}
	if fb := chunk.PromptFeedback; fb != nil && fb.BlockReason != "" {
		// The input was refused before generation; there are no candidates.
		return true, d.em.Incomplete(openresponses.IncompleteReasonContentFilter)
	}
	if len(chunk.Candidates) == 0 {
		return false, nil
	}
	c := chunk.Candidates[0]
	if c.Content != nil {
		for _, p := range c.Content.Parts {
			if err := d.part(p); err != nil {
				return false, err
			}
		}
	}
	if c.LogprobsResult != nil && d.msg != nil {
		if err := d.msg.Logprobs(logprobs(c.LogprobsResult)...); err != nil {
			return false, err
		}
	}
	if c.GroundingMetadata != nil {
		if err := d.ground(c.GroundingMetadata); err != nil {
			return false, err
		}
	}
	if c.FinishReason == "" {
		return false, nil
	}
	return true, d.finish(c)
}

// part routes one part to the right writer. A thought signature on a
// part that is not itself a thought (the shape Gemini 3 uses: the
// signature rides on the first part after thinking) is carried by a
// reasoning item emitted just before that part, so the request encoder
// can put it back where it came from.
func (d *decoder) part(p *genai.Part) error {
	if len(p.ThoughtSignature) > 0 && !p.Thought {
		r, err := d.openReasoning()
		if err != nil {
			return err
		}
		r.EncryptedContent(base64.StdEncoding.EncodeToString(p.ThoughtSignature))
		if err := d.closeOpen(); err != nil {
			return err
		}
	}
	switch {
	case p.FunctionCall != nil:
		if err := d.closeOpen(); err != nil {
			return err
		}
		args := "{}"
		if p.FunctionCall.Args != nil {
			data, err := json.Marshal(p.FunctionCall.Args)
			if err != nil {
				return openresponses.ServerError("invalid_upstream_response", "function call arguments: "+err.Error())
			}
			args = string(data)
		}
		w, err := d.em.FunctionCall(p.FunctionCall.ID, p.FunctionCall.Name)
		if err != nil {
			return err
		}
		if err := w.Arguments(args); err != nil {
			return err
		}
		return w.Close()
	case p.Thought:
		r, err := d.openReasoning()
		if err != nil {
			return err
		}
		if p.Text != "" {
			if err := r.Summary(p.Text); err != nil {
				return err
			}
		}
		if len(p.ThoughtSignature) > 0 {
			r.EncryptedContent(base64.StdEncoding.EncodeToString(p.ThoughtSignature))
		}
		return nil
	case p.Text != "":
		m, err := d.openMessage()
		if err != nil {
			return err
		}
		return m.Text(p.Text)
	case p.ExecutableCode != nil:
		return d.extension("executable_code", p)
	case p.CodeExecutionResult != nil:
		return d.extension("code_execution_result", p)
	case p.InlineData != nil:
		return d.extension("inline_data", p)
	case p.FileData != nil:
		return d.extension("file_data", p)
	}
	// An empty part carries nothing worth an event.
	return nil
}

// extension emits a part the specification has no item for as
// {"type": "gemini.<kind>", "id", "status", "part": <part>}, the shape
// the request encoder replays.
func (d *decoder) extension(kind string, p *genai.Part) error {
	if err := d.closeOpen(); err != nil {
		return err
	}
	part, err := json.Marshal(p)
	if err != nil {
		return openresponses.ServerError("invalid_upstream_response", kind+" part: "+err.Error())
	}
	item := &openresponses.UnknownItem{Type: slug + kind, ID: openresponses.NewID("gx"), Status: openresponses.StatusCompleted}
	item.Raw, err = json.Marshal(struct {
		Type   string               `json:"type"`
		ID     string               `json:"id"`
		Status openresponses.Status `json:"status"`
		Part   json.RawMessage      `json:"part"`
	}{item.Type, item.ID, item.Status, part})
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
	d.cited = 0
	return m, nil
}

func (d *decoder) openReasoning() (*openresponses.ReasoningWriter, error) {
	if d.reasoning != nil {
		return d.reasoning, nil
	}
	if err := d.closeOpen(); err != nil {
		return nil, err
	}
	r, err := d.em.Reasoning()
	if err != nil {
		return nil, err
	}
	d.reasoning = r
	return r, nil
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
	}
	return nil
}

// ground turns grounding supports into url_citation annotations on the
// open message. Gemini locates a support by the cited text and byte
// offsets within a part; the specification wants character offsets
// within the message, so each segment is found in the message text and
// converted. Citations that reach us with no message open get one, so
// nothing is lost.
func (d *decoder) ground(gm *genai.GroundingMetadata) error {
	if len(gm.GroundingSupports) == 0 {
		return nil
	}
	m, err := d.openMessage()
	if err != nil {
		return err
	}
	text := messageText(m)
	for _, support := range gm.GroundingSupports {
		if support == nil || support.Segment == nil {
			continue
		}
		start, end := locate(text, support.Segment, &d.cited)
		for _, i := range support.GroundingChunkIndices {
			if int(i) >= len(gm.GroundingChunks) || gm.GroundingChunks[i] == nil || gm.GroundingChunks[i].Web == nil {
				continue
			}
			web := gm.GroundingChunks[i].Web
			if err := m.Annotation(&openresponses.URLCitation{URL: web.URI, Title: web.Title, StartIndex: start, EndIndex: end}); err != nil {
				return err
			}
		}
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

// locate returns the character range of seg within text. The segment's
// own text is searched for from *cited onward, so repeated phrases
// resolve in order; when it is absent the byte offsets are used as
// given. end is exclusive.
func locate(text string, seg *genai.Segment, cited *int) (start, end int) {
	byteStart, byteEnd := int(seg.StartIndex), int(seg.EndIndex)
	if seg.Text != "" {
		if i := strings.Index(text[min(*cited, len(text)):], seg.Text); i >= 0 {
			byteStart = *cited + i
			byteEnd = byteStart + len(seg.Text)
			*cited = byteEnd
		}
	}
	byteStart = max(0, min(byteStart, len(text)))
	byteEnd = max(byteStart, min(byteEnd, len(text)))
	start = utf8.RuneCountInString(text[:byteStart])
	end = start + utf8.RuneCountInString(text[byteStart:byteEnd])
	return start, end
}

// finish ends the response according to the candidate's finish reason.
func (d *decoder) finish(c *genai.Candidate) error {
	switch c.FinishReason {
	case genai.FinishReasonStop:
		return d.em.Complete()
	case genai.FinishReasonMaxTokens:
		return d.em.Incomplete(openresponses.IncompleteReasonMaxOutputTokens)
	case genai.FinishReasonSafety, genai.FinishReasonRecitation, genai.FinishReasonBlocklist,
		genai.FinishReasonProhibitedContent, genai.FinishReasonSPII, genai.FinishReasonLanguage,
		genai.FinishReasonImageSafety, genai.FinishReasonImageProhibitedContent, genai.FinishReasonImageRecitation:
		return d.em.Incomplete(openresponses.IncompleteReasonContentFilter)
	}
	// MALFORMED_FUNCTION_CALL, UNEXPECTED_TOOL_CALL, TOO_MANY_TOOL_CALLS,
	// OTHER and the rest are failures of the generation itself.
	msg := c.FinishMessage
	if msg == "" {
		msg = "Gemini stopped generating: " + string(c.FinishReason)
	}
	return openresponses.ModelError(strings.ToLower(string(c.FinishReason)), msg)
}

// usage maps token counts. Open Responses counts every input token,
// cached ones included, in input_tokens, and every output token,
// thinking included, in output_tokens.
func usage(u *genai.GenerateContentResponseUsageMetadata) *openresponses.Usage {
	out := &openresponses.Usage{
		InputTokens:  int(u.PromptTokenCount) + int(u.ToolUsePromptTokenCount),
		OutputTokens: int(u.CandidatesTokenCount) + int(u.ThoughtsTokenCount),
		TotalTokens:  int(u.TotalTokenCount),
	}
	out.InputTokensDetails.CachedTokens = int(u.CachedContentTokenCount)
	out.OutputTokensDetails.ReasoningTokens = int(u.ThoughtsTokenCount)
	if out.TotalTokens == 0 {
		out.TotalTokens = out.InputTokens + out.OutputTokens
	}
	return out
}

// logprobs maps a chunk's chosen tokens and their alternatives.
func logprobs(r *genai.LogprobsResult) []openresponses.LogProb {
	out := make([]openresponses.LogProb, 0, len(r.ChosenCandidates))
	for i, chosen := range r.ChosenCandidates {
		if chosen == nil {
			continue
		}
		lp := openresponses.LogProb{Token: chosen.Token, Logprob: float64(chosen.LogProbability), Bytes: tokenBytes(chosen.Token)}
		if i < len(r.TopCandidates) && r.TopCandidates[i] != nil {
			for _, alt := range r.TopCandidates[i].Candidates {
				if alt == nil {
					continue
				}
				lp.TopLogprobs = append(lp.TopLogprobs, openresponses.TopLogProb{Token: alt.Token, Logprob: float64(alt.LogProbability), Bytes: tokenBytes(alt.Token)})
			}
		}
		out = append(out, lp)
	}
	return out
}

func tokenBytes(token string) []int {
	out := make([]int, len(token))
	for i := range len(token) {
		out[i] = int(token[i])
	}
	return out
}

// mapError converts an SDK error into the matching *openresponses.Error.
// The HTTP status decides the type, the gRPC status becomes the code,
// and a RetryInfo detail becomes Retry-After so a 429 keeps its meaning
// through the handler.
func mapError(err error) error {
	var oerr *openresponses.Error
	if errors.As(err, &oerr) {
		return err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var apiErr genai.APIError
	if !errors.As(err, &apiErr) {
		return openresponses.ServerError("upstream_error", err.Error())
	}
	code := strings.ToLower(apiErr.Status)
	if code == "" {
		code = "gemini_error"
	}
	var out *openresponses.Error
	switch {
	case apiErr.Code == http.StatusNotFound:
		out = openresponses.NotFound(code, apiErr.Message, "")
	case apiErr.Code == http.StatusTooManyRequests:
		out = openresponses.TooManyRequests(code, apiErr.Message)
		if delay, ok := retryDelay(apiErr.Details); ok {
			out.WithHeader("Retry-After", strconv.Itoa(delay))
		}
	case apiErr.Code >= 400 && apiErr.Code < 500:
		out = openresponses.InvalidRequest(code, apiErr.Message, "")
	default:
		out = openresponses.ServerError(code, apiErr.Message)
	}
	out.StatusCode = apiErr.Code
	return out
}

// retryDelay finds a google.rpc.RetryInfo detail and returns its delay
// in whole seconds, rounded up.
func retryDelay(details []map[string]any) (int, bool) {
	for _, detail := range details {
		if t, _ := detail["@type"].(string); !strings.HasSuffix(t, "google.rpc.RetryInfo") {
			continue
		}
		s, _ := detail["retryDelay"].(string)
		dur, err := time.ParseDuration(s)
		if err != nil {
			return 0, false
		}
		return int(math.Ceil(dur.Seconds())), true
	}
	return 0, false
}
