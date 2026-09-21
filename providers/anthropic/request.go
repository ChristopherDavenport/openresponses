package anthropic

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime"
	"net/url"
	"path"
	"strings"

	"github.com/ChristopherDavenport/openresponses"
	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/param"
)

// Slug prefix for Claude extension types: tools, items and annotations
// the Open Responses specification does not define.
const slug = "anthropic."

// encodeRequest translates a request into the parameters of one Messages
// call. Fields the Messages API has no equivalent for are rejected with
// invalid_request naming the field; nothing is dropped silently.
func encodeRequest(req openresponses.Request, maxTokens int64) (sdk.MessageNewParams, error) {
	if req.PreviousResponseID != "" {
		// The handler resolves continuation when it has a store. Reaching
		// here means it has none, and the Messages API keeps no state.
		return sdk.MessageNewParams{}, openresponses.PreviousResponseNotFound(req.PreviousResponseID)
	}
	p, err := encodeConfig(req, maxTokens)
	if err != nil {
		return sdk.MessageNewParams{}, err
	}
	messages, system, err := encodeInput(req.Input)
	if err != nil {
		return sdk.MessageNewParams{}, err
	}
	if req.Instructions != "" {
		system = append([]sdk.TextBlockParam{{Text: req.Instructions}}, system...)
	}
	p.Messages = messages
	p.System = system
	return p, nil
}

func unsupported(param string) *openresponses.Error {
	return openresponses.InvalidRequest(openresponses.CodeUnsupportedParameter,
		fmt.Sprintf("%s is not supported by the Messages API", param), param)
}

func invalid(param, message string) *openresponses.Error {
	return openresponses.InvalidRequest(openresponses.CodeInvalidValue, message, param)
}

// encodeConfig maps the request's settings.
func encodeConfig(req openresponses.Request, maxTokens int64) (sdk.MessageNewParams, error) {
	p := sdk.MessageNewParams{Model: sdk.Model(req.Model), MaxTokens: maxTokens}
	if req.MaxOutputTokens != nil {
		p.MaxTokens = int64(*req.MaxOutputTokens)
	}
	if req.Temperature != nil {
		p.Temperature = param.NewOpt(*req.Temperature)
	}
	if req.TopP != nil {
		p.TopP = param.NewOpt(*req.TopP)
	}
	switch {
	case req.PresencePenalty != nil:
		return p, unsupported("presence_penalty")
	case req.FrequencyPenalty != nil:
		return p, unsupported("frequency_penalty")
	case req.TopLogprobs != nil:
		return p, unsupported("top_logprobs")
	case req.MaxToolCalls != nil:
		return p, unsupported("max_tool_calls")
	case req.Truncation == openresponses.TruncationAuto:
		return p, unsupported("truncation")
	case req.Text.Verbosity != "":
		return p, unsupported("text.verbosity")
	}
	if req.SafetyIdentifier != "" {
		p.Metadata = sdk.MetadataParam{UserID: param.NewOpt(req.SafetyIdentifier)}
	}
	if req.PromptCacheKey != "" {
		// Claude caches on request. A cache key is the client asking for it,
		// so the request opts in; the key itself has nothing to name.
		p.CacheControl = sdk.NewCacheControlEphemeralParam()
	}
	// metadata is the client's own bookkeeping; the response echoes it and
	// nothing upstream wants it.

	switch req.ServiceTier {
	case "":
	case openresponses.ServiceTierAuto:
		p.ServiceTier = sdk.MessageNewParamsServiceTierAuto
	case openresponses.ServiceTierDefault:
		p.ServiceTier = sdk.MessageNewParamsServiceTierStandardOnly
	default:
		return p, unsupported("service_tier")
	}

	if err := encodeReasoning(req.Reasoning, &p); err != nil {
		return p, err
	}

	if f := req.Text.Format; f != nil {
		switch f.Type {
		case "", openresponses.TextFormatText:
		case openresponses.TextFormatJSONSchema:
			var schema map[string]any
			if err := json.Unmarshal(f.Schema, &schema); err != nil || schema == nil {
				return p, invalid("text.format.schema", "schema is not a JSON object")
			}
			p.OutputConfig.Format = sdk.JSONOutputFormatParam{Schema: schema}
		default:
			// json_object has no schema to constrain against.
			return p, unsupported("text.format.type")
		}
	}

	tools, err := encodeTools(req.Tools)
	if err != nil {
		return p, err
	}
	p.Tools = tools

	choice, err := encodeToolChoice(req.ToolChoice, req.ParallelToolCalls != nil && !*req.ParallelToolCalls)
	if err != nil {
		return p, err
	}
	p.ToolChoice = choice
	return p, nil
}

