package openresponses

import (
	"encoding/json"
	"time"

	"github.com/ChristopherDavenport/openresponses/internal/jsonx"
)

// ObjectResponse and ObjectCompaction are the "object" values of the two
// resources.
const (
	ObjectResponse   = "response"
	ObjectCompaction = "response.compaction"
)

// Response is the response resource returned by POST /responses and
// carried by the response.* streaming events. The spec requires most
// fields to be present, so a Response built in Go emits them even when
// null or zero. A Response decoded from the wire remembers which keys
// its source left out and re-encodes without them for as long as those
// fields keep their zero value, so a proxy passes a lenient upstream
// through unchanged instead of inventing a temperature of 0 or store of
// false. Setting such a field makes it appear again.
type Response struct {
	ID                 string             `json:"id"`
	Object             string             `json:"object"`
	CreatedAt          int64              `json:"created_at"`
	CompletedAt        *int64             `json:"completed_at"`
	Status             ResponseStatus     `json:"status"`
	IncompleteDetails  *IncompleteDetails `json:"incomplete_details"`
	Model              string             `json:"model"`
	PreviousResponseID *string            `json:"previous_response_id"`
	Instructions       *string            `json:"instructions"`
	Output             Items              `json:"output"`
	Error              *ErrorPayload      `json:"error"`
	Tools              Tools              `json:"tools"`
	ToolChoice         ToolChoice         `json:"tool_choice"`
	Truncation         Truncation         `json:"truncation"`
	ParallelToolCalls  bool               `json:"parallel_tool_calls"`
	Text               TextConfig         `json:"text"`
	TopP               float64            `json:"top_p"`
	PresencePenalty    float64            `json:"presence_penalty"`
	FrequencyPenalty   float64            `json:"frequency_penalty"`
	TopLogprobs        int                `json:"top_logprobs"`
	Temperature        float64            `json:"temperature"`
	Reasoning          ReasoningConfig    `json:"reasoning"`
	Usage              *Usage             `json:"usage"`
	MaxOutputTokens    *int               `json:"max_output_tokens"`
	MaxToolCalls       *int               `json:"max_tool_calls"`
	Store              bool               `json:"store"`
	Background         bool               `json:"background"`
	ServiceTier        ServiceTier        `json:"service_tier"`
	Metadata           map[string]string  `json:"metadata"`
	SafetyIdentifier   *string            `json:"safety_identifier"`
	PromptCacheKey     *string            `json:"prompt_cache_key"`

	// Extra holds top-level keys not defined by the spec.
	Extra map[string]any `json:"-"`

	// absent records the spec keys a decoded response did not carry. It
	// is read-only after decode and shared by clones.
	absent map[string]bool
}

// MarshalJSON emits the resource with spec-shaped defaults: object is
// always "response", nil slices become [], nil metadata becomes {}, and
// empty enums take their documented defaults. Keys that were absent when
// the response was decoded and whose fields are still zero are left out.
func (r Response) MarshalJSON() ([]byte, error) {
	type plain Response
	cp := plain(r)
	cp.Object = ObjectResponse
	if cp.Output == nil {
		cp.Output = Items{}
	}
	if cp.Tools == nil {
		cp.Tools = Tools{}
	}
	if cp.Metadata == nil {
		cp.Metadata = map[string]string{}
	}
	if cp.ToolChoice.IsZero() {
		cp.ToolChoice.Mode = ToolChoiceAuto
	}
	if cp.Truncation == "" {
		cp.Truncation = TruncationDisabled
	}
	if cp.ServiceTier == "" {
		cp.ServiceTier = ServiceTierDefault
	}
	if cp.Status == "" {
		cp.Status = ResponseStatusInProgress
	}
	return jsonx.MarshalWithExtra(cp, r.Extra, jsonx.ZeroFields(&r, r.absent))
}

// UnmarshalJSON captures unknown keys into Extra and remembers which
// spec keys were absent.
func (r *Response) UnmarshalJSON(data []byte) error {
	type plain Response
	var p plain
	extra, absent, err := jsonx.UnmarshalExtra(data, &p)
	if err != nil {
		return err
	}
	*r = Response(p)
	r.Extra = extra
	r.absent = absent
	return nil
}

