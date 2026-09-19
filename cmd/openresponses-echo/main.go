// Command openresponses-echo serves the echo adapter so the official
// compliance suite can run against this package.
//
// Usage:
//
//	openresponses-echo -addr :8000
//	bun run test:compliance --base-url http://localhost:8000/v1 --api-key test
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ChristopherDavenport/openresponses"
	"github.com/ChristopherDavenport/openresponses/echo"
	"github.com/ChristopherDavenport/openresponses/websocket"
)

func main() {
	addr := flag.String("addr", ":8000", "listen address")
	lifetime := flag.Duration("ws-lifetime", 60*time.Minute, "WebSocket connection lifetime")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	handler := websocket.Handler(openresponses.NewHandler(&echo.Adapter{}),
		websocket.WithLifetime(*lifetime),
		websocket.WithOrigins("*"),
	)
	srv := &http.Server{
		Addr:              *addr,
		Handler:           logRequests(logger, handler),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		logger.Info("listening", "addr", *addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("serve", "err", err)
			os.Exit(1)
		}
	}()
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("shutdown", "err", err)
	}
}

func logRequests(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		logger.Info("request", "method", r.Method, "path", r.URL.Path, "duration", time.Since(start))
	})
}
