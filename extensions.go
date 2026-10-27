package openresponses

import (
	"encoding/json"

	"github.com/ChristopherDavenport/openresponses/internal/jsonx"
)

// ExtensionSlug is the slug of the shared extensions defined in
// docs/extensions.md: members for what more than one provider needs and
// the specification has no member for. Each is optional, and a reader
// that ignores one gets the behaviour the specification defines.
const ExtensionSlug = "openresponses-experimental."

// Member names of the shared extensions. Each lives in the Extra field of
// the object it belongs to; the accessors below read and write them.
const (
	// MemberEffort is a member of the request's reasoning object asking
	// for an effort above xhigh (docs/extensions.md, section 3).
	MemberEffort = ExtensionSlug + "effort"
	// MemberIssuer is a member of a reasoning item naming who issued its
	// encrypted_content (section 4).
	MemberIssuer = ExtensionSlug + "issuer"
	// MemberCacheWriteTokens is a member of usage.input_tokens_details
	// counting the input tokens written to the prompt cache (section 5).
	MemberCacheWriteTokens = ExtensionSlug + "cache_write_tokens"
	// MemberCacheWriteTokensByTTL is a member of
	// usage.input_tokens_details splitting MemberCacheWriteTokens by how
	// long the provider keeps the entry (section 5).
	MemberCacheWriteTokensByTTL = ExtensionSlug + "cache_write_tokens_by_ttl"
	// MemberModelInfo is a member of the model object served at
	// GET /models/{model}; its value is an [Info] (section 7).
	MemberModelInfo = ExtensionSlug + "model_info"
)

// EffortMax is the one value defined for [MemberEffort]: the most
// reasoning the model offers, above xhigh where the provider
// distinguishes the two. It is not a value of the specification's
// reasoning effort enum and never goes in [ReasoningConfig.Effort]; it
// may appear in [Info.Efforts].
const EffortMax = "max"

// ExtendedEffort returns the effort [MemberEffort] asks for, or "" when
// the member is absent or holds a value docs/extensions.md does not
// define, which a reader treats as absent.
func (r ReasoningConfig) ExtendedEffort() string {
	if v, _ := r.Extra[MemberEffort].(string); v == EffortMax {
		return v
	}
	return ""
}

// SetExtendedEffort sets [MemberEffort] to effort, which must be a value
// docs/extensions.md defines ([EffortMax]), and sets Effort to the
// specification value nearest it, xhigh, as a writer must, so a server
// that ignores the member does the closest thing the specification can
// express. An empty effort removes the member and leaves Effort as it
// is.
func (r *ReasoningConfig) SetExtendedEffort(effort string) {
	if effort == "" {
		delete(r.Extra, MemberEffort)
		return
	}
	setExtra(&r.Extra, MemberEffort, effort)
	r.Effort = ReasoningEffortXHigh
}

// Issuer returns the reasoning item's [MemberIssuer], `<issuer>` or
// `<issuer>/<variant>`, or "" when the item carries none and who issued
// its encrypted_content is unknown. Compare the whole string.
func (r *ReasoningItem) Issuer() string {
	v, _ := r.Extra[MemberIssuer].(string)
	return v
}

// SetIssuer sets [MemberIssuer]. An adapter that emits a reasoning item
// with encrypted_content sets its own issuer, the slug its extensions
// are defined under. An empty issuer removes the member.
func (r *ReasoningItem) SetIssuer(issuer string) {
	if issuer == "" {
		delete(r.Extra, MemberIssuer)
		return
	}
	setExtra(&r.Extra, MemberIssuer, issuer)
}

// CacheWriteTokens returns [MemberCacheWriteTokens], the input tokens
// written to the prompt cache, and whether the member is present. They
// are counted in the usage's input tokens and are disjoint from
// CachedTokens.
func (d InputTokensDetails) CacheWriteTokens() (int, bool) {
	return intValue(d.Extra[MemberCacheWriteTokens])
}

