// Package echo is a deterministic Open Responses adapter for tests and
// conformance runs. It needs no model: it echoes the last user message,
// calls the first function tool when one is offered, and compacts by
// encoding the conversation into the compaction item.
package echo

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ChristopherDavenport/openresponses"
)

// Adapter implements openresponses.Adapter with canned behaviour.
type Adapter struct {
	// Phase labels assistant output messages. Defaults to final_answer.
	Phase openresponses.Phase
}

var _ openresponses.Adapter = (*Adapter)(nil)

// Create returns the response that CreateStream would stream.
func (a *Adapter) Create(ctx context.Context, req openresponses.Request) (*openresponses.Response, error) {
	return openresponses.CollectStream(ctx, a, req)
}

// CreateStream streams the canned response to sink.
func (a *Adapter) CreateStream(_ context.Context, req openresponses.Request, sink openresponses.EventSink) error {
	input, err := expandCompactions(req.Input)
	if err != nil {
		return err
	}
	em := openresponses.NewEmitter(sink, openresponses.NewResponse(req))

	var item openresponses.Item
	if tool, ok := pendingTool(req, input); ok {
		call, err := em.FunctionCall("", tool.Name)
		if err != nil {
			return err
		}
		if err := call.Arguments(arguments(tool, lastUserText(input))); err != nil {
			return err
		}
		if err := call.Close(); err != nil {
			return err
		}
		item = call.Item()
	} else {
		phase := a.Phase
		if phase == "" {
			phase = openresponses.PhaseFinalAnswer
		}
		msg, err := em.Message(phase)
		if err != nil {
			return err
		}
		for _, chunk := range chunks(reply(input)) {
			if err := msg.Text(chunk); err != nil {
				return err
			}
		}
		if err := msg.Close(); err != nil {
			return err
		}
		item = msg.Item()
	}
	u := usage(input, item)
	em.Response().Usage = &u
	return em.Complete()
}

// Compact encodes the conversation into a single compaction item. The
// adapter is stateless, so a previous_response_id cannot be resolved and
// is rejected.
func (a *Adapter) Compact(_ context.Context, req openresponses.CompactRequest) (*openresponses.CompactResponse, error) {
	if req.PreviousResponseID != "" {
		return nil, openresponses.PreviousResponseNotFound(req.PreviousResponseID)
	}
	input, err := expandCompactions(req.Input)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(openresponses.Items(input))
	if err != nil {
		return nil, fmt.Errorf("echo: encode compaction: %w", err)
	}
	item := &openresponses.Compaction{
		ID:               openresponses.NewID("cmp"),
		EncryptedContent: base64.StdEncoding.EncodeToString(encoded),
		CreatedBy:        "echo",
	}
	u := usage(input, item)
	return &openresponses.CompactResponse{
		ID:        openresponses.NewID("resp"),
		Object:    openresponses.ObjectCompaction,
		Output:    openresponses.Items{item},
		CreatedAt: time.Now().Unix(),
		Usage:     &u,
	}, nil
}

// pendingTool returns the first function tool when the model should call
// it: a tool is offered, tool_choice is not "none", and the last item is
// not already a tool result.
func pendingTool(req openresponses.Request, input []openresponses.Item) (*openresponses.FunctionTool, bool) {
	if req.ToolChoice.Mode == openresponses.ToolChoiceNone {
		return nil, false
	}
	if len(input) > 0 {
		if _, ok := input[len(input)-1].(*openresponses.FunctionCallOutput); ok {
			return nil, false
		}
	}
	if req.ToolChoice.Function != nil {
		for _, tool := range req.Tools {
			if ft, ok := tool.(*openresponses.FunctionTool); ok && ft.Name == req.ToolChoice.Function.Name {
				return ft, true
			}
		}
	}
	for _, tool := range req.Tools {
		if ft, ok := tool.(*openresponses.FunctionTool); ok {
			return ft, true
		}
	}
	return nil, false
}

// arguments builds a JSON object that fills each required parameter with
// the user's text, so the call is plausible for simple schemas.
func arguments(tool *openresponses.FunctionTool, text string) string {
	var schema struct {
		Required []string `json:"required"`
	}
	_ = json.Unmarshal(tool.Parameters, &schema) // an unparsable schema just yields {}
	args := make(map[string]string, len(schema.Required))
	for _, name := range schema.Required {
		args[name] = text
	}
	out, err := json.Marshal(args)
	if err != nil {
		return "{}"
	}
	return string(out)
}

// reply decides what the assistant says.
func reply(input []openresponses.Item) string {
	for i := len(input) - 1; i >= 0; i-- {
		switch v := input[i].(type) {
		case *openresponses.FunctionCallOutput:
			return "Tool result: " + v.Output.String()
		case *openresponses.Message:
			if v.Role == openresponses.RoleUser {
				if text := v.Text(); text != "" {
					return text
				}
				return "I received your message."
			}
		}
	}
	return "Hello!"
}

func lastUserText(input []openresponses.Item) string {
	for i := len(input) - 1; i >= 0; i-- {
		if m, ok := input[i].(*openresponses.Message); ok && m.Role == openresponses.RoleUser {
			return m.Text()
		}
	}
	return ""
}

// expandCompactions replaces compaction items produced by this adapter
// with the conversation they encode.
func expandCompactions(input []openresponses.Item) ([]openresponses.Item, error) {
	out := make([]openresponses.Item, 0, len(input))
	for i, item := range input {
		c, ok := item.(*openresponses.Compaction)
		if !ok {
			out = append(out, item)
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(c.EncryptedContent)
		if err != nil {
			return nil, openresponses.InvalidRequest(openresponses.CodeInvalidValue, "compaction item was not produced by this server", fmt.Sprintf("input[%d].encrypted_content", i))
		}
		var items openresponses.Items
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, openresponses.InvalidRequest(openresponses.CodeInvalidValue, "compaction item is corrupt", fmt.Sprintf("input[%d].encrypted_content", i))
		}
		out = append(out, items...)
	}
	return out, nil
}

// chunks splits text into word-sized deltas.
func chunks(text string) []string {
	words := strings.SplitAfter(text, " ")
	out := words[:0]
	for _, w := range words {
		if w != "" {
			out = append(out, w)
		}
	}
	return out
}

func usage(input []openresponses.Item, output openresponses.Item) openresponses.Usage {
	in := 0
	for _, item := range input {
		in += tokens(item)
	}
	out := tokens(output)
	return openresponses.Usage{InputTokens: in, OutputTokens: out, TotalTokens: in + out}
}

func tokens(item openresponses.Item) int {
	data, err := json.Marshal(item)
	if err != nil {
		return 0
	}
	return len(strings.Fields(string(data)))
}
