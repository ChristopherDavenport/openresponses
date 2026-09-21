package sse

import (
	"io"
	"strings"
	"testing"
)

func TestSSEScanner(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []Frame
	}{
		{"lf", "event: a\ndata: {\"x\":1}\n\nevent: b\ndata: 2\n\n", []Frame{{"a", `{"x":1}`}, {"b", "2"}}},
		{"crlf", "event: a\r\ndata: 1\r\n\r\ndata: 2\r\n\r\n", []Frame{{"a", "1"}, {"", "2"}}},
		{"multiline data", "data: line1\ndata: line2\n\n", []Frame{{"", "line1\nline2"}}},
		{"comments and ids", ": keepalive\nid: 5\nretry: 100\ndata: 1\n\n: another\n\n", []Frame{{"", "1"}}},
		{"no space after colon", "event:a\ndata:1\n\n", []Frame{{"a", "1"}}},
		{"missing trailing blank line", "data: 1\n\ndata: 2", []Frame{{"", "1"}, {"", "2"}}},
		{"empty", "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sc := NewScanner(strings.NewReader(tt.input))
			var got []Frame
			for {
				f, ok := sc.Next()
				if !ok {
					break
				}
				got = append(got, f)
			}
			if err := sc.Err(); err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("got %d frames %+v, want %+v", len(got), got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("frame %d = %+v, want %+v", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestSSEScannerTooLong(t *testing.T) {
	sc := NewScanner(io.MultiReader(strings.NewReader("data: "), strings.NewReader(strings.Repeat("x", MaxFrame+1))))
	if _, ok := sc.Next(); ok {
		t.Fatal("expected failure")
	}
	if sc.Err() == nil {
		t.Fatal("expected error")
	}
}

func TestSSEScannerMixedLineEndings(t *testing.T) {
	in := "event: a\r\ndata: 1\r\n\r\nevent: b\ndata: 2\n\nevent: c\r\ndata: 3\r\n\r\n"
	sc := NewScanner(strings.NewReader(in))
	var got []string
	for {
		f, ok := sc.Next()
		if !ok {
			break
		}
		got = append(got, f.Event+"="+f.Data)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if want := []string{"a=1", "b=2", "c=3"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("got %v, want %v", got, want)
	}
}
