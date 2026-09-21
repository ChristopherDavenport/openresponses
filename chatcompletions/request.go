package chatcompletions

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ChristopherDavenport/openresponses"
)

// body is the Chat Completions request. Only the fields the mapping sets
// are typed; provider extras ride in extra.
type body struct {
	Model             string            `json:"model"`
	Messages          []message         `json:"messages"`
	Tools             []tool            `json:"tools,omitempty"`
	ToolChoice        any               `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool             `json:"parallel_tool_calls,omitempty"`
	Temperature       *float64          `json:"temperature,omitempty"`
	TopP              *float64          `json:"top_p,omitempty"`
	PresencePenalty   *float64          `json:"presence_penalty,omitempty"`
	FrequencyPenalty  *float64          `json:"frequency_penalty,omitempty"`
	Logprobs          bool              `json:"logprobs,omitempty"`
	TopLogprobs       *int              `json:"top_logprobs,omitempty"`
	ReasoningEffort   string            `json:"reasoning_effort,omitempty"`
	ResponseFormat    *responseFormat   `json:"response_format,omitempty"`
	Verbosity         string            `json:"verbosity,omitempty"`
	ServiceTier       string            `json:"service_tier,omitempty"`
	SafetyIdentifier  string            `json:"safety_identifier,omitempty"`
	PromptCacheKey    string            `json:"prompt_cache_key,omitempty"`
	Metadata          map[string]string `json:"metadata,omitempty"`
	N                 int               `json:"n"`
	Stream            bool              `json:"stream"`
	StreamOptions     map[string]any    `json:"stream_options"`
	maxTokens         *int              // emitted under the configured name
	maxTokensField    string            //
	extra             map[string]any    // provider parameters, never overriding the above
}

// MarshalJSON adds the max tokens field under its configured name and
// the extras under theirs.
func (b body) MarshalJSON() ([]byte, error) {
	type plain body
	data, err := json.Marshal(plain(b))
	if err != nil {
		return nil, err
	}
	if b.maxTokens == nil && len(b.extra) == 0 {
		return data, nil
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, err
	}
	for k, v := range b.extra {
		if _, taken := obj[k]; taken {
			continue
		}
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("extra %q: %w", k, err)
		}
		obj[k] = raw
	}
	if b.maxTokens != nil {
		obj[b.maxTokensField], _ = json.Marshal(*b.maxTokens)
	}
	return json.Marshal(obj)
}

type message struct {
	Role           string     `json:"role"`
	Content        any        `json:"content,omitempty"` // string or []part
	Refusal        string     `json:"refusal,omitempty"`
	ToolCalls      []toolCall `json:"tool_calls,omitempty"`
	ToolCallID     string     `json:"tool_call_id,omitempty"`
	reasoning      string     // sent under the replay field, when configured
	reasoningField string
}

// MarshalJSON adds the reasoning replay field when there is one.
func (m message) MarshalJSON() ([]byte, error) {
	type plain message
	data, err := json.Marshal(plain(m))
	if err != nil || m.reasoningField == "" || m.reasoning == "" {
		return data, err
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		return nil, err
	}
	obj[m.reasoningField], _ = json.Marshal(m.reasoning)
	return json.Marshal(obj)
}

type part struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *imageURL `json:"image_url,omitempty"`
	File     *file     `json:"file,omitempty"`
}

type imageURL struct {
	URL    string `json:"url"`
	Detail string `json:"detail,omitempty"`
}

type file struct {
	FileID   string `json:"file_id,omitempty"`
	FileData string `json:"file_data,omitempty"`
	Filename string `json:"filename,omitempty"`
}

type toolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function functionCall `json:"function"`
}

type functionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type tool struct {
	Type     string   `json:"type"`
	Function function `json:"function"`
}

type function struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Parameters  json.RawMessage `json:"parameters,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

type responseFormat struct {
	Type       string      `json:"type"`
	JSONSchema *jsonSchema `json:"json_schema,omitempty"`
}

type jsonSchema struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Schema      json.RawMessage `json:"schema,omitempty"`
	Strict      *bool           `json:"strict,omitempty"`
}