// encodeReasoning maps effort onto output_config.effort and the summary
// setting onto thinking display. "none" disables thinking; whether a
// model allows that is the model's business.
func encodeReasoning(r openresponses.ReasoningConfig, p *sdk.MessageNewParams) error {
	switch r.Effort {
	case "":
	case openresponses.ReasoningEffortNone:
		p.Thinking = sdk.ThinkingConfigParamUnion{OfDisabled: &sdk.ThinkingConfigDisabledParam{}}
		return nil
	case openresponses.ReasoningEffortMinimal, openresponses.ReasoningEffortLow:
		p.OutputConfig.Effort = sdk.OutputConfigEffortLow
	case openresponses.ReasoningEffortMedium:
		p.OutputConfig.Effort = sdk.OutputConfigEffortMedium
	case openresponses.ReasoningEffortHigh:
		p.OutputConfig.Effort = sdk.OutputConfigEffortHigh
	case openresponses.ReasoningEffortXHigh:
		p.OutputConfig.Effort = sdk.OutputConfigEffortXhigh
	default:
		return invalid("reasoning.effort", fmt.Sprintf("unknown reasoning effort %q", r.Effort))
	}
	if r.Summary != "" {
		p.Thinking = sdk.ThinkingConfigParamUnion{OfAdaptive: &sdk.ThinkingConfigAdaptiveParam{Display: sdk.ThinkingConfigAdaptiveDisplaySummarized}}
	}
	return nil
}

// encodeTools maps function tools and passes anthropic.* tools through:
// the slug is stripped from the type and the rest of the tool is sent as
// written, so {"type": "anthropic.web_search_20260209", "name":
// "web_search"} is the web search tool.
func encodeTools(tools openresponses.Tools) ([]sdk.ToolUnionParam, error) {
	if len(tools) == 0 {
		return nil, nil
	}
	out := make([]sdk.ToolUnionParam, 0, len(tools))
	for i, tool := range tools {
		p := fmt.Sprintf("tools[%d]", i)
		switch t := tool.(type) {
		case *openresponses.FunctionTool:
			schema := json.RawMessage(`{"type":"object"}`)
			if len(t.Parameters) > 0 && string(t.Parameters) != "null" {
				if !json.Valid(t.Parameters) {
					return nil, invalid(p+".parameters", "parameters is not valid JSON")
				}
				schema = t.Parameters
			}
			fn := sdk.ToolParam{Name: t.Name, InputSchema: param.Override[sdk.ToolInputSchemaParam](schema)}
			if t.Description != "" {
				fn.Description = param.NewOpt(t.Description)
			}
			if t.Strict != nil {
				fn.Strict = param.NewOpt(*t.Strict)
			}
			out = append(out, sdk.ToolUnionParam{OfTool: &fn})
		case *openresponses.UnknownTool:
			raw, err := unslug(t.Type, t.Raw)
			if err != nil {
				return nil, invalid(p+".type", err.Error())
			}
			if raw == nil {
				return nil, unsupported(p + ".type")
			}
			out = append(out, param.Override[sdk.ToolUnionParam](raw))
		default:
			return nil, unsupported(p + ".type")
		}
	}
	return out, nil
}

