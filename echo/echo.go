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
func (a *Adapter) CreateStream(ctx context.Context, req openresponses.Request, sink openresponses.EventSink) error {
	input, err := expandCompactions(req.Input)
	if err != nil {
		return err
	}
	resp := openresponses.NewResponse(req)
	resp.ID = openresponses.NewID("resp")
	if err := sink.Send(&openresponses.ResponseCreatedEvent{Response: resp}); err != nil {
		return err
	}
	if err := sink.Send(&openresponses.ResponseInProgressEvent{Response: resp}); err != nil {
		return err
	}

	var item openresponses.Item
	if tool, ok := pendingTool(req, input); ok {
		item, err = a.streamFunctionCall(sink, resp, tool, input)
	} else {
		item, err = a.streamMessage(sink, resp, reply(input))
	}
	if err != nil {
		return err
	}
	resp.Output = openresponses.Items{item}
	resp.Status = openresponses.ResponseStatusCompleted
	now := time.Now().Unix()
	resp.CompletedAt = &now
	u := usage(input, item)
	resp.Usage = &u
	return sink.Send(&openresponses.ResponseCompletedEvent{Response: resp})
}

// Compact encodes the conversation into a single compaction item.
func (a *Adapter) Compact(_ context.Context, req openresponses.CompactRequest) (*openresponses.CompactResponse, error) {
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
	return &openresponses.CompactResponse{
		ID:        openresponses.NewID("resp"),
		Object:    openresponses.ObjectCompaction,
		Output:    openresponses.Items{item},
		CreatedAt: time.Now().Unix(),
		Usage:     usage(input, item),
	}, nil
}

func (a *Adapter) streamMessage(sink openresponses.EventSink, resp *openresponses.Response, text string) (openresponses.Item, error) {
	phase := a.Phase
	if phase == "" {
		phase = openresponses.PhaseFinalAnswer
	}
	msg := &openresponses.Message{
		ID:      openresponses.NewID("msg"),
		Status:  openresponses.StatusInProgress,
		Role:    openresponses.RoleAssistant,
		Phase:   phase,
		Content: openresponses.Contents{},
	}
	if err := sink.Send(&openresponses.OutputItemAddedEvent{Item: msg}); err != nil {
		return nil, err
	}
	part := &openresponses.OutputText{}
	if err := sink.Send(&openresponses.ContentPartAddedEvent{ItemID: msg.ID, Part: part}); err != nil {
		return nil, err
	}
	for _, chunk := range chunks(text) {
		if err := sink.Send(&openresponses.OutputTextDeltaEvent{ItemID: msg.ID, Delta: chunk}); err != nil {
			return nil, err
		}
	}
	part.Text = text
	if err := sink.Send(&openresponses.OutputTextDoneEvent{ItemID: msg.ID, Text: text}); err != nil {
		return nil, err
	}
	if err := sink.Send(&openresponses.ContentPartDoneEvent{ItemID: msg.ID, Part: part}); err != nil {
		return nil, err
	}
	msg.Content = openresponses.Contents{part}
	msg.Status = openresponses.StatusCompleted
	if err := sink.Send(&openresponses.OutputItemDoneEvent{Item: msg}); err != nil {
		return nil, err
	}
	return msg, nil
}

func (a *Adapter) streamFunctionCall(sink openresponses.EventSink, resp *openresponses.Response, tool *openresponses.FunctionTool, input []openresponses.Item) (openresponses.Item, error) {
	args := arguments(tool, lastUserText(input))
	call := &openresponses.FunctionCall{
		ID:     openresponses.NewID("fc"),
		Status: openresponses.StatusInProgress,
		CallID: openresponses.NewID("call"),
		Name:   tool.Name,
	}
	if err := sink.Send(&openresponses.OutputItemAddedEvent{Item: call}); err != nil {
		return nil, err
	}
	if err := sink.Send(&openresponses.FunctionCallArgumentsDeltaEvent{ItemID: call.ID, Delta: args}); err != nil {
		return nil, err
	}
	if err := sink.Send(&openresponses.FunctionCallArgumentsDoneEvent{ItemID: call.ID, Arguments: args}); err != nil {
		return nil, err
	}
	call.Arguments = args
	call.Status = openresponses.StatusCompleted
	if err := sink.Send(&openresponses.OutputItemDoneEvent{Item: call}); err != nil {
		return nil, err
	}
	return call, nil
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
