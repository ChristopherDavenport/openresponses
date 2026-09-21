// Package chatcompletions serves a Chat Completions endpoint, the
// protocol most model providers speak, as an [openresponses.Adapter].
//
// The adapter reuses an [openresponses.Client] for the server address,
// authentication and HTTP transport, so the same options configure a
// server that speaks Open Responses and one that speaks Chat
// Completions:
//
//	client := openresponses.NewClient("https://api.deepseek.com/v1", openresponses.WithAPIKey(key))
//	http.Handle("/v1/", openresponses.NewHandler(chatcompletions.New(client)))
//
// Requests map onto one POST to {base}/chat/completions and its stream
// maps back onto Open Responses items. Fields the protocol has no
// equivalent for are rejected rather than dropped, with two documented
// exceptions the protocol itself imposes: reasoning has no standard
// field, so reasoning items are replayed only into the field
// [WithReasoningReplay] names, and reasoning.summary has no effect. The
// README lists everything else. Compaction is not supported.
package chatcompletions

import (
	"context"

	"github.com/ChristopherDavenport/openresponses"
)

// Adapter implements [openresponses.Adapter] over a Chat Completions
// endpoint.
type Adapter struct {
	openresponses.UnsupportedCompaction

	client          *openresponses.Client
	maxTokensField  string
	reasoningReplay string
	forwardExtra    bool
}

var _ openresponses.Adapter = (*Adapter)(nil)

// Option configures an Adapter.
type Option func(*Adapter)

// WithMaxTokensField names the field max_output_tokens is sent as. The
// default is "max_tokens", which every provider takes; OpenAI's newer
// name is "max_completion_tokens".
func WithMaxTokensField(name string) Option {
	return func(a *Adapter) { a.maxTokensField = name }
}

// WithReasoningReplay names the assistant message field reasoning items
// are sent back as when a conversation is replayed, for example
// "reasoning_content" for DeepSeek or "reasoning" for OpenRouter. The
// protocol has no standard field; without this option reasoning items
// in the input are accepted and not sent.
func WithReasoningReplay(field string) Option {
	return func(a *Adapter) { a.reasoningReplay = field }
}

// WithExtra forwards the request's unknown top-level keys (Extra) into
// the Chat Completions body, for provider parameters such as top_k or
// routing preferences. Keys the mapping sets are never overridden. Any
// key a client sends then reaches the provider, so a server that does
// not trust its clients should filter Extra first.
func WithExtra() Option {
	return func(a *Adapter) { a.forwardExtra = true }
}

// New returns an adapter that posts to client's base URL.
func New(client *openresponses.Client, opts ...Option) *Adapter {
	a := &Adapter{client: client, maxTokensField: "max_tokens"}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// Create produces the response that CreateStream would stream.
func (a *Adapter) Create(ctx context.Context, req openresponses.Request) (*openresponses.Response, error) {
	return openresponses.CollectStream(ctx, a, req)
}