// unslug returns raw with the anthropic. prefix removed from its type, or
// nil when typ does not carry the prefix.
func unslug(typ string, raw json.RawMessage) (json.RawMessage, error) {
	name, ok := strings.CutPrefix(typ, slug)
	if !ok {
		return nil, nil
	}
	fields := map[string]json.RawMessage{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &fields); err != nil {
			return nil, err
		}
	}
	fields["type"] = json.RawMessage(strconvQuote(name))
	return json.Marshal(fields)
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func encodeToolChoice(c openresponses.ToolChoice, disableParallel bool) (sdk.ToolChoiceUnionParam, error) {
	var opt param.Opt[bool]
	if disableParallel {
		opt = param.NewOpt(true)
	}
	switch {
	case c.Function != nil:
		return sdk.ToolChoiceUnionParam{OfTool: &sdk.ToolChoiceToolParam{Name: c.Function.Name, DisableParallelToolUse: opt}}, nil
	case c.Allowed != nil:
		// No way to allow a subset of the configured tools.
		return sdk.ToolChoiceUnionParam{}, unsupported("tool_choice.type")
	case c.Mode == openresponses.ToolChoiceNone:
		return sdk.ToolChoiceUnionParam{OfNone: &sdk.ToolChoiceNoneParam{}}, nil
	case c.Mode == openresponses.ToolChoiceRequired:
		return sdk.ToolChoiceUnionParam{OfAny: &sdk.ToolChoiceAnyParam{DisableParallelToolUse: opt}}, nil
	case c.Mode == openresponses.ToolChoiceAuto, c.Mode == "" && disableParallel:
		return sdk.ToolChoiceUnionParam{OfAuto: &sdk.ToolChoiceAutoParam{DisableParallelToolUse: opt}}, nil
	case c.Mode != "":
		return sdk.ToolChoiceUnionParam{}, invalid("tool_choice", fmt.Sprintf("unknown tool choice %q", c.Mode))
	}
	return sdk.ToolChoiceUnionParam{}, nil
}

// encodeInput folds the items into alternating user and assistant
// messages. Everything the user side sends (messages, function outputs)
// joins one user message; everything the assistant produced (messages,
// function calls, reasoning, extension blocks) joins one assistant
// message, so a tool result always directly follows the message that
// called it. System and developer messages are returned separately for
// the system prompt.
func encodeInput(items openresponses.Items) (messages []sdk.MessageParam, system []sdk.TextBlockParam, err error) {
	add := func(role sdk.MessageParamRole, blocks []sdk.ContentBlockParamUnion) {
		if len(blocks) == 0 {
			return
		}
		if n := len(messages); n > 0 && messages[n-1].Role == role {
			messages[n-1].Content = append(messages[n-1].Content, blocks...)
			return
		}
		messages = append(messages, sdk.MessageParam{Role: role, Content: blocks})
	}
	for i, item := range items {
		p := fmt.Sprintf("input[%d]", i)
		switch it := item.(type) {
		case *openresponses.Message:
			blocks, err := encodeBlocks(it.Content, p+".content")
			if err != nil {
				return nil, nil, err
			}
			switch it.Role {
			case openresponses.RoleUser:
				add(sdk.MessageParamRoleUser, blocks)
			case openresponses.RoleAssistant:
				add(sdk.MessageParamRoleAssistant, blocks)
			case openresponses.RoleSystem, openresponses.RoleDeveloper:
				for j, b := range blocks {
					if b.OfText == nil {
						return nil, nil, unsupported(fmt.Sprintf("%s.content[%d].type", p, j))
					}
					system = append(system, *b.OfText)
				}
			default:
				return nil, nil, invalid(p+".role", fmt.Sprintf("unknown role %q", it.Role))
			}
		case *openresponses.FunctionCall:
			input, err := decodeArguments(it.Arguments)
			if err != nil {
				return nil, nil, invalid(p+".arguments", "arguments is not a JSON object: "+err.Error())
			}
			add(sdk.MessageParamRoleAssistant, []sdk.ContentBlockParamUnion{sdk.NewToolUseBlock(it.CallID, input, it.Name)})
		case *openresponses.FunctionCallOutput:
			block, err := encodeFunctionOutput(it, p+".output")
			if err != nil {
				return nil, nil, err
			}
			add(sdk.MessageParamRoleUser, []sdk.ContentBlockParamUnion{block})
		case *openresponses.ReasoningItem:
			// A thinking block is its text and its signature, both returned
			// unchanged. The text lives in the summary (see decoder).
			text := it.Summary.Text()
			if text == "" {
				text = it.Content.Text()
			}
			add(sdk.MessageParamRoleAssistant, []sdk.ContentBlockParamUnion{sdk.NewThinkingBlock(it.EncryptedContent, text)})
		case *openresponses.UnknownItem:
			block, err := extensionBlock(it)
			if err != nil {
				return nil, nil, invalid(p, err.Error())
			}
			if block == nil {
				return nil, nil, unsupported(p + ".type")
			}
			add(sdk.MessageParamRoleAssistant, []sdk.ContentBlockParamUnion{param.Override[sdk.ContentBlockParamUnion](block)})
		default:
			// Compaction and item_reference need state this adapter does not
			// keep.
			return nil, nil, unsupported(p + ".type")
		}
	}
	return messages, system, nil
}

