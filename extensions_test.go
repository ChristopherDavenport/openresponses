package openresponses

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
)

func TestExtendedEffort(t *testing.T) {
	var r ReasoningConfig
	if r.ExtendedEffort() != "" {
		t.Fatal("zero config has an extended effort")
	}
	r.SetExtendedEffort(EffortMax)
	if r.ExtendedEffort() != EffortMax || r.Effort != ReasoningEffortXHigh {
		t.Fatalf("after set: %#v", r)
	}
	out, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	assertSameJSON(t, []byte(`{"effort":"xhigh","summary":null,"openresponses-experimental.effort":"max"}`), out)

	var back ReasoningConfig
	if err := json.Unmarshal(out, &back); err != nil || back.ExtendedEffort() != EffortMax {
		t.Fatalf("decoded %#v, %v", back, err)
	}
	// A value the document does not define reads as absent.
	if err := json.Unmarshal([]byte(`{"effort":"xhigh","openresponses-experimental.effort":"ultra"}`), &back); err != nil || back.ExtendedEffort() != "" {
		t.Fatalf("undefined value read as %q, %v", back.ExtendedEffort(), err)
	}
	r.SetExtendedEffort("")
	if r.ExtendedEffort() != "" || r.Effort != ReasoningEffortXHigh {
		t.Fatalf("after clear: %#v", r)
	}
}

func TestIssuer(t *testing.T) {
	item := &ReasoningItem{EncryptedContent: "sig"}
	if item.Issuer() != "" {
		t.Fatal("new item has an issuer")
	}
	item.SetIssuer("anthropic")
	out, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	assertSameJSON(t, []byte(`{"type":"reasoning","summary":[],"encrypted_content":"sig","openresponses-experimental.issuer":"anthropic"}`), out)
	decoded, err := UnmarshalItem(out)
	if err != nil {
		t.Fatal(err)
	}
	if got := decoded.(*ReasoningItem).Issuer(); got != "anthropic" {
		t.Errorf("issuer = %q", got)
	}
	item.SetIssuer("")
	if _, ok := item.Extra[MemberIssuer]; ok {
		t.Error("empty issuer kept the member")
	}
}

func TestCacheWriteTokens(t *testing.T) {
	var d InputTokensDetails
	if _, ok := d.CacheWriteTokens(); ok || d.CacheWriteTokensByTTL() != nil {
		t.Fatal("zero details report cache writes")
	}
	d.SetCacheWriteTokensByTTL(map[string]int{"5m": 100, "1h": 2000})
	if n, ok := d.CacheWriteTokens(); !ok || n != 2100 {
		t.Fatalf("total = %d, %v", n, ok)
	}
	out, err := json.Marshal(Usage{InputTokens: 2300, InputTokensDetails: d})
	if err != nil {
		t.Fatal(err)
	}
	var u Usage
	if err := json.Unmarshal(out, &u); err != nil {
		t.Fatal(err)
	}
	if n, ok := u.InputTokensDetails.CacheWriteTokens(); !ok || n != 2100 {
		t.Errorf("decoded total = %d, %v in %s", n, ok, out)
	}
	if got := u.InputTokensDetails.CacheWriteTokensByTTL(); !reflect.DeepEqual(got, map[string]int{"5m": 100, "1h": 2000}) {
		t.Errorf("decoded split = %v", got)
	}
	d.SetCacheWriteTokens(7)
	if n, _ := d.CacheWriteTokens(); n != 7 || d.CacheWriteTokensByTTL() != nil {
		t.Errorf("count alone: %#v", d.Extra)
	}
}

func TestInfoRoundTrip(t *testing.T) {
	raw := `{"source":"anthropic","context_window":200000,"max_output_tokens":64000,"reasoning":true,` +
		`"efforts":["none","low","medium","high","xhigh","max"],"default_effort":"high","input_modalities":["text","image"],"x:a":1}`
	var info Info
	if err := json.Unmarshal([]byte(raw), &info); err != nil {
		t.Fatal(err)
	}
	if info.Reasoning == nil || !*info.Reasoning || info.Efforts[5] != EffortMax || info.ContextWindow != 200000 {
		t.Errorf("info = %#v", info)
	}
	out, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	assertSameJSON(t, []byte(raw), out)
	// Absent members stay absent: the source did not say.
	out, err = json.Marshal(Info{Source: "ollama"})
	if err != nil {
		t.Fatal(err)
	}
	assertSameJSON(t, []byte(`{"source":"ollama"}`), out)
}

func TestContextLengthExceeded(t *testing.T) {
	err := ContextLengthExceeded("prompt is too long")
	if !IsContextLengthExceeded(err) || !IsInvalidRequest(err) || err.HTTPStatus() != 400 {
		t.Errorf("err = %v", err)
	}
	if !IsContextLengthExceeded(fmt.Errorf("wrapped: %w", err)) {
		t.Error("wrapped error not matched")
	}
	upstream := ErrorPayload{Type: "invalid_request_error", Code: CodeContextLengthExceeded, Message: "m"}.Err(400)
	if !IsContextLengthExceeded(upstream) {
		t.Error("OpenAI-typed upstream error not matched")
	}
	if IsContextLengthExceeded(InvalidRequest(CodeInvalidValue, "m", "")) {
		t.Error("other invalid_request matched")
	}
}
