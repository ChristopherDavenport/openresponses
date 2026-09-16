package openresponses

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func FuzzDecodeEvent(f *testing.F) {
	data, err := os.ReadFile("testdata/golden/events.sse")
	if err != nil {
		f.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if rest, ok := strings.CutPrefix(line, "data: "); ok && rest != sseDone {
			f.Add([]byte(rest))
		}
	}
	f.Add([]byte(`{"error":{"code":"x","message":"y"}}`))
	f.Add([]byte(`{"type":"error","status":404,"error":{"code":"x","message":"y"}}`))
	f.Fuzz(func(t *testing.T, in []byte) {
		ev, err := DecodeEvent(in)
		if err != nil {
			return
		}
		out, err := json.Marshal(ev)
		if err != nil {
			t.Fatalf("re-marshal %T: %v", ev, err)
		}
		if _, err := DecodeEvent(out); err != nil {
			t.Fatalf("re-decode %s: %v", out, err)
		}
	})
}

func FuzzUnmarshalItem(f *testing.F) {
	data, err := os.ReadFile("testdata/golden/request.json")
	if err != nil {
		f.Fatal(err)
	}
	var req Request
	if err := json.Unmarshal(data, &req); err != nil {
		f.Fatal(err)
	}
	for _, item := range req.Input {
		b, _ := json.Marshal(item)
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, in []byte) {
		item, err := UnmarshalItem(in)
		if err != nil {
			return
		}
		out, err := json.Marshal(item)
		if err != nil {
			t.Fatalf("re-marshal %T: %v", item, err)
		}
		if _, err := UnmarshalItem(out); err != nil {
			t.Fatalf("re-decode %s: %v", out, err)
		}
	})
}
