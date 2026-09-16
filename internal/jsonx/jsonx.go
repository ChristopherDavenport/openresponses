// Package jsonx holds small encoding/json helpers shared by the
// openresponses package: discriminator peeking, type injection and
// passthrough of unknown top-level keys.
package jsonx

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
)

// PeekType returns the string value of the top-level "type" key in data.
// It returns "" (and no error) when the key is absent or null.
func PeekType(data []byte) (string, error) {
	var probe struct {
		Type *string `json:"type"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return "", err
	}
	if probe.Type == nil {
		return "", nil
	}
	return *probe.Type, nil
}

// HasKey reports whether the top-level object in data contains key.
func HasKey(data []byte, key string) bool {
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(data, &probe); err != nil {
		return false
	}
	_, ok := probe[key]
	return ok
}

// MarshalTyped marshals v and injects "type": typ as the first member of
// the resulting object. v must marshal to a JSON object.
func MarshalTyped(typ string, v any) ([]byte, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return InjectType(typ, body)
}

// InjectType prepends "type": typ to the JSON object in body. If body
// already contains a type key it is left untouched.
func InjectType(typ string, body []byte) ([]byte, error) {
	body = bytes.TrimSpace(body)
	if len(body) < 2 || body[0] != '{' || body[len(body)-1] != '}' {
		return nil, fmt.Errorf("jsonx: cannot inject type into non-object %q", truncate(body))
	}
	if HasKey(body, "type") {
		return body, nil
	}
	typJSON, err := json.Marshal(typ)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(body)+len(typJSON)+9)
	out = append(out, '{', '"', 't', 'y', 'p', 'e', '"', ':')
	out = append(out, typJSON...)
	if len(body) > 2 {
		out = append(out, ',')
		out = append(out, body[1:]...)
	} else {
		out = append(out, '}')
	}
	return out, nil
}

// MarshalWithExtra marshals v (which must produce a JSON object) and
// merges extra into the result. Keys already present in v win.
func MarshalWithExtra(v any, extra map[string]any) ([]byte, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if len(extra) == 0 {
		return body, nil
	}
	var merged map[string]json.RawMessage
	if err := json.Unmarshal(body, &merged); err != nil {
		return nil, fmt.Errorf("jsonx: re-decode for extra merge: %w", err)
	}
	for k, val := range extra {
		if _, exists := merged[k]; exists {
			continue
		}
		raw, err := json.Marshal(val)
		if err != nil {
			return nil, fmt.Errorf("jsonx: marshal extra %q: %w", k, err)
		}
		merged[k] = raw
	}
	return json.Marshal(merged)
}

// UnmarshalExtra decodes data into v and returns any top-level keys not
// declared by v's struct tags. The returned map is nil when there are no
// unknown keys.
func UnmarshalExtra(data []byte, v any) (map[string]any, error) {
	if err := json.Unmarshal(data, v); err != nil {
		return nil, err
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, err
	}
	known := knownKeys(reflect.TypeOf(v))
	var extra map[string]any
	for k, raw := range all {
		if known[k] {
			continue
		}
		var val any
		if err := json.Unmarshal(raw, &val); err != nil {
			return nil, fmt.Errorf("jsonx: decode extra %q: %w", k, err)
		}
		if extra == nil {
			extra = make(map[string]any)
		}
		extra[k] = val
	}
	return extra, nil
}

var (
	knownMu    sync.Mutex
	knownCache = map[reflect.Type]map[string]bool{}
)

// knownKeys returns the set of JSON member names produced by t's exported
// fields, following embedded structs. Results are cached per type.
func knownKeys(t reflect.Type) map[string]bool {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	knownMu.Lock()
	defer knownMu.Unlock()
	if keys, ok := knownCache[t]; ok {
		return keys
	}
	keys := map[string]bool{}
	collectKeys(t, keys)
	knownCache[t] = keys
	return keys
}

func collectKeys(t reflect.Type, keys map[string]bool) {
	if t.Kind() != reflect.Struct {
		return
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		name, _, _ := strings.Cut(tag, ",")
		if f.Anonymous && name == "" {
			ft := f.Type
			for ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			collectKeys(ft, keys)
			continue
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		keys[name] = true
	}
}

func truncate(b []byte) string {
	const max = 40
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "..."
}
