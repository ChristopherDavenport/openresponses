package gemini

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime"
	"net/url"
	"path"
	"strings"

	"github.com/ChristopherDavenport/openresponses"
	"google.golang.org/genai"
)

// Slug prefix for Gemini extension types: tools, items and content parts
// the Open Responses specification does not define.
const slug = "gemini."

// encodeRequest translates a request into the contents and configuration
// of one GenerateContent call. Fields Gemini has no equivalent for are
// rejected with invalid_request naming the field; nothing is dropped
// silently.
func encodeRequest(req openresponses.Request, thinking Thinking) ([]*genai.Content, *genai.GenerateContentConfig, error) {
	if req.PreviousResponseID != "" {
		// The handler resolves continuation when it has a store. Reaching
		// here means it has none, and Gemini keeps no conversation state.
		return nil, nil, openresponses.PreviousResponseNotFound(req.PreviousResponseID)
	}
	cfg, err := encodeConfig(req, thinking)
	if err != nil {
		return nil, nil, err
	}
	contents, system, err := encodeInput(req.Input)
	if err != nil {
		return nil, nil, err
	}
	if req.Instructions != "" {
		system = append([]*genai.Part{genai.NewPartFromText(req.Instructions)}, system...)
	}
	if len(system) > 0 {
		cfg.SystemInstruction = &genai.Content{Parts: system}
	}
	return contents, cfg, nil
}

func unsupported(param string) *openresponses.Error {
	return openresponses.InvalidRequest(openresponses.CodeUnsupportedParameter,
		fmt.Sprintf("%s is not supported by Gemini", param), param)
}

func invalid(param, message string) *openresponses.Error {
	return openresponses.InvalidRequest(openresponses.CodeInvalidValue, message, param)
}

// encodeConfig maps the request's settings. Model is not part of the
// config; the caller passes it to the SDK directly.
func encodeConfig(req openresponses.Request, thinking Thinking) (*genai.GenerateContentConfig, error) {
	cfg := &genai.GenerateContentConfig{CandidateCount: 1}

	if req.MaxOutputTokens != nil {
		cfg.MaxOutputTokens = int32(*req.MaxOutputTokens)
	}
	cfg.Temperature = f32(req.Temperature)
	cfg.TopP = f32(req.TopP)
	cfg.PresencePenalty = f32(req.PresencePenalty)
	cfg.FrequencyPenalty = f32(req.FrequencyPenalty)
	if req.TopLogprobs != nil {
		cfg.ResponseLogprobs = true
		cfg.Logprobs = genai.Ptr(int32(*req.TopLogprobs))
	}
	if req.ParallelToolCalls != nil && !*req.ParallelToolCalls {
		// Gemini decides on its own whether to issue several calls at once.
		return nil, unsupported("parallel_tool_calls")
	}
	if req.MaxToolCalls != nil {
		return nil, unsupported("max_tool_calls")
	}
	if req.SafetyIdentifier != "" {
		return nil, unsupported("safety_identifier")
	}
	if req.PromptCacheKey != "" {
		// Implicit caching needs no key; explicit caches are a resource the
		// caller creates, not something a request names.
		return nil, unsupported("prompt_cache_key")
	}
	if req.Truncation == openresponses.TruncationAuto {
		return nil, unsupported("truncation")
	}
	if req.Text.Verbosity != "" {
		return nil, unsupported("text.verbosity")
	}
	if len(req.Metadata) > 0 {
		// Labels are honoured on Vertex AI and rejected by the Gemini API.
		cfg.Labels = req.Metadata
	}

	switch req.ServiceTier {
	case "", openresponses.ServiceTierAuto:
	case openresponses.ServiceTierDefault:
		cfg.ServiceTier = genai.ServiceTierStandard
	case openresponses.ServiceTierFlex:
		cfg.ServiceTier = genai.ServiceTierFlex
	case openresponses.ServiceTierPriority:
		cfg.ServiceTier = genai.ServiceTierPriority
	default:
		return nil, invalid("service_tier", fmt.Sprintf("unknown service_tier %q", req.ServiceTier))
	}

	tc, err := encodeReasoning(req.Reasoning, req.Model, thinking)
	if err != nil {
		return nil, err
	}
	cfg.ThinkingConfig = tc

	if f := req.Text.Format; f != nil {
		switch f.Type {
		case "", openresponses.TextFormatText:
		case openresponses.TextFormatJSONObject:
			cfg.ResponseMIMEType = "application/json"
		case openresponses.TextFormatJSONSchema:
			var schema any
			if err := json.Unmarshal(f.Schema, &schema); err != nil {
				return nil, invalid("text.format.schema", "schema is not valid JSON: "+err.Error())
			}
			cfg.ResponseMIMEType = "application/json"
			cfg.ResponseJsonSchema = schema
		default:
			return nil, invalid("text.format.type", fmt.Sprintf("unknown text format %q", f.Type))
		}
	}

	tools, err := encodeTools(req.Tools)
	if err != nil {
		return nil, err
	}
	cfg.Tools = tools

	choice, err := encodeToolChoice(req.ToolChoice)
	if err != nil {
		return nil, err
	}
	if choice != nil {
		cfg.ToolConfig = &genai.ToolConfig{FunctionCallingConfig: choice}
	}
	return cfg, nil
}

