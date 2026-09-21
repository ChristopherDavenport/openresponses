// Package sse reads Server-Sent Events streams: the transport of Open
// Responses streaming and of the Chat Completions protocol alike.
package sse

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
)

// Done is the sentinel data payload that ends a stream.
const Done = "[DONE]"

// MaxFrame caps a single frame. Open Responses' output_item.done carries
// whole items, so this is generous.
const MaxFrame = 16 << 20

// ErrFrameTooLong reports a frame over MaxFrame.
var ErrFrameTooLong = errors.New("sse: frame exceeds 16 MiB")

// Frame is one decoded event.
type Frame struct {
	Event string
	Data  string
}

// Scanner splits a byte stream into frames. It handles LF and CRLF line
// endings, joins multi-line data with "\n" and ignores comments, id and
// retry fields.
type Scanner struct {
	sc *bufio.Scanner
}

// NewScanner returns a scanner over r.
func NewScanner(r io.Reader) *Scanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), MaxFrame)
	sc.Split(splitFrames)
	return &Scanner{sc: sc}
}

// Next returns the next frame that carries data. It returns false at EOF
// or on error; check Err.
func (s *Scanner) Next() (Frame, bool) {
	for s.sc.Scan() {
		frame, ok := parseFrame(s.sc.Bytes())
		if ok {
			return frame, true
		}
	}
	return Frame{}, false
}

// Err returns the underlying scanner error, if any.
func (s *Scanner) Err() error {
	err := s.sc.Err()
	if errors.Is(err, bufio.ErrTooLong) {
		return ErrFrameTooLong
	}
	return err
}

// splitFrames is a bufio.SplitFunc that yields one frame per blank line.
// A trailing frame without a terminating blank line is yielded at EOF.
func splitFrames(data []byte, atEOF bool) (advance int, token []byte, err error) {
	if atEOF && len(data) == 0 {
		return 0, nil, nil
	}
	// Whichever terminator comes first ends the frame, so a stream that
	// mixes line endings still splits at every blank line.
	lf := bytes.Index(data, []byte("\n\n"))
	crlf := bytes.Index(data, []byte("\r\n\r\n"))
	switch {
	case lf >= 0 && (crlf < 0 || lf < crlf):
		return lf + 2, data[:lf], nil
	case crlf >= 0:
		return crlf + 4, data[:crlf], nil
	}
	if atEOF {
		return len(data), data, nil
	}
	return 0, nil, nil
}

// parseFrame decodes the lines of one frame. It reports false when the
// frame carries no data field (comments, id-only or retry-only frames).
func parseFrame(raw []byte) (Frame, bool) {
	var frame Frame
	var dataLines []string
	for _, line := range bytes.Split(raw, []byte("\n")) {
		line = bytes.TrimSuffix(line, []byte("\r"))
		if len(line) == 0 || line[0] == ':' {
			continue
		}
		field, value, _ := bytes.Cut(line, []byte(":"))
		value = bytes.TrimPrefix(value, []byte(" "))
		switch string(field) {
		case "event":
			frame.Event = string(value)
		case "data":
			dataLines = append(dataLines, string(value))
		}
	}
	if dataLines == nil {
		return Frame{}, false
	}
	frame.Data = strings.Join(dataLines, "\n")
	return frame, true
}
