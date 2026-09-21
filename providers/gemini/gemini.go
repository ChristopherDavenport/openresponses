// Package gemini serves Google's Gemini models as an
// [openresponses.Adapter] through the google.golang.org/genai SDK.
//
// The adapter takes a constructed [genai.Client] and knows nothing about
// endpoints or credentials: the Gemini API and Vertex AI differ only in
// how the client is built.
//
//	genai.NewClient(ctx, &genai.ClientConfig{APIKey: key})
//	genai.NewClient(ctx, &genai.ClientConfig{Backend: genai.BackendVertexAI, Project: p, Location: l})
//
// The second form uses Application Default Credentials. The SDK reads
// GOOGLE_API_KEY or GEMINI_API_KEY, GOOGLE_CLOUD_PROJECT,
// GOOGLE_CLOUD_LOCATION and GOOGLE_GENAI_USE_VERTEXAI when the
// corresponding fields are empty.
//
// Requests map onto one GenerateContent call and its stream maps back
// onto Open Responses items; fields Gemini has no equivalent for are
// rejected rather than dropped, and Gemini's own tools, parts and
// thought signatures travel through gemini.* extension types and
// reasoning items so a conversation round-trips without loss. The
// README lists what is rejected and what is accepted without effect.
// Compaction is not supported.
package gemini

import (
	"context"

	"github.com/ChristopherDavenport/openresponses"
	"google.golang.org/genai"
)

// Adapter implements [openresponses.Adapter] over a Gemini client.
type Adapter struct {
	openresponses.UnsupportedCompaction

	client *genai.Client
}

var _ openresponses.Adapter = (*Adapter)(nil)

// New returns an adapter over client.
func New(client *genai.Client) *Adapter {
	return &Adapter{client: client}
}

// Create produces the response that CreateStream would stream.
func (a *Adapter) Create(ctx context.Context, req openresponses.Request) (*openresponses.Response, error) {
	return openresponses.CollectStream(ctx, a, req)
}