// encodeRequest translates a request into a Chat Completions body.
// Fields the protocol has no equivalent for are rejected with
// invalid_request naming the field.
func (a *Adapter) encodeRequest(req openresponses.Request) (*body, error) {
	if req.PreviousResponseID != "" {
		// The handler resolves continuation when it has a store. Reaching
		// here means it has none, and the endpoint keeps no state.
		return nil, openresponses.PreviousResponseNotFound(req.PreviousResponseID)
	}
	b := &body{
		Model:          req.Model,
		N:              1,
		Stream:         true,
		StreamOptions:  map[string]any{"include_usage": true},
		maxTokensField: a.maxTokensField,
	}
	switch {
	case req.MaxToolCalls != nil:
		return nil, unsupported("max_tool_calls")
	case req.Truncation == openresponses.TruncationAuto:
		return nil, unsupported("truncation")
	}
	b.maxTokens = req.MaxOutputTokens
	b.Temperature = req.Temperature
	b.TopP = req.TopP
	b.PresencePenalty = req.PresencePenalty
	b.FrequencyPenalty = req.FrequencyPenalty
	b.ParallelToolCalls = req.ParallelToolCalls
	if req.TopLogprobs != nil {
		b.Logprobs = true
		b.TopLogprobs = req.TopLogprobs
	}
	// reasoning.summary has no field in this protocol and no effect.
	b.ReasoningEffort = string(req.Reasoning.Effort)
	b.Verbosity = string(req.Text.Verbosity)
	b.ServiceTier = string(req.ServiceTier)
	b.SafetyIdentifier = req.SafetyIdentifier
	b.PromptCacheKey = req.PromptCacheKey
	b.Metadata = req.Metadata

	if f := req.Text.Format; f != nil {
		switch f.Type {
		case "", openresponses.TextFormatText:
		case openresponses.TextFormatJSONObject:
			b.ResponseFormat = &responseFormat{Type: "json_object"}
		case openresponses.TextFormatJSONSchema:
			if len(f.Schema) > 0 && !json.Valid(f.Schema) {
				return nil, invalid("text.format.schema", "schema is not valid JSON")
			}
			b.ResponseFormat = &responseFormat{Type: "json_schema", JSONSchema: &jsonSchema{
				Name: f.Name, Description: f.Description, Schema: f.Schema, Strict: f.Strict,
			}}
		default:
			return nil, invalid("text.format.type", fmt.Sprintf("unknown text format %q", f.Type))
		}
	}

	for i, t := range req.Tools {
		fn, ok := t.(*openresponses.FunctionTool)
		if !ok {
			return nil, unsupported(fmt.Sprintf("tools[%d].type", i))
		}
		if len(fn.Parameters) > 0 && !json.Valid(fn.Parameters) {
			return nil, invalid(fmt.Sprintf("tools[%d].parameters", i), "parameters is not valid JSON")
		}
		params := fn.Parameters
		if string(params) == "null" {
			params = nil
		}
		b.Tools = append(b.Tools, tool{Type: "function", Function: function{Name: fn.Name, Description: fn.Description, Parameters: params, Strict: fn.Strict}})
	}
	b.ToolChoice = encodeToolChoice(req.ToolChoice)

	messages, err := a.encodeInput(req)
	if err != nil {
		return nil, err
	}
	b.Messages = messages
	if a.forwardExtra {
		b.extra = req.Extra
	}
	return b, nil
}

func unsupported(param string) *openresponses.Error {
	return openresponses.InvalidRequest(openresponses.CodeUnsupportedParameter,
		fmt.Sprintf("%s is not supported by the Chat Completions protocol", param), param)
}

func invalid(param, message string) *openresponses.Error {
	return openresponses.InvalidRequest(openresponses.CodeInvalidValue, message, param)
}

// encodeToolChoice maps the choice onto the protocol's own forms, which
// include allowed_tools.
func encodeToolChoice(c openresponses.ToolChoice) any {
	switch {
	case c.Function != nil:
		return map[string]any{"type": "function", "function": map[string]string{"name": c.Function.Name}}
	case c.Allowed != nil:
		tools := make([]map[string]any, 0, len(c.Allowed.Tools))
		for _, ref := range c.Allowed.Tools {
			t := map[string]any{"type": ref.Type}
			if ref.Name != "" {
				t["function"] = map[string]string{"name": ref.Name}
			}
			tools = append(tools, t)
		}
		return map[string]any{"type": "allowed_tools", "allowed_tools": map[string]any{"mode": c.Allowed.Mode, "tools": tools}}
	case c.Mode != "":
		return string(c.Mode)
	}
	return nil
}