func f32(p *float64) *float32 {
	if p == nil {
		return nil
	}
	return genai.Ptr(float32(*p))
}

// effortBudgets is the thinking-token ladder for the generations that take
// a budget rather than a level. Gemini documents neither a mapping nor the
// per-model ceilings, so these are the widest values legal across the 2.5
// family: flash-lite's floor is 512 and flash and flash-lite cap at 24576,
// where pro would allow 32768.
var effortBudgets = map[openresponses.ReasoningEffort]int32{
	openresponses.ReasoningEffortNone:    0,
	openresponses.ReasoningEffortMinimal: 512,
	openresponses.ReasoningEffortLow:     4096,
	openresponses.ReasoningEffortMedium:  8192,
	openresponses.ReasoningEffortHigh:    24576,
}

var effortLevels = map[openresponses.ReasoningEffort]genai.ThinkingLevel{
	openresponses.ReasoningEffortMinimal: genai.ThinkingLevelMinimal,
	openresponses.ReasoningEffortLow:     genai.ThinkingLevelLow,
	openresponses.ReasoningEffortMedium:  genai.ThinkingLevelMedium,
	openresponses.ReasoningEffortHigh:    genai.ThinkingLevelHigh,
}

// encodeReasoning maps effort onto a thinking level or a thinking budget,
// whichever the model's generation takes. Sending both is a 400 and
// sending the wrong one is a 400, so exactly one field is ever set.
//
// Which values within an encoding a model accepts stays the model's
// business: "minimal" exists only on some Gemini 3 models and a budget
// below a model's floor is raised by Gemini, and both are reported
// upstream rather than guessed at here.
func encodeReasoning(r openresponses.ReasoningConfig, model string, thinking Thinking) (*genai.ThinkingConfig, error) {
	if r.IsZero() {
		return nil, nil
	}
	if thinking == ThinkingAuto {
		thinking = thinkingFor(model)
	}
	tc := &genai.ThinkingConfig{IncludeThoughts: r.Summary != ""}
	if r.Effort == "" {
		return tc, nil
	}
	if r.Effort == openresponses.ReasoningEffortNone && r.Summary != "" {
		// Thinking off and a summary of the thinking are contradictory, and
		// Gemini rejects the pair rather than picking one.
		return nil, invalid("reasoning.summary", "reasoning.summary asks for thoughts that reasoning.effort \"none\" switches off")
	}
	if thinking == ThinkingBudget {
		budget, ok := effortBudgets[r.Effort]
		if !ok {
			return nil, invalid("reasoning.effort", fmt.Sprintf("reasoning effort %q has no Gemini thinking budget", r.Effort))
		}
		tc.ThinkingBudget = genai.Ptr(budget)
		return tc, nil
	}
	if r.Effort == openresponses.ReasoningEffortNone {
		// A zero budget is the one way to ask for no thinking, and the
		// generations that take levels reject it: they always think.
		return nil, invalid("reasoning.effort",
			"reasoning effort \"none\" has no thinking level; Gemini 3 and later cannot stop thinking")
	}
	level, ok := effortLevels[r.Effort]
	if !ok {
		return nil, invalid("reasoning.effort", fmt.Sprintf("reasoning effort %q has no Gemini thinking level", r.Effort))
	}
	tc.ThinkingLevel = level
	return tc, nil
}

