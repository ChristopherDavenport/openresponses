// Command server exposes a tiny adapter as an Open Responses endpoint.
// It answers every request with the reversed user text, over plain JSON,
// SSE and WebSocket.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/ChristopherDavenport/openresponses"
)

type reverser struct {
	openresponses.UnsupportedCompaction
}

func (r reverser) Create(ctx context.Context, req openresponses.Request) (*openresponses.Response, error) {
	return openresponses.CollectStream(ctx, r, req)
}

func (reverser) CreateStream(_ context.Context, req openresponses.Request, sink openresponses.EventSink) error {
	em := openresponses.NewEmitter(sink, openresponses.NewResponse(req))
	msg, err := em.Message(openresponses.PhaseFinalAnswer)
	if err != nil {
		return err
	}
	for _, r := range reverse(lastUserText(req.Input)) {
		if err := msg.Text(string(r)); err != nil {
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

func reverse(s string) string {
	r := []rune(s)
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		r[i], r[j] = r[j], r[i]
	}
	return string(r)
}

func main() {
	mux := http.NewServeMux()
	mux.Handle("/v1/", openresponses.NewHandler(reverser{}))
	srv := &http.Server{
		Addr:              ":8000",
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	slog.Info("listening", "addr", srv.Addr)
	if err := srv.ListenAndServe(); err != nil {
		slog.Error("serve", "err", err)
		os.Exit(1)
	}
}