// encodeInput turns the items into messages. Instructions lead as a
// system message; system and developer messages keep their roles; the
// assistant's consecutive items (text, function calls, reasoning) fold
// into one assistant message, since a call is a field of the message
// that made it; each function output is its own tool message.
func (a *Adapter) encodeInput(req openresponses.Request) ([]message, error) {
	var messages []message
	if req.Instructions != "" {
		messages = append(messages, message{Role: "system", Content: req.Instructions})
	}
	assistant := func() *message {
		if n := len(messages); n > 0 && messages[n-1].Role == "assistant" {
			return &messages[n-1]
		}
		messages = append(messages, message{Role: "assistant", reasoningField: a.reasoningReplay})
		return &messages[len(messages)-1]
	}
	for i, item := range req.Input {
		p := fmt.Sprintf("input[%d]", i)
		switch it := item.(type) {
		case *openresponses.Message:
			switch it.Role {
			case openresponses.RoleUser:
				content, err := encodeParts(it.Content, p+".content")
				if err != nil {
					return nil, err
				}
				messages = append(messages, message{Role: "user", Content: content})
			case openresponses.RoleSystem, openresponses.RoleDeveloper:
				text, err := textOnly(it.Content, p+".content")
				if err != nil {
					return nil, err
				}
				messages = append(messages, message{Role: string(it.Role), Content: text})
			case openresponses.RoleAssistant:
				m := assistant()
				for j, c := range it.Content {
					switch c := c.(type) {
					case *openresponses.OutputText:
						appendText(m, c.Text)
					case *openresponses.Text:
						appendText(m, c.Text)
					case *openresponses.Refusal:
						m.Refusal += c.Refusal
					default:
						return nil, unsupported(fmt.Sprintf("%s.content[%d].type", p, j))
					}
				}
			default:
				return nil, invalid(p+".role", fmt.Sprintf("unknown role %q", it.Role))
			}
		case *openresponses.FunctionCall:
			args := it.Arguments
			if strings.TrimSpace(args) == "" {
				args = "{}"
			}
			m := assistant()
			m.ToolCalls = append(m.ToolCalls, toolCall{ID: it.CallID, Type: "function", Function: functionCall{Name: it.Name, Arguments: args}})
		case *openresponses.FunctionCallOutput:
			var content any = it.Output.Text
			if it.Output.Parts != nil {
				text, err := textOnly(it.Output.Parts, p+".output")
				if err != nil {
					return nil, err
				}
				content = text
			}
			messages = append(messages, message{Role: "tool", ToolCallID: it.CallID, Content: content})
		case *openresponses.ReasoningItem:
			// Only the field WithReasoningReplay names carries this; the
			// protocol has no standard one, and the item is accepted either
			// way.
			text := it.Content.Text()
			if text == "" {
				text = it.Summary.Text()
			}
			m := assistant()
			m.reasoning += text
		default:
			return nil, unsupported(p + ".type")
		}
	}
	return messages, nil
}

// appendText adds text to an assistant message: a string while there is
// one piece, an array of text parts once there are more.
func appendText(m *message, text string) {
	switch c := m.Content.(type) {
	case nil:
		m.Content = text
	case string:
		m.Content = []part{{Type: "text", Text: c}, {Type: "text", Text: text}}
	case []part:
		m.Content = append(c, part{Type: "text", Text: text})
	}
}

// textOnly concatenates textual parts and rejects anything else, for the
// roles whose content is text.
func textOnly(contents openresponses.Contents, p string) (string, error) {
	var out strings.Builder
	for j, c := range contents {
		switch c := c.(type) {
		case *openresponses.InputText:
			out.WriteString(c.Text)
		case *openresponses.Text:
			out.WriteString(c.Text)
		case *openresponses.OutputText:
			out.WriteString(c.Text)
		default:
			return "", unsupported(fmt.Sprintf("%s[%d].type", p, j))
		}
	}
	return out.String(), nil
}

// encodeParts maps user content: a bare string when it is one text part,
// otherwise an array of text, image_url and file parts.
func encodeParts(contents openresponses.Contents, p string) (any, error) {
	if len(contents) == 1 {
		if t, ok := contents[0].(*openresponses.InputText); ok {
			return t.Text, nil
		}
	}
	parts := make([]part, 0, len(contents))
	for j, c := range contents {
		pp := fmt.Sprintf("%s[%d]", p, j)
		switch c := c.(type) {
		case *openresponses.InputText:
			parts = append(parts, part{Type: "text", Text: c.Text})
		case *openresponses.InputImage:
			if c.FileID != "" {
				return nil, unsupported(pp + ".file_id")
			}
			if c.ImageURL == "" {
				return nil, openresponses.InvalidRequest(openresponses.CodeMissingRequiredParameter, "input_image needs image_url", pp)
			}
			img := &imageURL{URL: c.ImageURL}
			if c.Detail != "" && c.Detail != openresponses.ImageDetailAuto {
				img.Detail = string(c.Detail)
			}
			parts = append(parts, part{Type: "image_url", ImageURL: img})
		case *openresponses.InputFile:
			switch {
			case c.FileURL != "":
				return nil, unsupported(pp + ".file_url")
			case c.FileID == "" && c.FileData == "":
				return nil, openresponses.InvalidRequest(openresponses.CodeMissingRequiredParameter, "input_file needs file_data or file_id", pp)
			}
			parts = append(parts, part{Type: "file", File: &file{FileID: c.FileID, FileData: c.FileData, Filename: c.Filename}})
		default:
			return nil, unsupported(pp + ".type")
		}
	}
	return parts, nil
}