// encodeTools collects function tools into one declaration list and turns
// each gemini.* tool into the server tool of the same name: the slug is
// stripped, snake_case becomes camelCase, and the tool's own keys are
// its configuration in the SDK's wire form, so {"type":
// "gemini.google_search"} is Google Search with defaults.
func encodeTools(tools openresponses.Tools) ([]*genai.Tool, error) {
	var decls []*genai.FunctionDeclaration
	var out []*genai.Tool
	for i, tool := range tools {
		param := fmt.Sprintf("tools[%d]", i)
		switch t := tool.(type) {
		case *openresponses.FunctionTool:
			decl := &genai.FunctionDeclaration{Name: t.Name, Description: t.Description}
			if len(t.Parameters) > 0 && string(t.Parameters) != "null" {
				var schema any
				if err := json.Unmarshal(t.Parameters, &schema); err != nil {
					return nil, invalid(param+".parameters", "parameters is not valid JSON: "+err.Error())
				}
				decl.ParametersJsonSchema = schema
			}
			// strict has no Gemini equivalent and is accepted without effect.
			decls = append(decls, decl)
		case *openresponses.UnknownTool:
			st, err := serverTool(t)
			if err != nil {
				return nil, invalid(param+".type", err.Error())
			}
			if st == nil {
				return nil, unsupported(param + ".type")
			}
			out = append(out, st)
		default:
			return nil, unsupported(param + ".type")
		}
	}
	if len(decls) > 0 {
		out = append([]*genai.Tool{{FunctionDeclarations: decls}}, out...)
	}
	return out, nil
}

// serverTool decodes a gemini.* tool. It returns nil, nil for a tool
// without the slug, and an error for a slugged name genai does not know.
func serverTool(t *openresponses.UnknownTool) (*genai.Tool, error) {
	name, ok := strings.CutPrefix(t.Type, slug)
	if !ok {
		return nil, nil
	}
	var fields map[string]json.RawMessage
	if len(t.Raw) > 0 {
		if err := json.Unmarshal(t.Raw, &fields); err != nil {
			return nil, err
		}
	}
	delete(fields, "type")
	if fields == nil {
		fields = map[string]json.RawMessage{}
	}
	body, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	wrapped, err := json.Marshal(map[string]json.RawMessage{camel(name): body})
	if err != nil {
		return nil, err
	}
	var tool genai.Tool
	if err := json.Unmarshal(wrapped, &tool); err != nil {
		return nil, fmt.Errorf("%s: %w", t.Type, err)
	}
	if roundTrip, _ := json.Marshal(tool); string(roundTrip) == "{}" {
		return nil, fmt.Errorf("%s is not a Gemini tool", t.Type)
	}
	return &tool, nil
}

