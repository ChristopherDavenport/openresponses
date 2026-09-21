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
//
// reasoning.effort reaches Gemini as a thinking level or a thinking
// budget depending on the generation the model ID names; see [Thinking]
// for the model IDs that do not name one.
package gemini

import (
	"context"
	"strings"

	"github.com/ChristopherDavenport/openresponses"
	"google.golang.org/genai"
)

// Adapter implements [openresponses.Adapter] over a Gemini client.
type Adapter struct {
	openresponses.UnsupportedCompaction

	client   *genai.Client
	thinking Thinking
}

var _ openresponses.Adapter = (*Adapter)(nil)

// An Option configures an [Adapter].
type Option func(*Adapter)

// Thinking selects the field reasoning.effort is sent in. The two are
// mutually exclusive: a request carrying both is rejected, and each
// generation rejects the one it does not take, so this cannot be left to
// the caller's effort value alone.
type Thinking int

const (
	// ThinkingAuto reads the encoding off the model ID. This is the zero
	// value and the right answer unless the ID does not name its
	// generation.
	ThinkingAuto Thinking = iota
	// ThinkingLevel always sends thinkingLevel, which Gemini 3 and later
	// take and Gemini 2.5 and earlier reject.
	ThinkingLevel
	// ThinkingBudget always sends thinkingBudget, in tokens. Gemini 2.5
	// and earlier take only this; Gemini 3 accepts it for backward
	// compatibility but reasons less predictably under it.
	ThinkingBudget
)

// WithThinking fixes the reasoning.effort encoding rather than deriving it
// from the model ID. Set it for a tuned model or a private endpoint, whose
// ID does not say which generation it serves.
func WithThinking(t Thinking) Option {
	return func(a *Adapter) { a.thinking = t }
}

// thinkingFor derives the encoding from a model ID, for [ThinkingAuto].
// Anything that does not name a generation taking budgets is assumed to
// take levels, which is where the family has gone.
func thinkingFor(model string) Thinking {
	name := model
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	if strings.HasPrefix(name, "gemini-1.") || strings.HasPrefix(name, "gemini-2.") {
		return ThinkingBudget
	}
	return ThinkingLevel
}

// New returns an adapter over client.
func New(client *genai.Client, opts ...Option) *Adapter {
	a := &Adapter{client: client}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// Create produces the response that CreateStream would stream.
func (a *Adapter) Create(ctx context.Context, req openresponses.Request) (*openresponses.Response, error) {
	return openresponses.CollectStream(ctx, a, req)
}
