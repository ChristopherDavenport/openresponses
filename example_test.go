package openresponses_test

import (
	"encoding/json"
	"fmt"

	"github.com/christopherdavenport/openresponses"
)

func ExampleRequest() {
	req := openresponses.Request{
		Model: "gpt-5",
		Input: openresponses.Input{
			openresponses.SystemText("Be brief."),
			openresponses.UserText("Hello"),
		},
		Tools: openresponses.Tools{
			openresponses.NewFunctionTool("get_weather", "Current weather", json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`)),
		},
	}
	data, _ := json.Marshal(req)
	fmt.Println(string(data))
	// Output: {"model":"gpt-5","input":[{"type":"message","role":"system","content":[{"type":"input_text","text":"Be brief."}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"Hello"}]}],"tools":[{"type":"function","name":"get_weather","description":"Current weather","parameters":{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}}]}
}

func ExampleDecodeEvent() {
	ev, _ := openresponses.DecodeEvent([]byte(`{"type":"response.output_text.delta","sequence_number":3,"item_id":"msg_1","output_index":0,"content_index":0,"delta":"Hi"}`))
	switch e := ev.(type) {
	case *openresponses.OutputTextDeltaEvent:
		fmt.Println(e.Sequence(), e.Delta)
	}
	// Output: 3 Hi
}

func ExampleUnmarshalItem_extension() {
	item, _ := openresponses.UnmarshalItem([]byte(`{"type":"acme:search_result","id":"sr_1","status":"completed","hits":3}`))
	u := item.(*openresponses.UnknownItem)
	fmt.Println(u.Type, u.ID, u.Status)
	// Output: acme:search_result sr_1 completed
}
