package openresponses

import (
	"fmt"

	"github.com/ChristopherDavenport/openresponses/internal/jsonx"
)

// Request is the body of POST /responses (CreateResponseBody in the
// OpenAPI document). Optional fields are omitted when empty; pointer
// fields distinguish "unset" from a zero value.
type Request struct {
	Model              string            `json:"model,omitempty"`
	Input              Items             `json:"input,omitempty"`
	PreviousResponseID string            `json:"previous_response_id,omitempty"`
	Include            []Include         `json:"include,omitempty"`
	Tools              Tools             `json:"tools,omitempty"`
	ToolChoice         ToolChoice        `json:"tool_choice,omitzero"`
	Metadata           map[string]string `json:"metadata,omitempty"`
	Text               TextConfig        `json:"text,omitzero"`
	Temperature        *float64          `json:"temperature,omitempty"`
	TopP               *float64          `json:"top_p,omitempty"`
	PresencePenalty    *float64          `json:"presence_penalty,omitempty"`
	FrequencyPenalty   *float64          `json:"frequency_penalty,omitempty"`
	ParallelToolCalls  *bool             `json:"parallel_tool_calls,omitempty"`
	Stream             bool              `json:"stream,omitempty"`
	StreamOptions      *StreamOptions    `json:"stream_options,omitempty"`
	Background         bool              `json:"background,omitempty"`
	MaxOutputTokens    *int              `json:"max_output_tokens,omitempty"`
	MaxToolCalls       *int              `json:"max_tool_calls,omitempty"`
	Reasoning          *ReasoningConfig  `json:"reasoning,omitempty"`
	SafetyIdentifier   string            `json:"safety_identifier,omitempty"`
	PromptCacheKey     string            `json:"prompt_cache_key,omitempty"`
	Truncation         Truncation        `json:"truncation,omitempty"`
	Instructions       string            `json:"instructions,omitempty"`
	Store              *bool             `json:"store,omitempty"`
	ServiceTier        ServiceTier       `json:"service_tier,omitempty"`
	TopLogprobs        *int              `json:"top_logprobs,omitempty"`

	// Extra holds top-level keys not defined by the spec. They are
	// captured on decode and flattened into the object on encode, so a
	// proxy can pass provider-specific parameters through.
	Extra map[string]any `json:"-"`
}

// MarshalJSON flattens Extra into the object.
func (r Request) MarshalJSON() ([]byte, error) {
	type plain Request
	return jsonx.MarshalWithExtra(plain(r), r.Extra)
}

// UnmarshalJSON captures unknown keys into Extra.
func (r *Request) UnmarshalJSON(data []byte) error {
	type plain Request
	var p plain
	extra, err := jsonx.UnmarshalExtra(data, &p)
	if err != nil {
		return err
	}
	*r = Request(p)
	r.Extra = extra
	return nil
}

// Stored reports the effective value of store, which defaults to true.
func (r Request) Stored() bool {
	return r.Store == nil || *r.Store
}

// Validate checks the request against the rules of the specification and
// returns an invalid_request *Error naming the offending field.
func (r Request) Validate() error {
	if r.Model == "" {
		return InvalidRequest(CodeMissingRequiredParameter, "model is required", "model")
	}
	if err := validateItems(r.Input); err != nil {
		return err
	}
	for i, tool := range r.Tools {
		if ft, ok := tool.(*FunctionTool); ok && ft.Name == "" {
			return InvalidRequest(CodeMissingRequiredParameter, "function tool name is required", fmt.Sprintf("tools[%d].name", i))
		}
	}
	if r.ToolChoice.Function != nil && r.ToolChoice.Function.Name == "" {
		return InvalidRequest(CodeMissingRequiredParameter, "tool_choice function name is required", "tool_choice.name")
	}
	if r.Text.Format != nil && r.Text.Format.Type == TextFormatJSONSchema && r.Text.Format.Name == "" {
		return InvalidRequest(CodeMissingRequiredParameter, "json_schema format requires a name", "text.format.name")
	}
	if len(r.Metadata) > 16 {
		return InvalidRequest(CodeInvalidValue, "metadata may have at most 16 keys", "metadata")
	}
	return nil
}

