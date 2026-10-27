package anthropic

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/ChristopherDavenport/openresponses"
	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
)

const modelJSON = `{"type":"model","id":"claude-opus-5-5","display_name":"Claude Opus 5.5","created_at":"2026-09-01T00:00:00Z",
"max_input_tokens":1000000,"max_tokens":128000,
"capabilities":{
 "thinking":{"supported":true,"types":{"adaptive":{"supported":true},"enabled":{"supported":false}}},
 "effort":{"supported":true,"low":{"supported":true},"medium":{"supported":true},"high":{"supported":true},"xhigh":{"supported":true},"max":{"supported":true}},
 "image_input":{"supported":true},"pdf_input":{"supported":true}}}`

func describer(t *testing.T, handler http.Handler) *Adapter {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client := sdk.NewClient(option.WithAPIKey("test"), option.WithBaseURL(srv.URL), option.WithMaxRetries(0))
	return New(client.Messages, WithModels(client.Models))
}

func TestDescribe(t *testing.T) {
	a := describer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models/claude-opus-5-5" {
			apiError(404, `{"type":"error","error":{"type":"not_found_error","message":"model: x"}}`, nil).ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, modelJSON)
	}))
	info, err := a.Describe(context.Background(), "claude-opus-5-5")
	if err != nil {
		t.Fatal(err)
	}
	want := []openresponses.ReasoningEffort{"none", "low", "medium", "high", "xhigh", "max"}
	if info.Source != "anthropic" || info.ContextWindow != 1000000 || info.MaxOutputTokens != 128000 ||
		info.Reasoning == nil || !*info.Reasoning || !slices.Equal(info.Efforts, want) {
		t.Fatalf("info = %+v", info)
	}
	if info.DefaultEffort != "" || info.InputModalities != nil {
		t.Fatalf("Claude does not say the default effort or the modalities: %+v", info)
	}

	_, err = a.Describe(context.Background(), "claude-nope")
	var oerr *openresponses.Error
	if !errorsAs(err, &oerr) || oerr.Type != openresponses.ErrorTypeNotFound {
		t.Fatalf("unknown model: %v", err)
	}
}

func TestDescribeWithoutEffort(t *testing.T) {
	m := `{"type":"model","id":"claude-3-5-haiku","display_name":"x","created_at":"2024-10-22T00:00:00Z","max_input_tokens":200000,"max_tokens":8192,
"capabilities":{"thinking":{"supported":false,"types":{"adaptive":{"supported":false},"enabled":{"supported":false}}},"effort":{"supported":false,"low":{"supported":false},"medium":{"supported":false},"high":{"supported":false},"xhigh":{"supported":false},"max":{"supported":false}}}}`
	a := describer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, m)
	}))
	info, err := a.Describe(context.Background(), "claude-3-5-haiku")
	if err != nil {
		t.Fatal(err)
	}
	if info.Reasoning == nil || *info.Reasoning || info.Efforts != nil {
		t.Fatalf("info = %+v", info)
	}
}

func TestDescribeWithoutModels(t *testing.T) {
	_, err := New(sdk.MessageService{}).Describe(context.Background(), "claude-opus-5-5")
	var oerr *openresponses.Error
	if !errorsAs(err, &oerr) || oerr.Type != openresponses.ErrorTypeNotFound {
		t.Fatalf("err = %v", err)
	}
}