// OutputText concatenates the text of every output_text part in every
// assistant message of the output.
func (r *Response) OutputText() string {
	var out []byte
	for _, item := range r.Output {
		m, ok := item.(*Message)
		if !ok || m.Role != RoleAssistant {
			continue
		}
		for _, part := range m.Content {
			if t, ok := part.(*OutputText); ok {
				out = append(out, t.Text...)
			}
		}
	}
	return string(out)
}

// FunctionCalls returns every function_call item in the output.
func (r *Response) FunctionCalls() []*FunctionCall {
	var calls []*FunctionCall
	for _, item := range r.Output {
		if fc, ok := item.(*FunctionCall); ok {
			calls = append(calls, fc)
		}
	}
	return calls
}

// Clone returns a shallow copy of the response with its own Output,
// Tools and Metadata containers and its own copy of every value it holds
// directly: reasoning, text and tool choice, with their Extra members,
// and Extra itself. Items, tools and the objects behind the pointer
// fields (Usage, Error, IncompleteDetails) are shared.
func (r *Response) Clone() *Response {
	cp := *r
	if r.Output != nil {
		cp.Output = append(Items(nil), r.Output...)
	}
	if r.Tools != nil {
		cp.Tools = append(Tools(nil), r.Tools...)
	}
	cp.ToolChoice = cloneToolChoice(r.ToolChoice)
	cp.Text = cloneTextConfig(r.Text)
	cp.Reasoning = ReasoningConfig{Effort: r.Reasoning.Effort, Summary: r.Reasoning.Summary, Extra: cloneExtra(r.Reasoning.Extra)}
	cp.Metadata = cloneMap(r.Metadata)
	cp.Extra = cloneExtra(r.Extra)
	return &cp
}

// NewResponse builds an in_progress response that echoes the request's
// settings, as a server adapter would before generating output. The ID is
// the caller's to set; CreatedAt is now. Tools, tool choice, reasoning
// and metadata are copied, so filling in defaults never alters req.
// Reasoning is echoed without its Extra members: a response carries an
// extension such as [MemberEffort] only when the adapter honoured it
// (docs/extensions.md, section 3), so the adapter sets it.
func NewResponse(req Request) *Response {
	resp := &Response{
		Object:             ObjectResponse,
		CreatedAt:          time.Now().Unix(),
		Status:             ResponseStatusInProgress,
		Model:              req.Model,
		Output:             Items{},
		Tools:              cloneTools(req.Tools),
		ToolChoice:         cloneToolChoice(req.ToolChoice),
		Truncation:         req.Truncation,
		ParallelToolCalls:  req.ParallelToolCalls == nil || *req.ParallelToolCalls,
		Text:               cloneTextConfig(req.Text),
		TopP:               derefOr(req.TopP, 1),
		PresencePenalty:    derefOr(req.PresencePenalty, 0),
		FrequencyPenalty:   derefOr(req.FrequencyPenalty, 0),
		TopLogprobs:        derefOr(req.TopLogprobs, 0),
		Temperature:        derefOr(req.Temperature, 1),
		Reasoning:          ReasoningConfig{Effort: req.Reasoning.Effort, Summary: req.Reasoning.Summary},
		MaxOutputTokens:    cloneptr(req.MaxOutputTokens),
		MaxToolCalls:       cloneptr(req.MaxToolCalls),
		Store:              req.Stored(),
		Background:         req.Background,
		ServiceTier:        req.ServiceTier,
		Metadata:           cloneMap(req.Metadata),
		PreviousResponseID: nilIfEmpty(req.PreviousResponseID),
		Instructions:       nilIfEmpty(req.Instructions),
		SafetyIdentifier:   nilIfEmpty(req.SafetyIdentifier),
		PromptCacheKey:     nilIfEmpty(req.PromptCacheKey),
	}
	return resp
}

// Complete marks the response completed as of now.
func (r *Response) Complete() {
	r.Status = ResponseStatusCompleted
	now := time.Now().Unix()
	r.CompletedAt = &now
}