func decodeArguments(s string) (map[string]any, error) {
	args := map[string]any{}
	if strings.TrimSpace(s) == "" {
		return args, nil
	}
	if err := json.Unmarshal([]byte(s), &args); err != nil {
		return nil, err
	}
	return args, nil
}

// encodeFunctionOutput builds the tool_result block. Text and media parts
// are carried as content; the tool_use_id is the call_id, which is the
// id Claude issued when the call came from this adapter.
func encodeFunctionOutput(out *openresponses.FunctionCallOutput, p string) (sdk.ContentBlockParamUnion, error) {
	result := sdk.ToolResultBlockParam{ToolUseID: out.CallID}
	if out.Output.Parts == nil {
		if out.Output.Text != "" {
			result.Content = []sdk.ToolResultBlockParamContentUnion{{OfText: &sdk.TextBlockParam{Text: out.Output.Text}}}
		}
		return sdk.ContentBlockParamUnion{OfToolResult: &result}, nil
	}
	for j, part := range out.Output.Parts {
		pp := fmt.Sprintf("%s[%d]", p, j)
		switch c := part.(type) {
		case *openresponses.Text:
			result.Content = append(result.Content, sdk.ToolResultBlockParamContentUnion{OfText: &sdk.TextBlockParam{Text: c.Text}})
		case *openresponses.InputText:
			result.Content = append(result.Content, sdk.ToolResultBlockParamContentUnion{OfText: &sdk.TextBlockParam{Text: c.Text}})
		case *openresponses.OutputText:
			result.Content = append(result.Content, sdk.ToolResultBlockParamContentUnion{OfText: &sdk.TextBlockParam{Text: c.Text}})
		case *openresponses.InputImage:
			img, err := imageBlock(c, pp)
			if err != nil {
				return sdk.ContentBlockParamUnion{}, err
			}
			result.Content = append(result.Content, sdk.ToolResultBlockParamContentUnion{OfImage: img})
		case *openresponses.InputFile:
			block, err := fileBlock(c, pp)
			if err != nil {
				return sdk.ContentBlockParamUnion{}, err
			}
			switch {
			case block.OfDocument != nil:
				result.Content = append(result.Content, sdk.ToolResultBlockParamContentUnion{OfDocument: block.OfDocument})
			case block.OfImage != nil:
				result.Content = append(result.Content, sdk.ToolResultBlockParamContentUnion{OfImage: block.OfImage})
			}
		default:
			return sdk.ContentBlockParamUnion{}, unsupported(pp + ".type")
		}
	}
	return sdk.ContentBlockParamUnion{OfToolResult: &result}, nil
}