// CacheWriteTokensByTTL returns [MemberCacheWriteTokensByTTL], the cache
// writes keyed by how long the provider keeps the entry ("5m", "1h"), or
// nil when the provider did not report the split.
func (d InputTokensDetails) CacheWriteTokensByTTL() map[string]int {
	switch v := d.Extra[MemberCacheWriteTokensByTTL].(type) {
	case map[string]int:
		out := make(map[string]int, len(v))
		for k, n := range v {
			out[k] = n
		}
		return out
	case map[string]any:
		out := make(map[string]int, len(v))
		for k, raw := range v {
			if n, ok := intValue(raw); ok {
				out[k] = n
			}
		}
		return out
	}
	return nil
}

// SetCacheWriteTokens sets [MemberCacheWriteTokens] to n and removes any
// split by TTL, for a provider that reports the count alone.
func (d *InputTokensDetails) SetCacheWriteTokens(n int) {
	delete(d.Extra, MemberCacheWriteTokensByTTL)
	setExtra(&d.Extra, MemberCacheWriteTokens, n)
}

// SetCacheWriteTokensByTTL sets [MemberCacheWriteTokensByTTL] to a copy
// of byTTL and [MemberCacheWriteTokens] to the sum of its values, so the
// two agree as docs/extensions.md requires.
func (d *InputTokensDetails) SetCacheWriteTokensByTTL(byTTL map[string]int) {
	split := make(map[string]int, len(byTTL))
	total := 0
	for k, n := range byTTL {
		split[k] = n
		total += n
	}
	setExtra(&d.Extra, MemberCacheWriteTokens, total)
	setExtra(&d.Extra, MemberCacheWriteTokensByTTL, split)
}

// Info is what a model accepts, the value of [MemberModelInfo] as
// docs/extensions.md section 7 defines it. Source is required. Every
// other field is optional, and a zero or nil field means the source did
// not say, never "no" or zero. A writer fills a field only from what the
// provider publishes, never from a table of models it keeps.
//
// The information is a hint taken before a request. Without it a client
// sends the request as asked, and a request the model refuses still
// fails as the provider says.
type Info struct {
	// Source is who answered: the provider's slug ("anthropic",
	// "gemini", "openrouter", "ollama") or the serving implementation's
	// name.
	Source string `json:"source"`
	// ContextWindow is the most input tokens one request may carry,
	// with its instructions and tools.
	ContextWindow int `json:"context_window,omitempty"`
	// MaxOutputTokens is the most output tokens one response may carry,
	// reasoning included.
	MaxOutputTokens int `json:"max_output_tokens,omitempty"`
	// Reasoning is whether the model reasons at all.
	Reasoning *bool `json:"reasoning,omitempty"`
	// Efforts are the reasoning efforts the model accepts, ordered from
	// least to most: the specification's values, "minimal" and
	// [EffortMax]. "none" is listed when reasoning can be turned off; a
	// model that cannot reason lists only "none".
	Efforts []ReasoningEffort `json:"efforts,omitempty"`
	// DefaultEffort is the effort the model runs at when a request sends
	// none.
	DefaultEffort ReasoningEffort `json:"default_effort,omitempty"`
	// InputModalities are the input content the model accepts: "text",
	// "image", "file", "audio", "video".
	InputModalities []string `json:"input_modalities,omitempty"`

	// Extra holds members docs/extensions.md does not define.
	Extra map[string]any `json:"-"`
}

// MarshalJSON emits the information with its Extra members.
func (i Info) MarshalJSON() ([]byte, error) {
	type plain Info
	return jsonx.MarshalWithExtra(plain(i), i.Extra, nil)
}

// UnmarshalJSON decodes the information and keeps unknown members in
// Extra.
func (i *Info) UnmarshalJSON(data []byte) error {
	type plain Info
	extra, _, err := jsonx.UnmarshalExtra(data, (*plain)(i))
	i.Extra = extra
	return err
}

// setExtra sets key in the Extra map at m, creating the map when needed.
func setExtra(m *map[string]any, key string, value any) {
	if *m == nil {
		*m = map[string]any{}
	}
	(*m)[key] = value
}

// intValue reads an integer from an Extra value: an int set in Go, or the
// float64 or json.Number decoding produces.
func intValue(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		if n != float64(int(n)) {
			return 0, false
		}
		return int(n), true
	case json.Number:
		i, err := n.Int64()
		return int(i), err == nil
	}
	return 0, false
}