// Incomplete marks the response incomplete as of now with the given
// reason.
func (r *Response) Incomplete(reason IncompleteReason) {
	r.Status = ResponseStatusIncomplete
	r.IncompleteDetails = &IncompleteDetails{Reason: reason}
	now := time.Now().Unix()
	r.CompletedAt = &now
}

// Fail marks the response failed and records err in the error field.
func (r *Response) Fail(err error) {
	r.Status = ResponseStatusFailed
	payload := AsError(err).Payload()
	r.Error = &payload
}

// cloneTools copies the slice and every function tool so that the
// resource-form defaults (strict present) do not leak into the request.
// Other tool types are shared; they are treated as immutable.
func cloneTools(tools Tools) Tools {
	out := make(Tools, len(tools))
	for i, tool := range tools {
		ft, ok := tool.(*FunctionTool)
		if !ok {
			out[i] = tool
			continue
		}
		cp := *ft
		cp.Extra = cloneExtra(ft.Extra)
		if cp.Parameters != nil {
			cp.Parameters = append(json.RawMessage(nil), ft.Parameters...)
		}
		if cp.Strict == nil {
			strict := false
			cp.Strict = &strict
		} else {
			cp.Strict = cloneptr(ft.Strict)
		}
		out[i] = &cp
	}
	return out
}

func cloneToolChoice(tc ToolChoice) ToolChoice {
	out := ToolChoice{Mode: tc.Mode}
	if tc.Function != nil {
		fn := *tc.Function
		fn.Extra = cloneExtra(tc.Function.Extra)
		out.Function = &fn
	}
	if tc.Allowed != nil {
		allowed := *tc.Allowed
		allowed.Extra = cloneExtra(tc.Allowed.Extra)
		if tc.Allowed.Tools != nil {
			allowed.Tools = make([]ToolReference, len(tc.Allowed.Tools))
			for i, ref := range tc.Allowed.Tools {
				ref.Extra = cloneExtra(ref.Extra)
				allowed.Tools[i] = ref
			}
		}
		out.Allowed = &allowed
	}
	return out
}

func cloneTextConfig(tc TextConfig) TextConfig {
	out := TextConfig{Verbosity: tc.Verbosity, Extra: cloneExtra(tc.Extra)}
	if tc.Format != nil {
		format := *tc.Format
		if format.Schema != nil {
			format.Schema = append(json.RawMessage(nil), tc.Format.Schema...)
		}
		format.Strict = cloneptr(tc.Format.Strict)
		format.Extra = cloneExtra(tc.Format.Extra)
		out.Format = &format
	}
	return out
}

// cloneExtra deep-copies an Extra map: the objects and arrays decoding
// produces are copied at every depth, so the copy shares nothing that
// can be changed in place.
func cloneExtra(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = cloneValue(v)
	}
	return out
}

// cloneValue deep-copies a decoded JSON value and the integer maps the
// extension setters store.
func cloneValue(v any) any {
	switch v := v.(type) {
	case map[string]any:
		return cloneExtra(v)
	case []any:
		out := make([]any, len(v))
		for i, e := range v {
			out[i] = cloneValue(e)
		}
		return out
	case map[string]int:
		out := make(map[string]int, len(v))
		for k, n := range v {
			out[k] = n
		}
		return out
	case json.RawMessage:
		return append(json.RawMessage(nil), v...)
	}
	return v
}

func cloneMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func cloneptr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

func derefOr[T any](p *T, def T) T {
	if p == nil {
		return def
	}
	return *p
}

// IncompleteDetails explains why a response stopped early.
type IncompleteDetails struct {
	Reason IncompleteReason `json:"reason"`

	// Extra holds members not defined by the spec.
	Extra map[string]any `json:"-"`
}

// MarshalJSON emits the details with their Extra members.
func (d IncompleteDetails) MarshalJSON() ([]byte, error) {
	type plain IncompleteDetails
	return jsonx.MarshalWithExtra(plain(d), d.Extra, nil)
}

// UnmarshalJSON decodes the details and keeps unknown members in Extra.
func (d *IncompleteDetails) UnmarshalJSON(data []byte) error {
	type plain IncompleteDetails
	extra, _, err := jsonx.UnmarshalExtra(data, (*plain)(d))
	d.Extra = extra
	return err
}

