package openresponses

import (
	"encoding/json"
	"testing"
)

// extensionCall is a registered extension item type with its own Go type.
type extensionCall struct {
	ID     string `json:"id"`
	CallID string `json:"call_id"`
}

func (*extensionCall) ItemType() string { return "test:call" }

func (c *extensionCall) MarshalJSON() ([]byte, error) {
	type plain extensionCall
	return json.Marshal(struct {
		Type string `json:"type"`
		*plain
	}{"test:call", (*plain)(c)})
}

func TestItemIDAndCallID(t *testing.T) {
	unknown, err := UnmarshalItem([]byte(`{"type":"acme:shell_call","id":"sh_1","call_id":"call_9","status":"completed"}`))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		item       Item
		id, callID string
	}{
		{"message", &Message{ID: "msg_1", Role: RoleUser}, "msg_1", ""},
		{"function_call", &FunctionCall{ID: "fc_1", CallID: "call_1"}, "fc_1", "call_1"},
		{"function_call_output", &FunctionCallOutput{ID: "fco_1", CallID: "call_1"}, "fco_1", "call_1"},
		{"reasoning", &ReasoningItem{ID: "rs_1"}, "rs_1", ""},
		{"compaction", &Compaction{ID: "cmp_1"}, "cmp_1", ""},
		{"item_reference names another item", &ItemReference{ID: "msg_prev"}, "", ""},
		{"unknown item reads raw", unknown, "sh_1", "call_9"},
		{"unknown item without raw", &UnknownItem{Type: "acme:x", ID: "x_1"}, "x_1", ""},
		{"registered extension type", &extensionCall{ID: "ext_1", CallID: "call_2"}, "ext_1", "call_2"},
		{"nil", nil, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ItemID(tt.item); got != tt.id {
				t.Errorf("ItemID = %q, want %q", got, tt.id)
			}
			if got := CallID(tt.item); got != tt.callID {
				t.Errorf("CallID = %q, want %q", got, tt.callID)
			}
		})
	}
}
