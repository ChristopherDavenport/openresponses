package openresponses

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/ChristopherDavenport/openresponses/internal/jsonx"
)

// registry maps a wire "type" discriminator to a decoder for T. It is
// safe for concurrent use.
type registry[T any] struct {
	mu       sync.RWMutex
	decoders map[string]func(json.RawMessage) (T, error)
}

func (r *registry[T]) register(typ string, decode func(json.RawMessage) (T, error)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.decoders[typ] = decode
}

func (r *registry[T]) lookup(typ string) (func(json.RawMessage) (T, error), bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	decode, ok := r.decoders[typ]
	return decode, ok
}

// decodeInto unmarshals raw into a fresh *P and returns it as T. *P must
// implement T; the built-in registries are covered by tests.
func decodeInto[T any, P any](raw json.RawMessage) (T, error) {
	var v P
	if err := json.Unmarshal(raw, &v); err != nil {
		var zero T
		return zero, err
	}
	return any(&v).(T), nil
}

var itemRegistry = &registry[Item]{decoders: map[string]func(json.RawMessage) (Item, error){
	ItemTypeMessage:            decodeInto[Item, Message],
	ItemTypeFunctionCall:       decodeInto[Item, FunctionCall],
	ItemTypeFunctionCallOutput: decodeInto[Item, FunctionCallOutput],
	ItemTypeReasoning:          decodeInto[Item, ReasoningItem],
	ItemTypeCompaction:         decodeInto[Item, Compaction],
	ItemTypeItemReference:      decodeInto[Item, ItemReference],
}}

var contentRegistry = &registry[Content]{decoders: map[string]func(json.RawMessage) (Content, error){
	ContentTypeInputText:     decodeInto[Content, InputText],
	ContentTypeInputImage:    decodeInto[Content, InputImage],
	ContentTypeInputFile:     decodeInto[Content, InputFile],
	ContentTypeInputVideo:    decodeInto[Content, InputVideo],
	ContentTypeOutputText:    decodeInto[Content, OutputText],
	ContentTypeRefusal:       decodeInto[Content, Refusal],
	ContentTypeText:          decodeInto[Content, Text],
	ContentTypeSummaryText:   decodeInto[Content, SummaryText],
	ContentTypeReasoningText: decodeInto[Content, ReasoningText],
}}

var toolRegistry = &registry[Tool]{decoders: map[string]func(json.RawMessage) (Tool, error){
	ToolTypeFunction: decodeInto[Tool, FunctionTool],
}}

var annotationRegistry = &registry[Annotation]{decoders: map[string]func(json.RawMessage) (Annotation, error){
	AnnotationTypeURLCitation: decodeInto[Annotation, URLCitation],
}}

// RegisterItem registers a decoder for an extension item type such as
// "acme:search_result". Registering a built-in type replaces its decoder.
func RegisterItem(typ string, decode func(json.RawMessage) (Item, error)) {
	itemRegistry.register(typ, decode)
}

// RegisterContent registers a decoder for an extension content type.
func RegisterContent(typ string, decode func(json.RawMessage) (Content, error)) {
	contentRegistry.register(typ, decode)
}

// RegisterTool registers a decoder for an extension tool type.
func RegisterTool(typ string, decode func(json.RawMessage) (Tool, error)) {
	toolRegistry.register(typ, decode)
}

// RegisterAnnotation registers a decoder for an extension annotation type.
func RegisterAnnotation(typ string, decode func(json.RawMessage) (Annotation, error)) {
	annotationRegistry.register(typ, decode)
}

// UnmarshalItem decodes one item, dispatching on its "type". Unregistered
// types decode to [*UnknownItem].
func UnmarshalItem(data []byte) (Item, error) {
	typ, err := jsonx.PeekType(data)
	if err != nil {
		return nil, fmt.Errorf("item: %w", err)
	}
	if decode, ok := itemRegistry.lookup(typ); ok {
		item, err := decode(data)
		if err != nil {
			return nil, fmt.Errorf("item %q: %w", typ, err)
		}
		return item, nil
	}
	var u UnknownItem
	if err := json.Unmarshal(data, &u); err != nil {
		return nil, fmt.Errorf("item %q: %w", typ, err)
	}
	return &u, nil
}

