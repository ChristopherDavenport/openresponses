package gemini

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/ChristopherDavenport/openresponses"
	"google.golang.org/genai"
)

// model serves body as Gemini's models.get for the model named name.
func model(t *testing.T, name, body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/"+name) {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	})
}

func TestDescribe(t *testing.T) {
	cases := []struct {
		name, id, body string
		want           openresponses.Info
	}{
		{
			"thinking", "gemini-2.5-pro",
			`{"name":"models/gemini-2.5-pro","inputTokenLimit":1048576,"outputTokenLimit":65536,"thinking":true}`,
			openresponses.Info{Source: "gemini", ContextWindow: 1048576, MaxOutputTokens: 65536, Reasoning: ptr(true)},
		},
		{
			// The Gemini API leaves thinking out when it is false, and a
			// model that cannot reason lists only none.
			"not thinking", "gemini-2.0-flash",
			`{"name":"models/gemini-2.0-flash","inputTokenLimit":1048576,"outputTokenLimit":8192}`,
			openresponses.Info{Source: "gemini", ContextWindow: 1048576, MaxOutputTokens: 8192, Reasoning: ptr(false),
				Efforts: []openresponses.ReasoningEffort{openresponses.ReasoningEffortNone}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info, err := newAdapter(t, model(t, tc.id, tc.body)).Describe(context.Background(), tc.id)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(*info, tc.want) {
				t.Fatalf("info = %+v, want %+v", *info, tc.want)
			}
		})
	}
}

func TestDescribeNotFound(t *testing.T) {
	a := newAdapter(t, apiError(http.StatusNotFound, `{"error":{"code":404,"message":"models/nope is not found","status":"NOT_FOUND"}}`))
	_, err := a.Describe(context.Background(), "nope")
	if !openresponses.IsNotFound(err) {
		t.Fatalf("err = %v, want not_found", err)
	}
}

// TestDescribeVertex: Vertex AI's model resource publishes none of the
// members, so the result names its source alone rather than reading an
// absent thinking flag as false.
func TestDescribeVertex(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"name":"publishers/google/models/gemini-2.5-pro","versionId":"001"}`)
	}))
	t.Cleanup(srv.Close)
	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
		Backend:     genai.BackendVertexAI,
		APIKey:      "test",
		HTTPOptions: genai.HTTPOptions{BaseURL: srv.URL},
	})
	if err != nil {
		t.Fatal(err)
	}
	info, err := New(client).Describe(context.Background(), "gemini-2.5-pro")
	if err != nil {
		t.Fatal(err)
	}
	if want := (openresponses.Info{Source: "gemini"}); !reflect.DeepEqual(*info, want) {
		t.Fatalf("info = %+v, want %+v", *info, want)
	}
}
