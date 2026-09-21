// Package anthropic serves Claude as an [openresponses.Adapter] through
// the github.com/anthropics/anthropic-sdk-go Messages API.
//
// The adapter takes a constructed [sdk.MessageService] and knows nothing
// about endpoints or credentials. The Claude API, Vertex AI and Bedrock
// differ only in how the client is built, and every client exposes the
// service as its Messages field:
//
//	sdk.NewClient(option.WithAPIKey(key)).Messages
//	sdk.NewClient(vertex.WithGoogleAuth(ctx, location, project)).Messages
//	sdk.NewClient(bedrock.WithLoadDefaultConfig(ctx)).Messages
//
// The Vertex form uses Application Default Credentials.
//
// Requests map onto one Messages call and its stream maps back onto Open
// Responses items; fields the Messages API has no equivalent for are
// rejected rather than dropped, and Claude's own tools, content blocks
// and thinking signatures travel through anthropic.* extension types and
// reasoning items so a conversation round-trips without loss. The README
// lists what is rejected and what is accepted without effect.
// Compaction is not supported.
package anthropic

import (
	"context"

	"github.com/ChristopherDavenport/openresponses"
	sdk "github.com/anthropics/anthropic-sdk-go"
)

// DefaultMaxTokens is the max_tokens sent when a request has no
// max_output_tokens. The Messages API requires the field.
const DefaultMaxTokens = 32768

// DefaultContinuations is how many times a turn Claude paused
// (stop_reason pause_turn, used by long-running server tools) is resumed
// before the response completes with what it has.
const DefaultContinuations = 8

// Adapter implements [openresponses.Adapter] over the Messages API.
type Adapter struct {
	openresponses.UnsupportedCompaction

	messages      sdk.MessageService
	maxTokens     int64
	continuations int
}

var _ openresponses.Adapter = (*Adapter)(nil)

// Option configures an Adapter.
type Option func(*Adapter)

// WithMaxTokens sets the max_tokens used when a request carries no
// max_output_tokens. The default is [DefaultMaxTokens].
func WithMaxTokens(n int) Option {
	return func(a *Adapter) { a.maxTokens = int64(n) }
}

// WithContinuations sets how many times a paused turn is resumed. The
// default is [DefaultContinuations]; zero never resumes.
func WithContinuations(n int) Option {
	return func(a *Adapter) { a.continuations = n }
}

// New returns an adapter over messages, the Messages field of any client
// the SDK builds.
func New(messages sdk.MessageService, opts ...Option) *Adapter {
	a := &Adapter{messages: messages, maxTokens: DefaultMaxTokens, continuations: DefaultContinuations}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// Create produces the response that CreateStream would stream.
func (a *Adapter) Create(ctx context.Context, req openresponses.Request) (*openresponses.Response, error) {
	return openresponses.CollectStream(ctx, a, req)
}
