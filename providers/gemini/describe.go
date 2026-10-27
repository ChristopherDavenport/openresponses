package gemini

import (
	"context"

	"github.com/ChristopherDavenport/openresponses"
	"google.golang.org/genai"
)

var _ openresponses.Describer = (*Adapter)(nil)

// Describe reports what a model accepts, from Gemini's models.get
// (docs/extensions.md section 7). It fills a field only from what the
// endpoint publishes: inputTokenLimit is the context window,
// outputTokenLimit the output limit, and thinking whether the model
// reasons, with efforts ["none"] when it does not. The efforts of a
// thinking model, its default effort and its input modalities are not
// published, so they are left out.
//
// Vertex AI's model resource carries none of these, and the SDK maps
// none of them from it, so on that backend the result names its source
// and nothing else: thinking is a boolean the SDK cannot tell apart from
// absent, and an absent member must not read as false.
//
// A model Gemini does not know is a not_found error, as any failure is
// mapped for a request.
func (a *Adapter) Describe(ctx context.Context, model string) (*openresponses.Info, error) {
	m, err := a.client.Models.Get(ctx, model, nil)
	if err != nil {
		return nil, mapError(err)
	}
	if a.client.ClientConfig().Backend == genai.BackendVertexAI {
		return &openresponses.Info{Source: issuer}, nil
	}
	// The Gemini API leaves thinking out when it is false.
	thinking := m.Thinking
	info := &openresponses.Info{
		Source:          issuer,
		ContextWindow:   int(m.InputTokenLimit),
		MaxOutputTokens: int(m.OutputTokenLimit),
		Reasoning:       &thinking,
	}
	if !thinking {
		info.Efforts = []openresponses.ReasoningEffort{openresponses.ReasoningEffortNone}
	}
	return info, nil
}