// UnmarshalContent decodes one content part, dispatching on its "type".
// Unregistered types decode to [*UnknownContent].
func UnmarshalContent(data []byte) (Content, error) {
	typ, err := jsonx.PeekType(data)
	if err != nil {
		return nil, fmt.Errorf("content: %w", err)
	}
	if decode, ok := contentRegistry.lookup(typ); ok {
		part, err := decode(data)
		if err != nil {
			return nil, fmt.Errorf("content %q: %w", typ, err)
		}
		return part, nil
	}
	var u UnknownContent
	if err := json.Unmarshal(data, &u); err != nil {
		return nil, fmt.Errorf("content %q: %w", typ, err)
	}
	return &u, nil
}

// UnmarshalTool decodes one tool, dispatching on its "type". Unregistered
// types decode to [*UnknownTool].
func UnmarshalTool(data []byte) (Tool, error) {
	typ, err := jsonx.PeekType(data)
	if err != nil {
		return nil, fmt.Errorf("tool: %w", err)
	}
	if decode, ok := toolRegistry.lookup(typ); ok {
		tool, err := decode(data)
		if err != nil {
			return nil, fmt.Errorf("tool %q: %w", typ, err)
		}
		return tool, nil
	}
	var u UnknownTool
	if err := json.Unmarshal(data, &u); err != nil {
		return nil, fmt.Errorf("tool %q: %w", typ, err)
	}
	return &u, nil
}

// UnmarshalAnnotation decodes one annotation, dispatching on its "type".
// Unregistered types decode to [*UnknownAnnotation].
func UnmarshalAnnotation(data []byte) (Annotation, error) {
	typ, err := jsonx.PeekType(data)
	if err != nil {
		return nil, fmt.Errorf("annotation: %w", err)
	}
	if decode, ok := annotationRegistry.lookup(typ); ok {
		ann, err := decode(data)
		if err != nil {
			return nil, fmt.Errorf("annotation %q: %w", typ, err)
		}
		return ann, nil
	}
	var u UnknownAnnotation
	if err := json.Unmarshal(data, &u); err != nil {
		return nil, fmt.Errorf("annotation %q: %w", typ, err)
	}
	return &u, nil
}

// ItemID returns the item's own id member and [CallID] its call_id
// member, reading extension items from their raw JSON; an
// item_reference has no id of its own. ItemID returns "" for an
// [ItemReference], whose id names another item, and for an item without
// an id.
func ItemID(item Item) string {
	switch v := item.(type) {
	case nil, *ItemReference:
		return ""
	case *Message:
		return v.ID
	case *FunctionCall:
		return v.ID
	case *FunctionCallOutput:
		return v.ID
	case *ReasoningItem:
		return v.ID
	case *Compaction:
		return v.ID
	case *UnknownItem:
		return v.ID
	}
	return encodedMember(item, "id")
}

// CallID returns the item's call_id member, which pairs a tool call with
// its output, or "" when the item has none. An [UnknownItem]'s call_id
// is read from Raw, and a registered extension type's from its encoding.
func CallID(item Item) string {
	switch v := item.(type) {
	case nil, *Message, *ReasoningItem, *Compaction, *ItemReference:
		return ""
	case *FunctionCall:
		return v.CallID
	case *FunctionCallOutput:
		return v.CallID
	case *UnknownItem:
		return stringMember(v.Raw, "call_id")
	}
	return encodedMember(item, "call_id")
}

// encodedMember returns the string member key of item's wire form, or ""
// when the item does not encode or the member is absent or not a string.
func encodedMember(item Item, key string) string {
	raw, err := json.Marshal(item)
	if err != nil {
		return ""
	}
	return stringMember(raw, key)
}

// stringMember returns the string member key of the JSON object raw, or
// "" when raw is not an object or the member is absent or not a string.
func stringMember(raw []byte, key string) string {
	var members map[string]json.RawMessage
	if json.Unmarshal(raw, &members) != nil {
		return ""
	}
	var s string
	if json.Unmarshal(members[key], &s) != nil {
		return ""
	}
	return s
}