// extensionBlock returns the block an anthropic.* item carries: the item
// is {"type": "anthropic.<block type>", ..., "block": <block>}. It
// returns nil, nil for an item without the slug.
func extensionBlock(u *openresponses.UnknownItem) (json.RawMessage, error) {
	if !strings.HasPrefix(u.Type, slug) {
		return nil, nil
	}
	var wrapper struct {
		Block json.RawMessage `json:"block"`
	}
	if err := json.Unmarshal(u.Raw, &wrapper); err != nil {
		return nil, err
	}
	if len(wrapper.Block) == 0 || !json.Valid(wrapper.Block) {
		return nil, fmt.Errorf("%s item has no block", u.Type)
	}
	return wrapper.Block, nil
}

// encodeBlocks maps message content. Text of any flavour is a text
// block; images and files become image and document blocks.
func encodeBlocks(contents openresponses.Contents, p string) ([]sdk.ContentBlockParamUnion, error) {
	blocks := make([]sdk.ContentBlockParamUnion, 0, len(contents))
	for j, content := range contents {
		pp := fmt.Sprintf("%s[%d]", p, j)
		switch c := content.(type) {
		case *openresponses.InputText:
			blocks = append(blocks, sdk.NewTextBlock(c.Text))
		case *openresponses.OutputText:
			blocks = append(blocks, sdk.NewTextBlock(c.Text))
		case *openresponses.Text:
			blocks = append(blocks, sdk.NewTextBlock(c.Text))
		case *openresponses.Refusal:
			blocks = append(blocks, sdk.NewTextBlock(c.Refusal))
		case *openresponses.InputImage:
			img, err := imageBlock(c, pp)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, sdk.ContentBlockParamUnion{OfImage: img})
		case *openresponses.InputFile:
			block, err := fileBlock(c, pp)
			if err != nil {
				return nil, err
			}
			blocks = append(blocks, block)
		case *openresponses.InputVideo:
			return nil, unsupported(pp + ".type")
		default:
			return nil, unsupported(pp + ".type")
		}
	}
	return blocks, nil
}

// imageBlock maps an input_image: a Files API id, a data URL as base64,
// or any other URL as a URL source for the API to fetch.
func imageBlock(c *openresponses.InputImage, p string) (*sdk.ImageBlockParam, error) {
	// detail is a resolution hint the Messages API does not take.
	switch {
	case c.FileID != "":
		return &sdk.ImageBlockParam{Source: sdk.ImageBlockParamSourceUnion{OfFile: &sdk.FileImageSourceParam{FileID: c.FileID}}}, nil
	case strings.HasPrefix(c.ImageURL, "data:"):
		mt, data, err := parseDataURL(c.ImageURL)
		if err != nil {
			return nil, invalid(p+".image_url", err.Error())
		}
		return &sdk.ImageBlockParam{Source: sdk.ImageBlockParamSourceUnion{OfBase64: &sdk.Base64ImageSourceParam{
			MediaType: sdk.Base64ImageSourceMediaType(mt), Data: base64.StdEncoding.EncodeToString(data),
		}}}, nil
	case c.ImageURL != "":
		if u, err := url.Parse(c.ImageURL); err != nil || u.Scheme == "" {
			return nil, invalid(p+".image_url", "not a URL: "+c.ImageURL)
		}
		return &sdk.ImageBlockParam{Source: sdk.ImageBlockParamSourceUnion{OfURL: &sdk.URLImageSourceParam{URL: c.ImageURL}}}, nil
	}
	return nil, openresponses.InvalidRequest(openresponses.CodeMissingRequiredParameter, "input_image needs image_url or file_id", p)
}

