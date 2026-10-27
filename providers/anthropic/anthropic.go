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
	models        *sdk.ModelService // nil without WithModels
	continuations int
	config
}

// config holds the settings the request encoder reads.
type config struct {
	maxTokens int64
	cacheTTL  sdk.CacheControlEphemeralTTL
	thinking  Thinking
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

// WithCacheTTL sets how long the prompt cache keeps what a request
// writes, for requests that opt into caching with prompt_cache_key; it
// never turns caching on by itself. The default, and the zero value, is
// the API's own: five minutes. [sdk.CacheControlEphemeralTTLTTL1h] keeps
// the prefix for an hour, but a one-hour write costs twice the base
// input price where a five-minute write costs 1.25 times, so it pays
// when requests sharing a prefix arrive 5 to 60 minutes apart.
func WithCacheTTL(ttl sdk.CacheControlEphemeralTTL) Option {
	return func(a *Adapter) { a.cacheTTL = ttl }
}

// Thinking selects how reasoning.effort is encoded. Claude has taken
// three encodings, one per model generation, and each generation refuses
// the others; the README has the table.
type Thinking int

const (
	// ThinkingAuto derives the encoding from the model ID: ThinkingBudget
	// for Claude before 4.6, ThinkingAdaptive from 4.6 up to 5.5, and
	// ThinkingBetweenTools for 5.5, later models and any ID that does not
	// name a generation.
	ThinkingAuto Thinking = iota
	// ThinkingBudget sends an effort as thinking enabled with a
	// budget_tokens from the README's table, and none as thinking
	// disabled. output_config.effort is not sent.
	ThinkingBudget
	// ThinkingAdaptive sends an effort as output_config.effort with
	// adaptive thinking, and none as thinking disabled.
	ThinkingAdaptive
	// ThinkingBetweenTools is ThinkingAdaptive with none sent as thinking
	// between_tools, which Claude 5.5 takes in place of disabled.
	ThinkingBetweenTools
)

// WithThinking fixes the reasoning.effort encoding rather than deriving
// it from the model ID. Set it for an ID that does not say which
// generation it serves, such as a Bedrock inference profile ARN.
func WithThinking(t Thinking) Option {
	return func(a *Adapter) { a.thinking = t }
}

// New returns an adapter over messages, the Messages field of any client
// the SDK builds.
func New(messages sdk.MessageService, opts ...Option) *Adapter {
	a := &Adapter{messages: messages, continuations: DefaultContinuations, config: config{maxTokens: DefaultMaxTokens}}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// Create produces the response that CreateStream would stream.
func (a *Adapter) Create(ctx context.Context, req openresponses.Request) (*openresponses.Response, error) {
	return openresponses.CollectStream(ctx, a, req)
}
