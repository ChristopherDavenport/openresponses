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

	"github.com/christopherdavenport/openresponses"
)

type reverser struct {
	openresponses.UnsupportedCompaction
}

func (r reverser) Create(ctx context.Context, req openresponses.Request) (*openresponses.Response, error) {
	return openresponses.CollectStream(ctx, r, req)
}

func (reverser) CreateStream(_ context.Context, req openresponses.Request, sink openresponses.EventSink) error {
	resp := openresponses.NewResponse(req)
	resp.ID = openresponses.NewID("resp")
	if err := sink.Send(&openresponses.ResponseCreatedEvent{Response: resp}); err != nil {
		return err
	}

	text := reverse(lastUserText(req.Input))
	msg := &openresponses.Message{
		ID:     openresponses.NewID("msg"),
		Status: openresponses.StatusInProgress,
		Role:   openresponses.RoleAssistant,
		Phase:  openresponses.PhaseFinalAnswer,
	}
	if err := sink.Send(&openresponses.OutputItemAddedEvent{Item: msg}); err != nil {
		return err
	}
	if err := sink.Send(&openresponses.ContentPartAddedEvent{ItemID: msg.ID, Part: &openresponses.OutputText{}}); err != nil {
		return err
	}
	for _, r := range text {
		if err := sink.Send(&openresponses.OutputTextDeltaEvent{ItemID: msg.ID, Delta: string(r)}); err != nil {
			return err
		}
	}
	part := &openresponses.OutputText{Text: text}
	if err := sink.Send(&openresponses.OutputTextDoneEvent{ItemID: msg.ID, Text: text}); err != nil {
		return err
	}
	if err := sink.Send(&openresponses.ContentPartDoneEvent{ItemID: msg.ID, Part: part}); err != nil {
		return err
	}
	msg.Content = openresponses.Contents{part}
	msg.Status = openresponses.StatusCompleted
	if err := sink.Send(&openresponses.OutputItemDoneEvent{Item: msg}); err != nil {
		return err
	}

	resp.Output = openresponses.Items{msg}
	resp.Status = openresponses.ResponseStatusCompleted
	now := time.Now().Unix()
	resp.CompletedAt = &now
	resp.Usage = &openresponses.Usage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2}
	return sink.Send(&openresponses.ResponseCompletedEvent{Response: resp})
}

func lastUserText(items openresponses.Input) string {
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
