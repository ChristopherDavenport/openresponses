// Command websocket runs two turns on one WebSocket connection, using
// previous_response_id to continue a store:false conversation.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/ChristopherDavenport/openresponses"
)

func main() {
	ctx := context.Background()
	client := openresponses.NewClient(env("OPENRESPONSES_BASE_URL", "https://api.openai.com/v1"),
		openresponses.WithAPIKey(os.Getenv("OPENRESPONSES_API_KEY")))

	conn, err := client.Dial(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer conn.Close()

	store := false
	model := env("OPENRESPONSES_MODEL", "gpt-5")
	first, err := conn.Turn(ctx, openresponses.Request{
		Model: model,
		Store: &store,
		Input: openresponses.Items{openresponses.UserText("Remember the code word: cobalt. Reply with OK.")},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("turn 1:", first.OutputText())

	second, err := conn.Turn(ctx, openresponses.Request{
		Model:              model,
		Store:              &store,
		PreviousResponseID: first.ID,
		Input:              openresponses.Items{openresponses.UserText("What is the code word?")},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("turn 2:", second.OutputText())
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
