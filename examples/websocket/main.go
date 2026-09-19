// Command websocket runs two turns on one WebSocket connection, using
// previous_response_id to continue a store:false conversation.
//
// Environment: OPENRESPONSES_BASE_URL (default https://api.openai.com/v1),
// OPENRESPONSES_API_KEY, OPENRESPONSES_MODEL (default gpt-5).
package main

import (
	"cmp"
	"context"
	"fmt"
	"log"
	"os"

	"github.com/ChristopherDavenport/openresponses"
	"github.com/ChristopherDavenport/openresponses/websocket"
)

func main() {
	if err := run(context.Background()); err != nil {
		log.Fatal(err)
	}
}

func run(ctx context.Context) error {
	client := openresponses.NewClient(
		cmp.Or(os.Getenv("OPENRESPONSES_BASE_URL"), "https://api.openai.com/v1"),
		openresponses.WithAPIKey(os.Getenv("OPENRESPONSES_API_KEY")),
	)
	conn, err := websocket.Dial(ctx, client)
	if err != nil {
		return err
	}
	defer conn.Close()

	model := cmp.Or(os.Getenv("OPENRESPONSES_MODEL"), "gpt-5")
	first, err := conn.Turn(ctx, openresponses.Request{
		Model: model,
		Store: new(bool), // store:false; the connection remembers the turn
		Input: openresponses.Items{openresponses.UserText("Remember the code word: cobalt. Reply with OK.")},
	})
	if err != nil {
		return err
	}
	fmt.Println("turn 1:", first.OutputText())

	second, err := conn.Turn(ctx, openresponses.Request{
		Model:              model,
		Store:              new(bool),
		PreviousResponseID: first.ID,
		Input:              openresponses.Items{openresponses.UserText("What is the code word?")},
	})
	if err != nil {
		return err
	}
	fmt.Println("turn 2:", second.OutputText())
	return nil
}