// validateItems checks the structural rules for input items.
func validateItems(items []Item) error {
	for i, item := range items {
		param := fmt.Sprintf("input[%d]", i)
		switch v := item.(type) {
		case *Message:
			if err := validateMessage(v, param); err != nil {
				return err
			}
		case *FunctionCall:
			if v.CallID == "" {
				return InvalidRequest(CodeMissingRequiredParameter, "function_call requires call_id", param+".call_id")
			}
			if v.Name == "" {
				return InvalidRequest(CodeMissingRequiredParameter, "function_call requires name", param+".name")
			}
		case *FunctionCallOutput:
			if v.CallID == "" {
				return InvalidRequest(CodeMissingRequiredParameter, "function_call_output requires call_id", param+".call_id")
			}
		case *Compaction:
			if v.EncryptedContent == "" {
				return InvalidRequest(CodeMissingRequiredParameter, "compaction requires encrypted_content", param+".encrypted_content")
			}
		case *ItemReference:
			if v.ID == "" {
				return InvalidRequest(CodeMissingRequiredParameter, "item_reference requires id", param+".id")
			}
		case nil:
			return InvalidRequest(CodeInvalidValue, "input item is null", param)
		}
	}
	return nil
}

func validateMessage(m *Message, param string) error {
	switch m.Role {
	case RoleUser, RoleSystem, RoleDeveloper:
		for j, part := range m.Content {
			switch part.(type) {
			case *InputText, *InputImage, *InputFile, *InputVideo, *UnknownContent:
			default:
				return InvalidRequest(CodeInvalidValue,
					fmt.Sprintf("%s messages cannot contain %s content", m.Role, part.ContentType()),
					fmt.Sprintf("%s.content[%d]", param, j))
			}
		}
	case RoleAssistant:
		for j, part := range m.Content {
			switch part.(type) {
			case *OutputText, *Refusal, *UnknownContent:
			default:
				return InvalidRequest(CodeInvalidValue,
					fmt.Sprintf("assistant messages cannot contain %s content", part.ContentType()),
					fmt.Sprintf("%s.content[%d]", param, j))
			}
		}
		switch m.Phase {
		case "", PhaseCommentary, PhaseFinalAnswer:
		default:
			return InvalidRequest(CodeInvalidValue, fmt.Sprintf("unknown phase %q", m.Phase), param+".phase")
		}
	case "":
		return InvalidRequest(CodeMissingRequiredParameter, "message requires role", param+".role")
	default:
		return InvalidRequest(CodeInvalidValue, fmt.Sprintf("unknown role %q", m.Role), param+".role")
	}
	return nil
}

// CompactRequest is the body of POST /responses/compact.
type CompactRequest struct {
	Model              string `json:"model,omitempty"`
	Input              Items  `json:"input,omitempty"`
	PreviousResponseID string `json:"previous_response_id,omitempty"`
	Instructions       string `json:"instructions,omitempty"`
	PromptCacheKey     string `json:"prompt_cache_key,omitempty"`

	// Extra holds top-level keys not defined by the spec.
	Extra map[string]any `json:"-"`
}

// MarshalJSON flattens Extra into the object.
func (r CompactRequest) MarshalJSON() ([]byte, error) {
	type plain CompactRequest
	return jsonx.MarshalWithExtra(plain(r), r.Extra)
}

// UnmarshalJSON captures unknown keys into Extra.
func (r *CompactRequest) UnmarshalJSON(data []byte) error {
	type plain CompactRequest
	var p plain
	extra, err := jsonx.UnmarshalExtra(data, &p)
	if err != nil {
		return err
	}
	*r = CompactRequest(p)
	r.Extra = extra
	return nil
}

// Validate checks the compaction request.
func (r CompactRequest) Validate() error {
	if r.Model == "" {
		return InvalidRequest(CodeMissingRequiredParameter, "model is required", "model")
	}
	return validateItems(r.Input)
}
