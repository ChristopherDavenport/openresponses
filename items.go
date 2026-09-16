package openresponses

import (
	"encoding/json"
	"fmt"

	"github.com/christopherdavenport/openresponses/internal/jsonx"
)

// Item is one entry in a request input or a response output. Concrete
// types are [Message], [FunctionCall], [FunctionCallOutput],
// [ReasoningItem], [Compaction], [ItemReference] and [UnknownItem].
// Decoded values are always pointers, so switch on *Message and so on.
type Item interface {
	ItemType() string
}

// Items is a list of items that decodes through the item registry.
type Items []Item

// UnmarshalJSON decodes each element through [UnmarshalItem].
func (it *Items) UnmarshalJSON(data []byte) error {
	var raws []json.RawMessage
	if err := json.Unmarshal(data, &raws); err != nil {
		return err
	}
	out := make(Items, 0, len(raws))
	for i, raw := range raws {
		item, err := UnmarshalItem(raw)
		if err != nil {
			return fmt.Errorf("items[%d]: %w", i, err)
		}
		out = append(out, item)
	}
	*it = out
	return nil
}

// Message is a message to or from the model. Non-assistant messages carry
// input_* content parts; assistant messages carry output_text and refusal
// parts and may be labelled with a Phase.
type Message struct {
	ID      string   `json:"id,omitempty"`
	Status  Status   `json:"status,omitempty"`
	Role    Role     `json:"role"`
	Content Contents `json:"content"`
	Phase   Phase    `json:"phase,omitempty"`
}

// ItemType returns "message".
func (*Message) ItemType() string { return ItemTypeMessage }

// MarshalJSON emits the message with its type discriminator. Content is
// always emitted as an array.
func (m *Message) MarshalJSON() ([]byte, error) {
	type plain Message
	cp := plain(*m)
	if cp.Content == nil {
		cp.Content = Contents{}
	}
	return jsonx.MarshalTyped(ItemTypeMessage, cp)
}

// UnmarshalJSON accepts content as either an array of parts or a bare
// string. A string becomes one input_text part, or one output_text part
// when the role is assistant.
func (m *Message) UnmarshalJSON(data []byte) error {
	type plain Message
	aux := struct {
		*plain
		Content json.RawMessage `json:"content"`
	}{plain: (*plain)(m)}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	return unmarshalStringOrParts(aux.Content, &m.Content, func(s string) Content {
		if m.Role == RoleAssistant {
			return &OutputText{Text: s}
		}
		return &InputText{Text: s}
	})
}

// Text returns the concatenated text of the message.
func (m *Message) Text() string { return m.Content.Text() }

// UserText builds a user message with one input_text part.
func UserText(text string) *Message {
	return &Message{Role: RoleUser, Content: Contents{&InputText{Text: text}}}
}

// UserMessage builds a user message from content parts.
func UserMessage(parts ...Content) *Message {
	return &Message{Role: RoleUser, Content: Contents(parts)}
}

// SystemText builds a system message with one input_text part.
func SystemText(text string) *Message {
	return &Message{Role: RoleSystem, Content: Contents{&InputText{Text: text}}}
}

// DeveloperText builds a developer message with one input_text part.
func DeveloperText(text string) *Message {
	return &Message{Role: RoleDeveloper, Content: Contents{&InputText{Text: text}}}
}

// AssistantText builds an assistant message with one output_text part.
func AssistantText(text string) *Message {
	return &Message{Role: RoleAssistant, Content: Contents{&OutputText{Text: text}}}
}

