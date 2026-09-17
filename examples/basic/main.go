// Command basic sends one request and prints the response text.
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
	resp, err := client.Create(ctx, openresponses.Request{
		Model: cmp.Or(os.Getenv("OPENRESPONSES_MODEL"), "gpt-5"),
		Input: openresponses.Items{openresponses.UserText("Say hello in exactly three words.")},
	})
	if err != nil {
		return err
	}
	fmt.Println(resp.OutputText())
	return nil
}