// fileBlock maps an input_file onto a document block, or an image block
// when the file is an image. Inline data is a data URL or bare base64
// typed by the filename; PDFs and text are the document sources the API
// takes.
func fileBlock(f *openresponses.InputFile, p string) (sdk.ContentBlockParamUnion, error) {
	doc := func(src sdk.DocumentBlockParamSourceUnion) sdk.ContentBlockParamUnion {
		d := &sdk.DocumentBlockParam{Source: src}
		if f.Filename != "" {
			d.Title = param.NewOpt(f.Filename)
		}
		return sdk.ContentBlockParamUnion{OfDocument: d}
	}
	switch {
	case f.FileID != "":
		return doc(sdk.DocumentBlockParamSourceUnion{OfFile: &sdk.FileDocumentSourceParam{FileID: f.FileID}}), nil
	case f.FileURL != "":
		if u, err := url.Parse(f.FileURL); err != nil || u.Scheme == "" {
			return sdk.ContentBlockParamUnion{}, invalid(p+".file_url", "not a URL: "+f.FileURL)
		}
		return doc(sdk.DocumentBlockParamSourceUnion{OfURL: &sdk.URLPDFSourceParam{URL: f.FileURL}}), nil
	case f.FileData != "":
		var mt string
		var data []byte
		if strings.HasPrefix(f.FileData, "data:") {
			var err error
			if mt, data, err = parseDataURL(f.FileData); err != nil {
				return sdk.ContentBlockParamUnion{}, invalid(p+".file_data", err.Error())
			}
		} else {
			var err error
			if data, err = base64.StdEncoding.DecodeString(f.FileData); err != nil {
				return sdk.ContentBlockParamUnion{}, invalid(p+".file_data", "file_data is not base64: "+err.Error())
			}
			if mt = typeByExtension(f.Filename); mt == "" {
				return sdk.ContentBlockParamUnion{}, invalid(p+".filename", "file_data needs a filename with a known extension, or a data URL with a media type")
			}
		}
		switch {
		case mt == "application/pdf":
			return doc(sdk.DocumentBlockParamSourceUnion{OfBase64: &sdk.Base64PDFSourceParam{Data: base64.StdEncoding.EncodeToString(data)}}), nil
		case strings.HasPrefix(mt, "text/"):
			return doc(sdk.DocumentBlockParamSourceUnion{OfText: &sdk.PlainTextSourceParam{Data: string(data)}}), nil
		case strings.HasPrefix(mt, "image/"):
			return sdk.ContentBlockParamUnion{OfImage: &sdk.ImageBlockParam{Source: sdk.ImageBlockParamSourceUnion{OfBase64: &sdk.Base64ImageSourceParam{
				MediaType: sdk.Base64ImageSourceMediaType(mt), Data: base64.StdEncoding.EncodeToString(data),
			}}}}, nil
		}
		return sdk.ContentBlockParamUnion{}, invalid(p+".file_data", fmt.Sprintf("the Messages API takes PDF, text and image files, not %s", mt))
	}
	return sdk.ContentBlockParamUnion{}, openresponses.InvalidRequest(openresponses.CodeMissingRequiredParameter,
		"input_file needs file_data, file_url or file_id", p)
}

// typeByExtension infers a media type from a path, without the charset
// parameter Go adds to text types.
func typeByExtension(p string) string {
	mt, _, err := mime.ParseMediaType(mime.TypeByExtension(path.Ext(p)))
	if err != nil {
		return ""
	}
	return mt
}

// parseDataURL decodes data:<media type>[;base64],<data>.
func parseDataURL(raw string) (string, []byte, error) {
	meta, payload, ok := strings.Cut(strings.TrimPrefix(raw, "data:"), ",")
	if !ok {
		return "", nil, fmt.Errorf("data URL has no comma")
	}
	mt, isBase64 := strings.CutSuffix(meta, ";base64")
	if i := strings.IndexByte(mt, ';'); i >= 0 {
		mt = mt[:i]
	}
	if mt == "" {
		return "", nil, fmt.Errorf("data URL has no media type")
	}
	if isBase64 {
		data, err := base64.StdEncoding.DecodeString(payload)
		if err != nil {
			return "", nil, fmt.Errorf("data URL payload is not base64: %w", err)
		}
		return mt, data, nil
	}
	data, err := url.PathUnescape(payload)
	if err != nil {
		return "", nil, fmt.Errorf("data URL payload: %w", err)
	}
	return mt, []byte(data), nil
}
