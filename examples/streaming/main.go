// Command streaming prints output text as it arrives over SSE.
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
	stream, err := client.CreateStream(ctx, openresponses.Request{
		Model: cmp.Or(os.Getenv("OPENRESPONSES_MODEL"), "gpt-5"),
		Input: openresponses.Items{openresponses.UserText("Count from 1 to 5.")},
	})
	if err != nil {
		return err
	}
	defer stream.Close()

	for ev := range stream.Events() {
		if delta, ok := ev.(*openresponses.OutputTextDeltaEvent); ok {
			fmt.Print(delta.Delta)
		}
	}
	fmt.Println()

	// Wait reports a transport error or a failed response as an error and
	// returns the response folded from the events.
	resp, err := stream.Wait()
	if err != nil {
		return err
	}
	fmt.Println("status:", resp.Status)
	return nil
}
