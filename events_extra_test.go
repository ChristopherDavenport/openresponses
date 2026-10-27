package openresponses

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

// TestEventsKeepUnknownMembers checks that every built-in event and the
// error object keep members the spec does not define
// (docs/extensions.md, section 1.3).
func TestEventsKeepUnknownMembers(t *testing.T) {
	events := []StreamEvent{
		&ResponseCreatedEvent{}, &ResponseQueuedEvent{}, &ResponseInProgressEvent{},
		&ResponseCompletedEvent{}, &ResponseFailedEvent{}, &ResponseIncompleteEvent{},
		&OutputItemAddedEvent{Item: AssistantText("x")}, &OutputItemDoneEvent{Item: AssistantText("x")},
		&ContentPartAddedEvent{Part: &OutputText{}}, &ContentPartDoneEvent{Part: &OutputText{}},
		&OutputTextDeltaEvent{}, &OutputTextDoneEvent{}, &RefusalDeltaEvent{}, &RefusalDoneEvent{},
		&FunctionCallArgumentsDeltaEvent{}, &FunctionCallArgumentsDoneEvent{},
		&ReasoningSummaryPartAddedEvent{Part: &SummaryText{}}, &ReasoningSummaryPartDoneEvent{Part: &SummaryText{}},
		&ReasoningSummaryTextDeltaEvent{}, &ReasoningSummaryTextDoneEvent{},
		&ReasoningDeltaEvent{}, &ReasoningDoneEvent{},
		&OutputTextAnnotationAddedEvent{Annotation: &URLCitation{}},
		&ErrorEvent{Error: ErrorPayload{Type: ErrorTypeServerError, Code: "c", Message: "m"}},
	}
	for _, ev := range events {
		t.Run(ev.EventType(), func(t *testing.T) {
			base, err := EncodeEvent(ev)
			if err != nil {
				t.Fatal(err)
			}
			var members map[string]any
			if err := json.Unmarshal(base, &members); err != nil {
				t.Fatal(err)
			}
			members["x:a"] = map[string]any{"b": 1}
			if e, ok := members["error"].(map[string]any); ok {
				e["x:c"] = "kept"
			}
			raw, _ := json.Marshal(members)
			decoded, err := DecodeEvent(raw)
			if err != nil {
				t.Fatal(err)
			}
			if reflect.TypeOf(decoded) != reflect.TypeOf(ev) {
				t.Fatalf("decoded %T", decoded)
			}
			out, err := EncodeEvent(decoded)
			if err != nil {
				t.Fatal(err)
			}
			assertSameJSON(t, raw, out)
		})
	}
}

func TestErrorKeepsUnknownMembers(t *testing.T) {
	raw := `{"type":"invalid_request","code":"c","message":"m","param":null,"x:hint":"shorten","headers":{"Retry-After":"1","Set-Cookie":"x"}}`
	var payload ErrorPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Extra["x:hint"] != "shorten" || payload.Extra["headers"] != nil {
		t.Fatalf("extra = %v", payload.Extra)
	}
	// Through *Error and back, as a proxy forwards it: the member survives
	// and the header filter still applies.
	out, err := json.Marshal(payload.Err(400).Payload())
	if err != nil {
		t.Fatal(err)
	}
	assertSameJSON(t, []byte(`{"type":"invalid_request","code":"c","message":"m","param":null,"x:hint":"shorten","headers":{"Retry-After":"1"}}`), out)
	// Extra cannot carry headers past the filter.
	e := &Error{Type: ErrorTypeServerError, Extra: map[string]any{"headers": map[string]any{"Set-Cookie": "x"}}}
	out, err = json.Marshal(e.Payload())
	if err != nil {
		t.Fatal(err)
	}
	assertSameJSON(t, []byte(`{"type":"server_error","code":"","message":"","param":null}`), out)
}

// TestRemoteErrorDropsHeadersInAnyCase checks that a "headers" member
// spelled in another case cannot carry headers past the failure-header
// filter by riding in Extra.
func TestRemoteErrorDropsHeadersInAnyCase(t *testing.T) {
	raw := `{"type":"server_error","code":"c","message":"m","param":null,"Headers":{"set-cookie":"secret"},"HEADERS":{"x":"y"},"x:hint":"kept","headers":{"Retry-After":"1"}}`
	var payload ErrorPayload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatal(err)
	}
	want := `{"type":"server_error","code":"c","message":"m","param":null,"x:hint":"kept","headers":{"Retry-After":"1"}}`
	out, err := json.Marshal(payload.Err(500).Payload())
	if err != nil {
		t.Fatal(err)
	}
	assertSameJSON(t, []byte(want), out)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":` + raw + `}`))
	}))
	defer srv.Close()
	_, err = NewClient(srv.URL).Create(context.Background(), Request{Model: "m"})
	e := AsError(err)
	for k := range e.Extra {
		if strings.EqualFold(k, "headers") {
			t.Errorf("client error kept %q in Extra: %v", k, e.Extra)
		}
	}
	if e.Extra["x:hint"] != "kept" {
		t.Errorf("client error extra = %v", e.Extra)
	}
}