// camel turns google_search into googleSearch and url_context into
// urlContext, the field names the SDK's wire form uses.
func camel(s string) string {
	parts := strings.Split(s, "_")
	for i := 1; i < len(parts); i++ {
		if parts[i] != "" {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}

func encodeToolChoice(c openresponses.ToolChoice) (*genai.FunctionCallingConfig, error) {
	switch {
	case c.Function != nil:
		return &genai.FunctionCallingConfig{
			Mode:                 genai.FunctionCallingConfigModeAny,
			AllowedFunctionNames: []string{c.Function.Name},
		}, nil
	case c.Allowed != nil:
		mode, err := callingMode(c.Allowed.Mode, "tool_choice.mode")
		if err != nil {
			return nil, err
		}
		names := make([]string, 0, len(c.Allowed.Tools))
		for i, ref := range c.Allowed.Tools {
			if ref.Type != openresponses.ToolTypeFunction {
				return nil, unsupported(fmt.Sprintf("tool_choice.tools[%d].type", i))
			}
			names = append(names, ref.Name)
		}
		return &genai.FunctionCallingConfig{Mode: mode, AllowedFunctionNames: names}, nil
	case c.Mode != "":
		mode, err := callingMode(c.Mode, "tool_choice")
		if err != nil {
			return nil, err
		}
		return &genai.FunctionCallingConfig{Mode: mode}, nil
	}
	return nil, nil
}

func callingMode(m openresponses.ToolChoiceMode, param string) (genai.FunctionCallingConfigMode, error) {
	switch m {
	case openresponses.ToolChoiceAuto:
		return genai.FunctionCallingConfigModeAuto, nil
	case openresponses.ToolChoiceNone:
		return genai.FunctionCallingConfigModeNone, nil
	case openresponses.ToolChoiceRequired:
		return genai.FunctionCallingConfigModeAny, nil
	}
	return "", invalid(param, fmt.Sprintf("unknown tool choice %q", m))
}

// encodeInput folds the items into alternating user and model turns.
// Everything the user side sends (messages, function outputs) joins one
// user turn; everything the model side produced (messages, function
// calls, reasoning, extension parts) joins one model turn, so a function
// response always directly follows the turn that called it. System and
// developer messages are returned separately for the system instruction.
//
// A reasoning item's signature goes back on the first non-thought part
// after it, which is where Gemini put it (see decoder.part). It is held
// until that part is added and gets a thought part of its own only when
// the model turn ends without one.
func encodeInput(items openresponses.Items) (contents []*genai.Content, system []*genai.Part, err error) {
	names := map[string]string{}
	for _, item := range items {
		if fc, ok := item.(*openresponses.FunctionCall); ok {
			names[fc.CallID] = fc.Name
		}
	}
	var pending []byte
	appendParts := func(role string, parts []*genai.Part) {
		if n := len(contents); n > 0 && contents[n-1].Role == role {
			contents[n-1].Parts = append(contents[n-1].Parts, parts...)
			return
		}
		contents = append(contents, &genai.Content{Role: role, Parts: parts})
	}
	flush := func() {
		if pending != nil {
			appendParts(genai.RoleModel, []*genai.Part{{Thought: true, ThoughtSignature: pending}})
			pending = nil
		}
	}
	add := func(role string, parts []*genai.Part) {
		if len(parts) == 0 {
			return
		}
		switch {
		case role != genai.RoleModel:
			flush()
		case pending != nil && !parts[0].Thought:
			if len(parts[0].ThoughtSignature) > 0 {
				flush()
			} else {
				parts[0].ThoughtSignature, pending = pending, nil
			}
		}
		appendParts(role, parts)
	}
	for i, item := range items {
		param := fmt.Sprintf("input[%d]", i)
		switch it := item.(type) {
		case *openresponses.Message:
			parts, err := encodeParts(it.Content, param+".content")
			if err != nil {
				return nil, nil, err
			}
			switch it.Role {
			case openresponses.RoleUser:
				add(genai.RoleUser, parts)
			case openresponses.RoleAssistant:
				add(genai.RoleModel, parts)
			case openresponses.RoleSystem, openresponses.RoleDeveloper:
				for j, p := range parts {
					if p.Text == "" {
						return nil, nil, unsupported(fmt.Sprintf("%s.content[%d].type", param, j))
					}
				}
				system = append(system, parts...)
			default:
				return nil, nil, invalid(param+".role", fmt.Sprintf("unknown role %q", it.Role))
			}
		case *openresponses.FunctionCall:
			args, err := decodeArguments(it.Arguments)
			if err != nil {
				return nil, nil, invalid(param+".arguments", "arguments is not a JSON object: "+err.Error())
			}
			add(genai.RoleModel, []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: it.CallID, Name: it.Name, Args: args}}})
		case *openresponses.FunctionCallOutput:
			name, ok := names[it.CallID]
			if !ok {
				// Gemini matches a response to its call by name, and only the
				// call carries it.
				return nil, nil, invalid(param+".call_id", fmt.Sprintf("no function_call with call_id %q precedes this output", it.CallID))
			}
			part, err := encodeFunctionOutput(it, name, param+".output")
			if err != nil {
				return nil, nil, err
			}
			add(genai.RoleUser, []*genai.Part{part})
		case *openresponses.ReasoningItem:
			var sig []byte
			if it.EncryptedContent != "" {
				sig, err = base64.StdEncoding.DecodeString(it.EncryptedContent)
				if err != nil {
					return nil, nil, invalid(param+".encrypted_content", "encrypted_content is not base64: "+err.Error())
				}
			}
			if text := reasoningText(it); text != "" {
				add(genai.RoleModel, []*genai.Part{{Thought: true, Text: text}})
			}
			if sig != nil {
				flush()
				pending = sig
			}
		case *openresponses.UnknownItem:
			part, err := extensionPart(it)
			if err != nil {
				return nil, nil, invalid(param, err.Error())
			}
			if part == nil {
				return nil, nil, unsupported(param + ".type")
			}
			add(genai.RoleModel, []*genai.Part{part})
		default:
			// Compaction and item_reference need state this adapter does not
			// keep.
			return nil, nil, unsupported(param + ".type")
		}
	}
	flush()
	return contents, system, nil
}

