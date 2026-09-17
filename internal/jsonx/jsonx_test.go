package jsonx

import (
	"encoding/json"
	"testing"
)

func TestPeekType(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"present", `{"type":"message","x":1}`, "message", false},
		{"absent", `{"x":1}`, "", false},
		{"null", `{"type":null}`, "", false},
		{"invalid", `{"type":`, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := PeekType([]byte(tt.in))
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestInjectType(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"empty object", `{}`, `{"type":"t"}`},
		{"with members", `{"a":1}`, `{"type":"t","a":1}`},
		{"whitespace", ` {"a":1} `, `{"type":"t","a":1}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := InjectType("t", []byte(tt.body))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Errorf("got %s, want %s", got, tt.want)
			}
		})
	}
	if _, err := InjectType("t", []byte(`[1]`)); err == nil {
		t.Error("expected error for non-object")
	}
}

func TestExtraRoundTrip(t *testing.T) {
	type inner struct {
		B string `json:"b"`
	}
	type outer struct {
		inner
		A      int    `json:"a"`
		Hidden string `json:"-"`
		Plain  string
	}
	var v outer
	extra, absent, err := UnmarshalExtra([]byte(`{"a":1,"b":"x","Plain":"p","z":[1,2],"acme:y":{"k":true}}`), &v)
	if err != nil {
		t.Fatal(err)
	}
	if v.A != 1 || v.B != "x" || v.Plain != "p" {
		t.Errorf("decoded %+v", v)
	}
	if len(extra) != 2 || extra["z"] == nil || extra["acme:y"] == nil {
		t.Errorf("extra = %v", extra)
	}
	if absent != nil {
		t.Errorf("absent = %v, want nil", absent)
	}
	out, err := MarshalWithExtra(v, extra, nil)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"a", "b", "Plain", "z", "acme:y"} {
		if _, ok := back[k]; !ok {
			t.Errorf("missing key %q in %s", k, out)
		}
	}
	// Known keys win over extra.
	out, err = MarshalWithExtra(v, map[string]any{"a": 99}, nil)
	if err != nil {
		t.Fatal(err)
	}
	back = nil
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatal(err)
	}
	if back["a"] != float64(1) {
		t.Errorf("known key overridden: %s", out)
	}
}

func TestAbsentAndZeroFields(t *testing.T) {
	type shape struct {
		A int     `json:"a"`
		B string  `json:"b"`
		C *int    `json:"c"`
		D []int   `json:"d"`
		E float64 `json:"e"`
	}
	var v shape
	_, absent, err := UnmarshalExtra([]byte(`{"a":1,"e":0}`), &v)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"b", "c", "d"} {
		if !absent[k] {
			t.Errorf("absent missing %q: %v", k, absent)
		}
	}
	if absent["a"] || absent["e"] {
		t.Errorf("present keys reported absent: %v", absent)
	}
	// Setting a field after decode keeps it; untouched absent fields are
	// dropped again.
	v.B = "set"
	omit := ZeroFields(&v, absent)
	if omit["b"] || !omit["c"] || !omit["d"] {
		t.Errorf("omit = %v", omit)
	}
	out, err := MarshalWithExtra(v, nil, omit)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if err := json.Unmarshal(out, &back); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"a", "b", "e"} {
		if _, ok := back[k]; !ok {
			t.Errorf("missing %q in %s", k, out)
		}
	}
	for _, k := range []string{"c", "d"} {
		if _, ok := back[k]; ok {
			t.Errorf("unexpected %q in %s", k, out)
		}
	}
}