// Usage is the token accounting for a response.
type Usage struct {
	InputTokens         int                 `json:"input_tokens"`
	OutputTokens        int                 `json:"output_tokens"`
	TotalTokens         int                 `json:"total_tokens"`
	InputTokensDetails  InputTokensDetails  `json:"input_tokens_details"`
	OutputTokensDetails OutputTokensDetails `json:"output_tokens_details"`

	// Extra holds members not defined by the spec.
	Extra map[string]any `json:"-"`
}

// MarshalJSON emits the usage with its Extra members.
func (u Usage) MarshalJSON() ([]byte, error) {
	type plain Usage
	return jsonx.MarshalWithExtra(plain(u), u.Extra, nil)
}

// UnmarshalJSON decodes the usage and keeps unknown members in Extra.
func (u *Usage) UnmarshalJSON(data []byte) error {
	type plain Usage
	extra, _, err := jsonx.UnmarshalExtra(data, (*plain)(u))
	u.Extra = extra
	return err
}

// InputTokensDetails breaks down input tokens.
type InputTokensDetails struct {
	CachedTokens int `json:"cached_tokens"`

	// Extra holds members not defined by the spec, such as
	// [MemberCacheWriteTokens].
	Extra map[string]any `json:"-"`
}

// MarshalJSON emits the details with their Extra members.
func (d InputTokensDetails) MarshalJSON() ([]byte, error) {
	type plain InputTokensDetails
	return jsonx.MarshalWithExtra(plain(d), d.Extra, nil)
}

// UnmarshalJSON decodes the details and keeps unknown members in Extra.
func (d *InputTokensDetails) UnmarshalJSON(data []byte) error {
	type plain InputTokensDetails
	extra, _, err := jsonx.UnmarshalExtra(data, (*plain)(d))
	d.Extra = extra
	return err
}

// OutputTokensDetails breaks down output tokens.
type OutputTokensDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`

	// Extra holds members not defined by the spec.
	Extra map[string]any `json:"-"`
}

// MarshalJSON emits the details with their Extra members.
func (d OutputTokensDetails) MarshalJSON() ([]byte, error) {
	type plain OutputTokensDetails
	return jsonx.MarshalWithExtra(plain(d), d.Extra, nil)
}

// UnmarshalJSON decodes the details and keeps unknown members in Extra.
func (d *OutputTokensDetails) UnmarshalJSON(data []byte) error {
	type plain OutputTokensDetails
	extra, _, err := jsonx.UnmarshalExtra(data, (*plain)(d))
	d.Extra = extra
	return err
}

// CompactResponse is the resource returned by POST /responses/compact.
// Usage is a pointer to match [Response]; the spec requires the key, so
// a nil Usage is emitted as zero counts.
type CompactResponse struct {
	ID        string `json:"id"`
	Object    string `json:"object"`
	Output    Items  `json:"output"`
	CreatedAt int64  `json:"created_at"`
	Usage     *Usage `json:"usage"`

	// Extra holds top-level keys not defined by the spec.
	Extra map[string]any `json:"-"`

	absent map[string]bool
}

// MarshalJSON emits the resource with object fixed to
// "response.compaction" and a non-null output array. Keys absent at
// decode time whose fields are still zero are left out, as for
// [Response].
func (r CompactResponse) MarshalJSON() ([]byte, error) {
	type plain CompactResponse
	cp := plain(r)
	cp.Object = ObjectCompaction
	if cp.Output == nil {
		cp.Output = Items{}
	}
	if cp.Usage == nil {
		cp.Usage = &Usage{}
	}
	return jsonx.MarshalWithExtra(cp, r.Extra, jsonx.ZeroFields(&r, r.absent))
}

// UnmarshalJSON captures unknown keys into Extra.
func (r *CompactResponse) UnmarshalJSON(data []byte) error {
	type plain CompactResponse
	var p plain
	extra, absent, err := jsonx.UnmarshalExtra(data, &p)
	if err != nil {
		return err
	}
	*r = CompactResponse(p)
	r.Extra = extra
	r.absent = absent
	return nil
}

var _ json.Marshaler = Response{}
