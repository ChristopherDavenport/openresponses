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
// the resulting object. v must marshal to a JSON object without a type
// member of its own.
func MarshalTyped(typ string, v any) ([]byte, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return InjectType(typ, body)
}

// InjectType prepends "type": typ to the JSON object in body. The body is
// not inspected for an existing type member; callers own that invariant,
// which keeps this off the hot path of every encoded item and event.
func InjectType(typ string, body []byte) ([]byte, error) {
	body = bytes.TrimSpace(body)
	if len(body) < 2 || body[0] != '{' || body[len(body)-1] != '}' {
		return nil, fmt.Errorf("jsonx: cannot inject type into non-object %q", truncate(body))
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

// MarshalWithExtra marshals v (which must produce a JSON object), merges
// extra into the result and removes the keys in omit. Keys already
// present in v win over extra.
func MarshalWithExtra(v any, extra map[string]any, omit map[string]bool) ([]byte, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	if len(extra) == 0 && len(omit) == 0 {
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
	for k := range omit {
		delete(merged, k)
	}
	return json.Marshal(merged)
}

// UnmarshalExtra decodes data into v and returns the top-level keys not
// declared by v's struct tags (extra, nil when there are none) and the
// declared keys that data did not carry (absent, nil when there are
// none). absent lets a decoded value re-encode without inventing members
// its source never sent.
func UnmarshalExtra(data []byte, v any) (extra map[string]any, absent map[string]bool, err error) {
	if err := json.Unmarshal(data, v); err != nil {
		return nil, nil, err
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(data, &all); err != nil {
		return nil, nil, err
	}
	info := structInfoOf(reflect.TypeOf(v))
	for k, raw := range all {
		if info.keys[k] {
			continue
		}
		var val any
		if err := json.Unmarshal(raw, &val); err != nil {
			return nil, nil, fmt.Errorf("jsonx: decode extra %q: %w", k, err)
		}
		if extra == nil {
			extra = make(map[string]any)
		}
		extra[k] = val
	}
	for k := range info.keys {
		if _, present := all[k]; present {
			continue
		}
		if absent == nil {
			absent = make(map[string]bool)
		}
		absent[k] = true
	}
	return extra, absent, nil
}

// ZeroFields returns the subset of keys whose field in v (a struct or a
// pointer to one) holds its zero value. Keys that name no field are
// ignored.
func ZeroFields(v any, keys map[string]bool) map[string]bool {
	if len(keys) == 0 {
		return nil
	}
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil
		}
		rv = rv.Elem()
	}
	info := structInfoOf(rv.Type())
	var out map[string]bool
	for k := range keys {
		index, ok := info.index[k]
		if !ok {
			continue
		}
		if rv.FieldByIndex(index).IsZero() {
			if out == nil {
				out = make(map[string]bool)
			}
			out[k] = true
		}
	}
	return out
}

// structInfo is the JSON member names produced by a struct type and the
// field index behind each one.
type structInfo struct {
	keys  map[string]bool
	index map[string][]int
}

var (
	infoMu    sync.Mutex
	infoCache = map[reflect.Type]*structInfo{}
)

// structInfoOf returns the cached member set of t, following embedded
// structs.
func structInfoOf(t reflect.Type) *structInfo {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	infoMu.Lock()
	defer infoMu.Unlock()
	if info, ok := infoCache[t]; ok {
		return info
	}
	info := &structInfo{keys: map[string]bool{}, index: map[string][]int{}}
	collectFields(t, nil, info)
	infoCache[t] = info
	return info
}

func collectFields(t reflect.Type, prefix []int, info *structInfo) {
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
		index := append(append([]int(nil), prefix...), i)
		if f.Anonymous && name == "" {
			ft := f.Type
			for ft.Kind() == reflect.Pointer {
				ft = ft.Elem()
			}
			collectFields(ft, index, info)
			continue
		}
		if !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		info.keys[name] = true
		info.index[name] = index
	}
}

func truncate(b []byte) string {
	const max = 40
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "..."
}