// FunctionCall is a request from the model to call a function tool.
// Arguments is a JSON-encoded string.
type FunctionCall struct {
	ID        string `json:"id,omitempty"`
	Status    Status `json:"status,omitempty"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ItemType returns "function_call".
func (*FunctionCall) ItemType() string { return ItemTypeFunctionCall }

// MarshalJSON emits the item with its type discriminator.
func (f *FunctionCall) MarshalJSON() ([]byte, error) {
	type plain FunctionCall
	return jsonx.MarshalTyped(ItemTypeFunctionCall, (*plain)(f))
}

// UnmarshalArguments decodes the JSON arguments into v.
func (f *FunctionCall) UnmarshalArguments(v any) error {
	return json.Unmarshal([]byte(f.Arguments), v)
}

// FunctionCallOutput carries the result of a function call back to the
// model.
type FunctionCallOutput struct {
	ID     string                 `json:"id,omitempty"`
	Status Status                 `json:"status,omitempty"`
	CallID string                 `json:"call_id"`
	Output FunctionCallOutputData `json:"output"`
}

// ItemType returns "function_call_output".
func (*FunctionCallOutput) ItemType() string { return ItemTypeFunctionCallOutput }

// MarshalJSON emits the item with its type discriminator.
func (f *FunctionCallOutput) MarshalJSON() ([]byte, error) {
	type plain FunctionCallOutput
	return jsonx.MarshalTyped(ItemTypeFunctionCallOutput, (*plain)(f))
}

// NewFunctionCallOutput builds a function_call_output with a string
// result.
func NewFunctionCallOutput(callID, output string) *FunctionCallOutput {
	return &FunctionCallOutput{CallID: callID, Output: FunctionCallOutputData{Text: output}}
}

// FunctionCallOutputData is the output of a function call: either a
// string or a list of content parts. When Parts is non-nil it is emitted;
// otherwise Text is emitted as a JSON string.
type FunctionCallOutputData struct {
	Text  string
	Parts Contents
}

// MarshalJSON emits Parts when set, otherwise Text.
func (d FunctionCallOutputData) MarshalJSON() ([]byte, error) {
	if d.Parts != nil {
		return json.Marshal(d.Parts)
	}
	return json.Marshal(d.Text)
}

// UnmarshalJSON accepts a string or an array of parts.
func (d *FunctionCallOutputData) UnmarshalJSON(data []byte) error {
	*d = FunctionCallOutputData{}
	if len(data) > 0 && data[0] == '"' {
		return json.Unmarshal(data, &d.Text)
	}
	return json.Unmarshal(data, &d.Parts)
}

// String returns the text form of the output, concatenating textual parts.
func (d FunctionCallOutputData) String() string {
	if d.Parts != nil {
		return d.Parts.Text()
	}
	return d.Text
}

// ReasoningItem is the model's reasoning. Summary is required by the spec
// and is always emitted; Content and EncryptedContent are optional.
type ReasoningItem struct {
	ID               string   `json:"id,omitempty"`
	Status           Status   `json:"status,omitempty"`
	Summary          Contents `json:"summary"`
	Content          Contents `json:"content,omitempty"`
	EncryptedContent string   `json:"encrypted_content,omitempty"`
}

// ItemType returns "reasoning".
func (*ReasoningItem) ItemType() string { return ItemTypeReasoning }

// MarshalJSON emits the item with its type discriminator. Summary is
// always emitted as an array.
func (r *ReasoningItem) MarshalJSON() ([]byte, error) {
	type plain ReasoningItem
	cp := plain(*r)
	if cp.Summary == nil {
		cp.Summary = Contents{}
	}
	return jsonx.MarshalTyped(ItemTypeReasoning, cp)
}

// Compaction is an opaque, provider-encrypted summary of earlier
// conversation produced by the compaction endpoint.
type Compaction struct {
	ID               string `json:"id,omitempty"`
	Status           Status `json:"status,omitempty"`
	EncryptedContent string `json:"encrypted_content"`
	CreatedBy        string `json:"created_by,omitempty"`
}

// ItemType returns "compaction".
func (*Compaction) ItemType() string { return ItemTypeCompaction }

// MarshalJSON emits the item with its type discriminator.
func (c *Compaction) MarshalJSON() ([]byte, error) {
	type plain Compaction
	return jsonx.MarshalTyped(ItemTypeCompaction, (*plain)(c))
}

// ItemReference references an item from a previous response by ID. It is
// input-only.
type ItemReference struct {
	ID string `json:"id"`
}

// ItemType returns "item_reference".
func (*ItemReference) ItemType() string { return ItemTypeItemReference }

// MarshalJSON emits the item with its type discriminator.
func (r *ItemReference) MarshalJSON() ([]byte, error) {
	type plain ItemReference
	return jsonx.MarshalTyped(ItemTypeItemReference, (*plain)(r))
}

// UnknownItem is an item whose type is not registered, typically a
// slug-prefixed provider extension. Raw holds the original bytes and is
// re-emitted verbatim.
type UnknownItem struct {
	Type   string
	ID     string
	Status Status
	Raw    json.RawMessage
}

// ItemType returns the wire type.
func (u *UnknownItem) ItemType() string { return u.Type }

// MarshalJSON emits the original bytes, or a minimal object when Raw is
// empty.
func (u *UnknownItem) MarshalJSON() ([]byte, error) {
	if len(u.Raw) == 0 {
		return jsonx.MarshalTyped(u.Type, struct {
			ID     string `json:"id,omitempty"`
			Status Status `json:"status,omitempty"`
		}{u.ID, u.Status})
	}
	return u.Raw, nil
}

// UnmarshalJSON records the common fields and the raw bytes.
func (u *UnknownItem) UnmarshalJSON(data []byte) error {
	var head struct {
		Type   string `json:"type"`
		ID     string `json:"id"`
		Status Status `json:"status"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return err
	}
	u.Type = head.Type
	u.ID = head.ID
	u.Status = head.Status
	u.Raw = append(json.RawMessage(nil), data...)
	return nil
}

// unmarshalStringOrParts decodes raw into dst, accepting either a JSON
// string (converted with fromString) or an array of parts. A missing or
// null value leaves dst nil.
func unmarshalStringOrParts(raw json.RawMessage, dst *Contents, fromString func(string) Content) error {
	if len(raw) == 0 || string(raw) == "null" {
		*dst = nil
		return nil
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return err
		}
		*dst = Contents{fromString(s)}
		return nil
	}
	return json.Unmarshal(raw, dst)
}

var (
	_ Item = (*Message)(nil)
	_ Item = (*FunctionCall)(nil)
	_ Item = (*FunctionCallOutput)(nil)
	_ Item = (*ReasoningItem)(nil)
	_ Item = (*Compaction)(nil)
	_ Item = (*ItemReference)(nil)
	_ Item = (*UnknownItem)(nil)
)