// reasoningText is the summary when there is one, else the content.
func reasoningText(r *openresponses.ReasoningItem) string {
	if text := r.Summary.Text(); text != "" {
		return text
	}
	return r.Content.Text()
}

func decodeArguments(s string) (map[string]any, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(s), &args); err != nil {
		return nil, err
	}
	return args, nil
}

// encodeFunctionOutput builds the function response. A text output that
// is itself a JSON object is passed as the response; any other text is
// wrapped as {"output": text}. Media parts ride along as response parts.
func encodeFunctionOutput(out *openresponses.FunctionCallOutput, name, param string) (*genai.Part, error) {
	var text []byte
	var media []*genai.FunctionResponsePart
	if out.Output.Parts == nil {
		text = []byte(out.Output.Text)
	} else {
		for j, part := range out.Output.Parts {
			p := fmt.Sprintf("%s[%d]", param, j)
			switch c := part.(type) {
			case *openresponses.Text:
				text = append(text, c.Text...)
			case *openresponses.InputText:
				text = append(text, c.Text...)
			case *openresponses.OutputText:
				text = append(text, c.Text...)
			case *openresponses.InputImage:
				blob, file, err := mediaSource(c.ImageURL, p+".image_url")
				if err != nil {
					return nil, err
				}
				media = append(media, responseMedia(blob, file))
			case *openresponses.InputFile:
				blob, file, err := fileSource(c, p)
				if err != nil {
					return nil, err
				}
				media = append(media, responseMedia(blob, file))
			default:
				return nil, unsupported(p + ".type")
			}
		}
	}
	response := map[string]any{"output": string(text)}
	if obj := map[string]any(nil); json.Unmarshal(text, &obj) == nil && obj != nil {
		response = obj
	}
	fr := &genai.FunctionResponse{ID: out.CallID, Name: name, Response: response, Parts: media}
	return &genai.Part{FunctionResponse: fr}, nil
}

func responseMedia(blob *genai.Blob, file *genai.FileData) *genai.FunctionResponsePart {
	if blob != nil {
		return &genai.FunctionResponsePart{InlineData: &genai.FunctionResponseBlob{MIMEType: blob.MIMEType, Data: blob.Data}}
	}
	return &genai.FunctionResponsePart{FileData: &genai.FunctionResponseFileData{MIMEType: file.MIMEType, FileURI: file.FileURI}}
}

// extensionPart decodes a gemini.* item back into the part it carried:
// the item is {"type": "gemini.<kind>", ..., "part": <genai part>}. It
// returns nil, nil for an item without the slug.
func extensionPart(u *openresponses.UnknownItem) (*genai.Part, error) {
	if !strings.HasPrefix(u.Type, slug) {
		return nil, nil
	}
	var wrapper struct {
		Part json.RawMessage `json:"part"`
	}
	if err := json.Unmarshal(u.Raw, &wrapper); err != nil {
		return nil, err
	}
	if len(wrapper.Part) == 0 {
		return nil, fmt.Errorf("%s item has no part", u.Type)
	}
	var part genai.Part
	if err := json.Unmarshal(wrapper.Part, &part); err != nil {
		return nil, fmt.Errorf("%s part: %w", u.Type, err)
	}
	return &part, nil
}

