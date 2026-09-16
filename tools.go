package openresponses

import (
	"encoding/json"
	"fmt"

	"github.com/ChristopherDavenport/openresponses/internal/jsonx"
)

// Tool is a tool the model may call. The only standard tool is
// [FunctionTool]; everything else decodes to [UnknownTool].
type Tool interface {
	ToolType() string
}

// Tools is a list of tools that decodes through the tool registry.
type Tools []Tool

// UnmarshalJSON decodes each element through [UnmarshalTool].
func (t *Tools) UnmarshalJSON(data []byte) error {
	var raws []json.RawMessage
	if err := json.Unmarshal(data, &raws); err != nil {
		return err
	}
	out := make(Tools, 0, len(raws))
	for i, raw := range raws {
		tool, err := UnmarshalTool(raw)
		if err != nil {
			return fmt.Errorf("tools[%d]: %w", i, err)
		}
		out = append(out, tool)
	}
	*t = out
	return nil
}

// FunctionTool describes a function the model may call. Parameters is a
// JSON Schema object.
type FunctionTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
	Strict      *bool           `json:"strict,omitempty"`
}

// ToolType returns "function".
func (*FunctionTool) ToolType() string { return ToolTypeFunction }

// MarshalJSON emits the tool with its type discriminator. Parameters is
// emitted as null when unset because the resource form requires the key.
func (f *FunctionTool) MarshalJSON() ([]byte, error) {
	type plain FunctionTool
	cp := plain(*f)
	if len(cp.Parameters) == 0 {
		cp.Parameters = json.RawMessage("null")
	}
	return jsonx.MarshalTyped(ToolTypeFunction, cp)
}

// NewFunctionTool builds a function tool. parameters must be a JSON
// Schema object encoded as JSON, or nil.
func NewFunctionTool(name, description string, parameters json.RawMessage) *FunctionTool {
	return &FunctionTool{Name: name, Description: description, Parameters: parameters}
}

// UnknownTool is a tool whose type is not registered.
type UnknownTool struct {
	Type string
	Raw  json.RawMessage
}

// ToolType returns the wire type.
func (u *UnknownTool) ToolType() string { return u.Type }

// MarshalJSON emits the original bytes.
func (u *UnknownTool) MarshalJSON() ([]byte, error) {
	if len(u.Raw) == 0 {
		return jsonx.MarshalTyped(u.Type, struct{}{})
	}
	return u.Raw, nil
}

// UnmarshalJSON records the type and the raw bytes.
func (u *UnknownTool) UnmarshalJSON(data []byte) error {
	typ, err := jsonx.PeekType(data)
	if err != nil {
		return err
	}
	u.Type = typ
	u.Raw = append(json.RawMessage(nil), data...)
	return nil
}

// ToolChoice controls which tools the model may call. Exactly one of Mode,
// Function or Allowed is set; the zero value means "not specified" and is
// omitted from requests.
type ToolChoice struct {
	Mode     ToolChoiceMode
	Function *FunctionToolChoice
	Allowed  *AllowedTools
}

// FunctionToolChoice forces a call to the named function.
type FunctionToolChoice struct {
	Name string `json:"name"`
}

// AllowedTools restricts the model to a subset of the configured tools.
// Mode is "auto" or "required".
type AllowedTools struct {
	Tools []ToolReference `json:"tools"`
	Mode  ToolChoiceMode  `json:"mode"`
}

// ToolReference names a tool inside an allowed_tools choice.
type ToolReference struct {
	Type string `json:"type"`
	Name string `json:"name,omitempty"`
}

// FunctionReference builds a reference to the named function tool.
func FunctionReference(name string) ToolReference {
	return ToolReference{Type: ToolTypeFunction, Name: name}
}

// ToolChoiceFunction builds a tool choice that forces the named function.
func ToolChoiceFunction(name string) ToolChoice {
	return ToolChoice{Function: &FunctionToolChoice{Name: name}}
}

// IsZero reports whether no choice is set.
func (c ToolChoice) IsZero() bool {
	return c.Mode == "" && c.Function == nil && c.Allowed == nil
}

