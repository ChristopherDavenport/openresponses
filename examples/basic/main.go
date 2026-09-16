// Command basic sends one request and prints the response text.
//
// Environment: OPENRESPONSES_BASE_URL (default https://api.openai.com/v1),
// OPENRESPONSES_API_KEY, OPENRESPONSES_MODEL (default gpt-5).
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/ChristopherDavenport/openresponses"
)

func main() {
	client := openresponses.NewClient(env("OPENRESPONSES_BASE_URL", "https://api.openai.com/v1"),
		openresponses.WithAPIKey(os.Getenv("OPENRESPONSES_API_KEY")))

	resp, err := client.Create(context.Background(), openresponses.Request{
		Model: env("OPENRESPONSES_MODEL", "gpt-5"),
		Input: openresponses.Items{openresponses.UserText("Say hello in exactly three words.")},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(resp.OutputText())
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
