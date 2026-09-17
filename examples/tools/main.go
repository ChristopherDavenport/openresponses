// Command tools runs a function-calling loop: while the model asks for a
// tool, the program answers and asks again; the model's final text is
// printed.
//
// Environment: OPENRESPONSES_BASE_URL (default https://api.openai.com/v1),
// OPENRESPONSES_API_KEY, OPENRESPONSES_MODEL (default gpt-5).
package main

import (
	"cmp"
	"context"
	"encoding/json"
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
	req := openresponses.Request{
		Model: cmp.Or(os.Getenv("OPENRESPONSES_MODEL"), "gpt-5"),
		Input: openresponses.Items{openresponses.UserText("What's the weather like in San Francisco?")},
		Tools: openresponses.Tools{
			openresponses.NewFunctionTool("get_weather", "Get the current weather for a location",
				json.RawMessage(`{"type":"object","properties":{"location":{"type":"string"}},"required":["location"]}`)),
		},
	}
	for {
		resp, err := client.Create(ctx, req)
		if err != nil {
			return err
		}
		calls := resp.FunctionCalls()
		if len(calls) == 0 {
			fmt.Println(resp.OutputText())
			return nil
		}
		// Every output item goes back, reasoning included, followed by an
		// output for each call.
		req.Input = append(req.Input, resp.Output...)
		for _, call := range calls {
			var args struct {
				Location string `json:"location"`
			}
			if err := call.UnmarshalArguments(&args); err != nil {
				return err
			}
			req.Input = append(req.Input, openresponses.NewFunctionCallOutput(call.CallID,
				fmt.Sprintf("It is 18C and foggy in %s.", args.Location)))
		}
	}
}
