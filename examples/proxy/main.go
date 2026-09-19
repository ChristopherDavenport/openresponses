// Command proxy serves a local Open Responses endpoint on :8000 that
// forwards every request to an upstream server, Ollama by default. The
// Client composes as an Adapter, so the proxy is a Handler over a Client
// and nothing else; the handler adds what the upstream may lack, the
// WebSocket transport and previous_response_id continuation backed by a
// local store. examples/websocket therefore runs against Ollama through
// this proxy.
//
// Environment: OPENRESPONSES_UPSTREAM_URL (default
// http://localhost:11434/v1), OPENRESPONSES_UPSTREAM_API_KEY.
package main

import (
	"cmp"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/ChristopherDavenport/openresponses"
	"github.com/ChristopherDavenport/openresponses/websocket"
)

func main() {
	upstream := openresponses.NewClient(
		cmp.Or(os.Getenv("OPENRESPONSES_UPSTREAM_URL"), "http://localhost:11434/v1"),
		openresponses.WithAPIKey(os.Getenv("OPENRESPONSES_UPSTREAM_API_KEY")),
	)
	srv := &http.Server{
		Addr: ":8000",
		Handler: websocket.Handler(openresponses.NewHandler(upstream.AsAdapter(),
			openresponses.WithResponseStore(openresponses.NewMemoryStore(256)),
		)),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Println("proxying", srv.Addr, "to", upstream.BaseURL())
	log.Fatal(srv.ListenAndServe())
}
