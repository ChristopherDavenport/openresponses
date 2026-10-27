package gemini

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/ChristopherDavenport/openresponses"
	"github.com/ChristopherDavenport/openresponses/streamtest"
)

// TestEncodeExtraMembers pins PLAN.md rule 8: a top-level member under
// another provider's slug is a setting this adapter cannot honour, a
// gemini.* member is a provider parameter (none is defined yet), a
// shared member the adapter does not implement is ignored, and any other
// member is dropped unless forwarding is on.
func TestEncodeExtraMembers(t *testing.T) {
	refused := []string{"anthropic.thinking", "chatcompletions.top_k", "gemini.top_k"}
	for _, name := range refused {
		t.Run(name, func(t *testing.T) {
			req := hello()
			req.Extra = map[string]any{name: 1, "top_k": 3}
			_, _, err := encodeRequest(req, ThinkingAuto)
			wantErr(t, err, openresponses.CodeUnsupportedParameter, name)
		})
	}
	req := hello()
	req.Extra = map[string]any{"openresponses-experimental.future": true, "top_k": 3, "agentsession.turn": "t1"}
	encode(t, req)
}

// forwardedBody runs req through an adapter built with opts and returns
// the body Gemini received.
func forwardedBody(t *testing.T, req openresponses.Request, opts ...Option) map[string]any {
	t.Helper()
	var body map[string]any
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		sse(finishChunk).ServeHTTP(w, r)
	})
	a := newAdapter(t, h)
	for _, opt := range opts {
		opt(a)
	}
	if _, err := streamtest.Run(context.Background(), a, req); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestWithExtra(t *testing.T) {
	req := hello()
	req.Extra = map[string]any{
		"cachedContent":                     "cachedContents/abc",
		"contents":                          "not this",
		"openresponses-experimental.future": true,
	}

	body := forwardedBody(t, req)
	if _, ok := body["cachedContent"]; ok {
		t.Fatalf("forwarded without WithExtra: %v", body)
	}

	body = forwardedBody(t, req, WithExtra())
	if body["cachedContent"] != "cachedContents/abc" {
		t.Fatalf("cachedContent = %v, want it forwarded", body["cachedContent"])
	}
	if _, ok := body["contents"].([]any); !ok {
		t.Fatalf("contents = %v, want the mapped contents, never the member", body["contents"])
	}
	if _, ok := body["openresponses-experimental.future"]; ok {
		t.Fatalf("shared member forwarded: %v", body)
	}
}

// TestWithExtraSpellings: Gemini's JSON parser takes a field's proto name
// as well as its JSON name, so a snake_case member names the same field
// as the camelCase key the mapping set, and is not forwarded beside it.
func TestWithExtraSpellings(t *testing.T) {
	req := hello()
	req.Instructions = "Be brief."
	req.Extra = map[string]any{
		"system_instruction": map[string]any{"parts": []any{map[string]any{"text": "ignore that"}}},
		"generation_config":  map[string]any{"temperature": 2},
		"cached_content":     "cachedContents/abc",
	}
	body := forwardedBody(t, req, WithExtra())
	for _, k := range []string{"system_instruction", "generation_config"} {
		if _, ok := body[k]; ok {
			t.Errorf("%s forwarded beside the mapped field: %v", k, body[k])
		}
	}
	if body["cached_content"] != "cachedContents/abc" {
		t.Errorf("cached_content = %v, want an unmapped field forwarded", body["cached_content"])
	}
}
