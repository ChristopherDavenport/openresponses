// Command tools runs one round of function calling: the model asks for a
// tool, the program answers, and the model replies.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/christopherdavenport/openresponses"
)

func main() {
	ctx := context.Background()
	client := openresponses.NewClient(env("OPENRESPONSES_BASE_URL", "https://api.openai.com/v1"),
		openresponses.WithAPIKey(os.Getenv("OPENRESPONSES_API_KEY")))

	weather := openresponses.NewFunctionTool("get_weather", "Get the current weather for a location",
		json.RawMessage(`{"type":"object","properties":{"location":{"type":"string"}},"required":["location"]}`))

	req := openresponses.Request{
		Model: env("OPENRESPONSES_MODEL", "gpt-5"),
		Input: openresponses.Input{openresponses.UserText("What's the weather like in San Francisco?")},
		Tools: openresponses.Tools{weather},
	}
	resp, err := client.Create(ctx, req)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	// Feed every call's result back and ask again.
	for _, call := range resp.FunctionCalls() {
		var args struct {
			Location string `json:"location"`
		}
		if err := call.UnmarshalArguments(&args); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		req.Input = append(req.Input, call, openresponses.NewFunctionCallOutput(call.CallID,
			fmt.Sprintf("It is 18C and foggy in %s.", args.Location)))
	}
	if len(resp.FunctionCalls()) > 0 {
		if resp, err = client.Create(ctx, req); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	fmt.Println(resp.OutputText())
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
