// Command streaming prints output text as it arrives over SSE.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/christopherdavenport/openresponses"
)

func main() {
	client := openresponses.NewClient(env("OPENRESPONSES_BASE_URL", "https://api.openai.com/v1"),
		openresponses.WithAPIKey(os.Getenv("OPENRESPONSES_API_KEY")))

	stream, err := client.CreateStream(context.Background(), openresponses.Request{
		Model: env("OPENRESPONSES_MODEL", "gpt-5"),
		Input: openresponses.Input{openresponses.UserText("Count from 1 to 5.")},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer stream.Close()

	for ev := range stream.Events() {
		switch e := ev.(type) {
		case *openresponses.OutputTextDeltaEvent:
			fmt.Print(e.Delta)
		case *openresponses.ErrorEvent:
			fmt.Fprintln(os.Stderr, "\nerror:", e.Error.Message)
		}
	}
	fmt.Println()
	if err := stream.Err(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("status:", stream.Response().Status)
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
