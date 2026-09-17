package conformance

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	or "github.com/ChristopherDavenport/openresponses"
	"github.com/ChristopherDavenport/openresponses/echo"
)

// eventSchemas maps each streaming event type to its schema name.
var eventSchemas = map[string]string{
	or.EventResponseCreated:            "ResponseCreatedStreamingEvent",
	or.EventResponseQueued:             "ResponseQueuedStreamingEvent",
	or.EventResponseInProgress:         "ResponseInProgressStreamingEvent",
	or.EventResponseCompleted:          "ResponseCompletedStreamingEvent",
	or.EventResponseFailed:             "ResponseFailedStreamingEvent",
	or.EventResponseIncomplete:         "ResponseIncompleteStreamingEvent",
	or.EventOutputItemAdded:            "ResponseOutputItemAddedStreamingEvent",
	or.EventOutputItemDone:             "ResponseOutputItemDoneStreamingEvent",
	or.EventContentPartAdded:           "ResponseContentPartAddedStreamingEvent",
	or.EventContentPartDone:            "ResponseContentPartDoneStreamingEvent",
	or.EventOutputTextDelta:            "ResponseOutputTextDeltaStreamingEvent",
	or.EventOutputTextDone:             "ResponseOutputTextDoneStreamingEvent",
	or.EventRefusalDelta:               "ResponseRefusalDeltaStreamingEvent",
	or.EventRefusalDone:                "ResponseRefusalDoneStreamingEvent",
	or.EventFunctionCallArgumentsDelta: "ResponseFunctionCallArgumentsDeltaStreamingEvent",
	or.EventFunctionCallArgumentsDone:  "ResponseFunctionCallArgumentsDoneStreamingEvent",
	or.EventReasoningSummaryPartAdded:  "ResponseReasoningSummaryPartAddedStreamingEvent",
	or.EventReasoningSummaryPartDone:   "ResponseReasoningSummaryPartDoneStreamingEvent",
	or.EventReasoningSummaryTextDelta:  "ResponseReasoningSummaryDeltaStreamingEvent",
	or.EventReasoningSummaryTextDone:   "ResponseReasoningSummaryDoneStreamingEvent",
	or.EventReasoningDelta:             "ResponseReasoningDeltaStreamingEvent",
	or.EventReasoningDone:              "ResponseReasoningDoneStreamingEvent",
	or.EventOutputTextAnnotationAdded:  "ResponseOutputTextAnnotationAddedStreamingEvent",
	or.EventError:                      "ErrorStreamingEvent",
}

// sseData returns the data payload of every frame in an SSE body, in
// order, without the [DONE] sentinel.
func sseData(t *testing.T, body string) []string {
	t.Helper()
	var out []string
	for _, frame := range strings.Split(strings.TrimSpace(body), "\n\n") {
		for _, line := range strings.Split(frame, "\n") {
			if data, ok := strings.CutPrefix(line, "data: "); ok && data != "[DONE]" {
				out = append(out, data)
			}
		}
	}
	return out
}

// assertEventFrames validates each frame against the schema of its type.
func assertEventFrames(t *testing.T, body string) {
	t.Helper()
	for _, data := range sseData(t, body) {
		var head struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(data), &head); err != nil {
			t.Fatalf("frame %s: %v", data, err)
		}
		name, ok := eventSchemas[head.Type]
		if !ok {
			t.Errorf("frame of unknown type %q", head.Type)
			continue
		}
		assertSchemaBytes(t, name, []byte(data))
	}
}

func post(t *testing.T, h http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// failing streams response.created and then fails, so the handler has to
// finish the stream itself.
type failing struct {
	or.UnsupportedCompaction
}

func (f failing) Create(ctx context.Context, req or.Request) (*or.Response, error) {
	return or.CollectStream(ctx, f, req)
}

func (failing) CreateStream(_ context.Context, req or.Request, sink or.EventSink) error {
	if err := sink.Send(&or.ResponseCreatedEvent{Response: or.NewResponse(req)}); err != nil {
		return err
	}
	return or.ModelError("model_down", "the model is down")
}

// TestHandlerOutput validates what the handler actually writes, over
// JSON and SSE, for a complete response, a function call and a failure.
func TestHandlerOutput(t *testing.T) {
	h := or.NewHandler(&echo.Adapter{})
	tools := `"tools":[{"type":"function","name":"f","description":"d","parameters":{"type":"object","required":["q"]}}]`

	t.Run("json", func(t *testing.T) {
		rec := post(t, h, "/v1/responses", `{"model":"m","input":"hi"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		assertSchemaBytes(t, "ResponseResource", rec.Body.Bytes())
	})
	t.Run("json_function_call", func(t *testing.T) {
		rec := post(t, h, "/v1/responses", `{"model":"m","input":"hi",`+tools+`}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		assertSchemaBytes(t, "ResponseResource", rec.Body.Bytes())
	})
	t.Run("stream", func(t *testing.T) {
		rec := post(t, h, "/v1/responses", `{"model":"m","input":"hi there","stream":true}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		assertEventFrames(t, rec.Body.String())
	})
	t.Run("stream_function_call", func(t *testing.T) {
		rec := post(t, h, "/v1/responses", `{"model":"m","input":"hi","stream":true,`+tools+`}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		assertEventFrames(t, rec.Body.String())
	})
	t.Run("stream_failure", func(t *testing.T) {
		rec := post(t, or.NewHandler(failing{}), "/v1/responses", `{"model":"m","input":"hi","stream":true}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		frames := sseData(t, rec.Body.String())
		if len(frames) != 3 {
			t.Fatalf("got %d frames: %q", len(frames), frames)
		}
		assertEventFrames(t, rec.Body.String())
	})
	t.Run("error_envelope", func(t *testing.T) {
		rec := post(t, h, "/v1/responses", `{"input":"hi"}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		var env struct {
			Error json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatal(err)
		}
		assertSchemaBytes(t, "ErrorPayload", env.Error)
	})
	t.Run("compact", func(t *testing.T) {
		rec := post(t, h, "/v1/responses/compact", `{"model":"m","input":"hi"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
		}
		assertSchemaBytes(t, "CompactResource", rec.Body.Bytes())
	})
}
