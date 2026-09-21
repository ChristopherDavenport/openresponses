package openresponses

import (
	"bytes"
	"io"
	"net/http"

	"github.com/ChristopherDavenport/openresponses/internal/sse"
)

// The reader lives in internal/sse, shared with the Chat Completions
// adapter; these names keep the root package's call sites unchanged.
type sseFrame = sse.Frame

const (
	sseDone     = sse.Done
	maxSSEFrame = sse.MaxFrame
)

func newSSEScanner(r io.Reader) *sse.Scanner { return sse.NewScanner(r) }

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
