package anthropic

import (
	"context"
	"fmt"

	"github.com/ChristopherDavenport/openresponses"
	sdk "github.com/anthropics/anthropic-sdk-go"
)

var _ openresponses.Describer = (*Adapter)(nil)

// WithModels gives the adapter the SDK's ModelService, the Models field
// of the client whose Messages it serves, so [Adapter.Describe] can ask
// Claude's models endpoint. Vertex AI has no such endpoint; leave it
// unset there.
func WithModels(models sdk.ModelService) Option {
	return func(a *Adapter) { a.models = &models }
}

// Describe returns what model accepts, from GET /v1/models/{model}, as
// docs/extensions.md section 7 maps it. A member Claude does not report
// is left out. Without [WithModels] the adapter knows no model and
// returns not_found, as it does for a model Claude does not know.
func (a *Adapter) Describe(ctx context.Context, model string) (*openresponses.Info, error) {
	if a.models == nil {
		return nil, openresponses.NotFound("model_not_found",
			fmt.Sprintf("cannot describe %s: the adapter has no models service (WithModels)", model), "model")
	}
	m, err := a.models.Get(ctx, model, sdk.ModelGetParams{})
	if err != nil {
		return nil, mapError(err)
	}
	return describe(m), nil
}

// describe maps Claude's model object onto the shared model information.
// efforts is none followed by each level Claude says the model takes,
// when it says the model takes an effort at all.
func describe(m *sdk.ModelInfo) *openresponses.Info {
	info := &openresponses.Info{Source: issuer}
	if m.MaxInputTokens > 0 {
		info.ContextWindow = int(m.MaxInputTokens)
	}
	if m.MaxTokens > 0 {
		info.MaxOutputTokens = int(m.MaxTokens)
	}
	caps := m.Capabilities
	if caps.JSON.Thinking.Valid() {
		reasons := caps.Thinking.Supported
		info.Reasoning = &reasons
	}
	if caps.Effort.Supported {
		info.Efforts = []openresponses.ReasoningEffort{openresponses.ReasoningEffortNone}
		for _, level := range []struct {
			effort    openresponses.ReasoningEffort
			supported bool
		}{
			{openresponses.ReasoningEffortLow, caps.Effort.Low.Supported},
			{openresponses.ReasoningEffortMedium, caps.Effort.Medium.Supported},
			{openresponses.ReasoningEffortHigh, caps.Effort.High.Supported},
			{openresponses.ReasoningEffortXHigh, caps.Effort.Xhigh.Supported},
			{openresponses.EffortMax, caps.Effort.Max.Supported},
		} {
			if level.supported {
				info.Efforts = append(info.Efforts, level.effort)
			}
		}
	}
	return info
}
