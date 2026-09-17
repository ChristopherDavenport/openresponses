package openresponses

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
)

// sseDone is the sentinel data payload that ends an SSE stream.
const sseDone = "[DONE]"

// maxSSEFrame caps a single SSE frame. output_item.done carries whole
// items, so this is generous.
const maxSSEFrame = 16 << 20

// sseFrame is one decoded Server-Sent Events frame.
type sseFrame struct {
	Event string
	Data  string
}

// sseScanner splits an SSE byte stream into frames. It handles LF and
// CRLF line endings, joins multi-line data with "\n" and ignores
// comments, id and retry fields.
type sseScanner struct {
	sc *bufio.Scanner
}

func newSSEScanner(r io.Reader) *sseScanner {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), maxSSEFrame)
	sc.Split(scanSSEFrames)
	return &sseScanner{sc: sc}
}

// Next returns the next frame that carries data. It returns false at EOF
// or on error; check Err.
func (s *sseScanner) Next() (sseFrame, bool) {
	for s.sc.Scan() {
		frame, ok := parseSSEFrame(s.sc.Bytes())
		if ok {
			return frame, true
		}
	}
	return sseFrame{}, false
}

// Err returns the underlying scanner error, if any.
func (s *sseScanner) Err() error {
	err := s.sc.Err()
	if errors.Is(err, bufio.ErrTooLong) {
		return errors.New("openresponses: SSE frame exceeds 16 MiB")
	}
	return err
}

// scanSSEFrames is a bufio.SplitFunc that yields one frame per blank
// line. A trailing frame without a terminating blank line is yielded at
// EOF.
func scanSSEFrames(data []byte, atEOF bool) (advance int, token []byte, err error) {
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

// parseSSEFrame decodes the lines of one frame. It reports false when the
// frame carries no data field (comments, id-only or retry-only frames).
func parseSSEFrame(raw []byte) (sseFrame, bool) {
	var frame sseFrame
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
		return sseFrame{}, false
	}
	frame.Data = strings.Join(dataLines, "\n")
	return frame, true
}

// sseWriter frames events for an HTTP response and flushes after each
// one.
type sseWriter struct {
	w       io.Writer
	flusher http.Flusher
}

// newSSEWriter sets the streaming headers and returns a writer. Headers
// are written when the first frame is flushed, or immediately by the
// caller via w.WriteHeader.
func newSSEWriter(w http.ResponseWriter) *sseWriter {
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	flusher, _ := w.(http.Flusher)
	return &sseWriter{w: w, flusher: flusher}
}

// WriteEvent writes one "event:" + "data:" frame and flushes.
func (s *sseWriter) WriteEvent(event string, data []byte) error {
	var buf bytes.Buffer
	buf.Grow(len(event) + len(data) + 16)
	buf.WriteString("event: ")
	buf.WriteString(event)
	buf.WriteString("\ndata: ")
	buf.Write(data)
	buf.WriteString("\n\n")
	if _, err := s.w.Write(buf.Bytes()); err != nil {
		return err
	}
	s.flush()
	return nil
}

// WriteDone writes the [DONE] sentinel and flushes.
func (s *sseWriter) WriteDone() error {
	if _, err := io.WriteString(s.w, "data: "+sseDone+"\n\n"); err != nil {
		return err
	}
	s.flush()
	return nil
}

func (s *sseWriter) flush() {
	if s.flusher != nil {
		s.flusher.Flush()
	}
}