// encodeParts maps message content. Text of any flavour is a text part;
// images, files and video become inline data or a file reference.
func encodeParts(contents openresponses.Contents, param string) ([]*genai.Part, error) {
	parts := make([]*genai.Part, 0, len(contents))
	for j, content := range contents {
		p := fmt.Sprintf("%s[%d]", param, j)
		switch c := content.(type) {
		case *openresponses.InputText:
			parts = append(parts, genai.NewPartFromText(c.Text))
		case *openresponses.OutputText:
			parts = append(parts, genai.NewPartFromText(c.Text))
		case *openresponses.Text:
			parts = append(parts, genai.NewPartFromText(c.Text))
		case *openresponses.Refusal:
			parts = append(parts, genai.NewPartFromText(c.Refusal))
		case *openresponses.InputImage:
			if c.FileID != "" {
				return nil, unsupported(p + ".file_id")
			}
			blob, file, err := mediaSource(c.ImageURL, p+".image_url")
			if err != nil {
				return nil, err
			}
			part := mediaPart(blob, file)
			switch c.Detail {
			case "", openresponses.ImageDetailAuto:
			case openresponses.ImageDetailLow:
				part.MediaResolution = &genai.PartMediaResolution{Level: genai.PartMediaResolutionLevelMediaResolutionLow}
			case openresponses.ImageDetailHigh:
				part.MediaResolution = &genai.PartMediaResolution{Level: genai.PartMediaResolutionLevelMediaResolutionHigh}
			default:
				return nil, invalid(p+".detail", fmt.Sprintf("unknown image detail %q", c.Detail))
			}
			parts = append(parts, part)
		case *openresponses.InputFile:
			blob, file, err := fileSource(c, p)
			if err != nil {
				return nil, err
			}
			parts = append(parts, mediaPart(blob, file))
		case *openresponses.InputVideo:
			blob, file, err := mediaSource(c.VideoURL, p+".video_url")
			if err != nil {
				return nil, err
			}
			parts = append(parts, mediaPart(blob, file))
		default:
			return nil, unsupported(p + ".type")
		}
	}
	return parts, nil
}

func mediaPart(blob *genai.Blob, file *genai.FileData) *genai.Part {
	if blob != nil {
		return &genai.Part{InlineData: blob}
	}
	return &genai.Part{FileData: file}
}

// fileSource resolves an input_file. file_data is inline content, either
// a data URL or bare base64 typed by the filename; file_url is a
// reference; file_id names a resource this adapter cannot look up.
func fileSource(f *openresponses.InputFile, param string) (*genai.Blob, *genai.FileData, error) {
	switch {
	case f.FileID != "":
		return nil, nil, unsupported(param + ".file_id")
	case f.FileData != "":
		if strings.HasPrefix(f.FileData, "data:") {
			return mediaSource(f.FileData, param+".file_data")
		}
		data, err := base64.StdEncoding.DecodeString(f.FileData)
		if err != nil {
			return nil, nil, invalid(param+".file_data", "file_data is not base64: "+err.Error())
		}
		mt := typeByExtension(f.Filename)
		if mt == "" {
			return nil, nil, invalid(param+".filename", "file_data needs a filename with a known extension, or a data URL with a media type")
		}
		return &genai.Blob{MIMEType: mt, Data: data, DisplayName: f.Filename}, nil, nil
	case f.FileURL != "":
		blob, file, err := mediaSource(f.FileURL, param+".file_url")
		if err != nil {
			return nil, nil, err
		}
		if file != nil && f.Filename != "" {
			file.DisplayName = f.Filename
		}
		return blob, file, nil
	}
	return nil, nil, openresponses.InvalidRequest(openresponses.CodeMissingRequiredParameter,
		"input_file needs file_data, file_url or file_id", param)
}

// mediaSource turns a URL into inline data (a data URL) or a file
// reference (anything else). Which references a backend can read, gs://
// on Vertex AI, Files API and YouTube URLs on the Gemini API, is for the
// backend to say; the adapter passes the URL through and the media type
// it can infer from the path.
func mediaSource(raw, param string) (*genai.Blob, *genai.FileData, error) {
	if strings.HasPrefix(raw, "data:") {
		mt, data, err := parseDataURL(raw)
		if err != nil {
			return nil, nil, invalid(param, err.Error())
		}
		return &genai.Blob{MIMEType: mt, Data: data}, nil, nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return nil, nil, invalid(param, "not a URL: "+raw)
	}
	return nil, &genai.FileData{FileURI: raw, MIMEType: typeByExtension(u.Path)}, nil
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