// MarshalJSON emits the string, function or allowed_tools form.
func (c ToolChoice) MarshalJSON() ([]byte, error) {
	switch {
	case c.Function != nil:
		return jsonx.MarshalTyped(ToolTypeFunction, c.Function)
	case c.Allowed != nil:
		return jsonx.MarshalTyped("allowed_tools", c.Allowed)
	case c.Mode != "":
		return json.Marshal(c.Mode)
	default:
		return []byte("null"), nil
	}
}

// UnmarshalJSON accepts a string or an object form.
func (c *ToolChoice) UnmarshalJSON(data []byte) error {
	*c = ToolChoice{}
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	if data[0] == '"' {
		return json.Unmarshal(data, &c.Mode)
	}
	typ, err := jsonx.PeekType(data)
	if err != nil {
		return err
	}
	switch typ {
	case ToolTypeFunction:
		c.Function = &FunctionToolChoice{}
		return json.Unmarshal(data, c.Function)
	case "allowed_tools":
		c.Allowed = &AllowedTools{}
		return json.Unmarshal(data, c.Allowed)
	default:
		return fmt.Errorf("tool_choice: unknown type %q", typ)
	}
}

// TextFormat selects the output format: "text", "json_object" or
// "json_schema". Name, Description, Schema and Strict apply to
// json_schema only.
type TextFormat struct {
	Type        string          `json:"type"`
	Name        string          `json:"name,omitempty"`
	Description string          `json:"description,omitempty"`
	Schema      json.RawMessage `json:"schema,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

// JSONSchemaFormat builds a json_schema text format.
func JSONSchemaFormat(name string, schema json.RawMessage, strict bool) *TextFormat {
	return &TextFormat{Type: TextFormatJSONSchema, Name: name, Schema: schema, Strict: &strict}
}

// TextConfig is the "text" field of a request or response. A nil Format
// on a response is emitted as {"type":"text"}.
type TextConfig struct {
	Format    *TextFormat `json:"format,omitempty"`
	Verbosity Verbosity   `json:"verbosity,omitempty"`
}

// IsZero reports whether no text options are set.
func (t TextConfig) IsZero() bool { return t.Format == nil && t.Verbosity == "" }

// MarshalJSON always emits format, defaulting to {"type":"text"}.
func (t TextConfig) MarshalJSON() ([]byte, error) {
	type plain TextConfig
	cp := plain(t)
	if cp.Format == nil {
		cp.Format = &TextFormat{Type: TextFormatText}
	}
	return json.Marshal(cp)
}

// ReasoningConfig is the "reasoning" field of a request or response. The
// zero value means "not configured": it is omitted from requests and
// emitted as {"effort":null,"summary":null} on responses, and a null on
// the wire decodes to it.
type ReasoningConfig struct {
	Effort  ReasoningEffort
	Summary ReasoningSummary
}

// IsZero reports whether neither effort nor summary is set.
func (r ReasoningConfig) IsZero() bool { return r.Effort == "" && r.Summary == "" }

// MarshalJSON emits effort and summary, using null for empty values.
func (r ReasoningConfig) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Effort  *ReasoningEffort  `json:"effort"`
		Summary *ReasoningSummary `json:"summary"`
	}{nilIfEmpty(r.Effort), nilIfEmpty(r.Summary)})
}

// UnmarshalJSON decodes effort and summary, treating null as empty.
func (r *ReasoningConfig) UnmarshalJSON(data []byte) error {
	*r = ReasoningConfig{}
	if len(data) == 0 || string(data) == "null" {
		return nil
	}
	var aux struct {
		Effort  *ReasoningEffort  `json:"effort"`
		Summary *ReasoningSummary `json:"summary"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if aux.Effort != nil {
		r.Effort = *aux.Effort
	}
	if aux.Summary != nil {
		r.Summary = *aux.Summary
	}
	return nil
}

// StreamOptions configures streaming.
type StreamOptions struct {
	IncludeObfuscation bool `json:"include_obfuscation,omitempty"`
}

func nilIfEmpty[T ~string](s T) *T {
	if s == "" {
		return nil
	}
	return &s
}

var (
	_ Tool = (*FunctionTool)(nil)
	_ Tool = (*UnknownTool)(nil)
)
