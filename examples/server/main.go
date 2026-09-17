// Command server exposes a small adapter as an Open Responses endpoint
// on :8000. It shouts the last user message back, over plain JSON, SSE
// and WebSocket alike; the handler supplies the transports.
package main

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/ChristopherDavenport/openresponses"
)

// shouter is the whole backend: a streaming implementation, Create
// derived from it, and no compaction.
type shouter struct {
	openresponses.UnsupportedCompaction
}

func (s shouter) Create(ctx context.Context, req openresponses.Request) (*openresponses.Response, error) {
	return openresponses.CollectStream(ctx, s, req)
}

func (shouter) CreateStream(_ context.Context, req openresponses.Request, sink openresponses.EventSink) error {
	em := openresponses.NewEmitter(sink, openresponses.NewResponse(req))
	msg, err := em.Message(openresponses.PhaseFinalAnswer)
	if err != nil {
		return err
	}
	for _, word := range strings.Fields(strings.ToUpper(lastUserText(req.Input))) {
		if err := msg.Text(word + " "); err != nil {
			return err
		}
	}
	em.Response().Usage = &openresponses.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}
	return em.Complete()
}

func lastUserText(items openresponses.Items) string {
	for i := len(items) - 1; i >= 0; i-- {
		if m, ok := items[i].(*openresponses.Message); ok && m.Role == openresponses.RoleUser {
			return m.Text()
		}
	}
	return ""
}

func main() {
	srv := &http.Server{
		Addr:              ":8000",
		Handler:           openresponses.NewHandler(shouter{}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Println("listening on", srv.Addr)
	log.Fatal(srv.ListenAndServe())
}
