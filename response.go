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
// fields to be present, so they are emitted even when null or zero.
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
	Reasoning          *ReasoningConfig   `json:"reasoning"`
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
}

// MarshalJSON emits the resource with spec-shaped defaults: object is
// always "response", nil slices become [], nil metadata becomes {}, and
// empty enums take their documented defaults.
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
	return jsonx.MarshalWithExtra(cp, r.Extra)
}

// UnmarshalJSON captures unknown keys into Extra.
func (r *Response) UnmarshalJSON(data []byte) error {
	type plain Response
	var p plain
	extra, err := jsonx.UnmarshalExtra(data, &p)
	if err != nil {
		return err
	}
	*r = Response(p)
	r.Extra = extra
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
// Tools and Metadata containers. Items are shared.
func (r *Response) Clone() *Response {
	cp := *r
	if r.Output != nil {
		cp.Output = append(Items(nil), r.Output...)
	}
	if r.Tools != nil {
		cp.Tools = append(Tools(nil), r.Tools...)
	}
	cp.Metadata = cloneMap(r.Metadata)
	if r.Extra != nil {
		cp.Extra = make(map[string]any, len(r.Extra))
		for k, v := range r.Extra {
			cp.Extra[k] = v
		}
	}
	return &cp
}

// NewResponse builds an in_progress response that echoes the request's
// settings, as a server adapter would before generating output. The ID is
// the caller's to set; CreatedAt is now. Tools, tool choice, reasoning
// and metadata are copied, so filling in defaults never alters req.
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
		Reasoning:          &ReasoningConfig{},
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
	if req.Reasoning != nil {
		*resp.Reasoning = *req.Reasoning
	}
	return resp
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
		out.Function = &fn
	}
	if tc.Allowed != nil {
		allowed := *tc.Allowed
		allowed.Tools = append([]ToolReference(nil), tc.Allowed.Tools...)
		out.Allowed = &allowed
	}
	return out
}

func cloneTextConfig(tc TextConfig) TextConfig {
	out := TextConfig{Verbosity: tc.Verbosity}
	if tc.Format != nil {
		format := *tc.Format
		if format.Schema != nil {
			format.Schema = append(json.RawMessage(nil), tc.Format.Schema...)
		}
		format.Strict = cloneptr(tc.Format.Strict)
		out.Format = &format
	}
	return out
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
}

// Usage is the token accounting for a response.
type Usage struct {
	InputTokens         int                 `json:"input_tokens"`
	OutputTokens        int                 `json:"output_tokens"`
	TotalTokens         int                 `json:"total_tokens"`
	InputTokensDetails  InputTokensDetails  `json:"input_tokens_details"`
	OutputTokensDetails OutputTokensDetails `json:"output_tokens_details"`
}

// InputTokensDetails breaks down input tokens.
type InputTokensDetails struct {
	CachedTokens int `json:"cached_tokens"`
}

// OutputTokensDetails breaks down output tokens.
type OutputTokensDetails struct {
	ReasoningTokens int `json:"reasoning_tokens"`
}

// CompactResponse is the resource returned by POST /responses/compact.
type CompactResponse struct {
	ID        string `json:"id"`
	Object    string `json:"object"`
	Output    Items  `json:"output"`
	CreatedAt int64  `json:"created_at"`
	Usage     Usage  `json:"usage"`

	// Extra holds top-level keys not defined by the spec.
	Extra map[string]any `json:"-"`
}

// MarshalJSON emits the resource with object fixed to
// "response.compaction" and a non-null output array.
func (r CompactResponse) MarshalJSON() ([]byte, error) {
	type plain CompactResponse
	cp := plain(r)
	cp.Object = ObjectCompaction
	if cp.Output == nil {
		cp.Output = Items{}
	}
	return jsonx.MarshalWithExtra(cp, r.Extra)
}

// UnmarshalJSON captures unknown keys into Extra.
func (r *CompactResponse) UnmarshalJSON(data []byte) error {
	type plain CompactResponse
	var p plain
	extra, err := jsonx.UnmarshalExtra(data, &p)
	if err != nil {
		return err
	}
	*r = CompactResponse(p)
	r.Extra = extra
	return nil
}

var _ json.Marshaler = Response{}
