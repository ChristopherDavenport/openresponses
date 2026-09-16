package openresponses

// Status is the lifecycle status of an item. Terminal statuses are
// [StatusCompleted] and [StatusIncomplete]. Values are not validated on
// decode so a new provider value never breaks parsing.
type Status string

// Item statuses.
const (
	StatusInProgress Status = "in_progress"
	StatusCompleted  Status = "completed"
	StatusIncomplete Status = "incomplete"
)

// ResponseStatus is the lifecycle status of a response.
type ResponseStatus string

// Response statuses.
const (
	ResponseStatusQueued     ResponseStatus = "queued"
	ResponseStatusInProgress ResponseStatus = "in_progress"
	ResponseStatusCompleted  ResponseStatus = "completed"
	ResponseStatusIncomplete ResponseStatus = "incomplete"
	ResponseStatusFailed     ResponseStatus = "failed"
)

// Terminal reports whether the status ends a response.
func (s ResponseStatus) Terminal() bool {
	switch s {
	case ResponseStatusCompleted, ResponseStatusIncomplete, ResponseStatusFailed:
		return true
	}
	return false
}

// Role is the author of a message.
type Role string

// Message roles.
const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleSystem    Role = "system"
	RoleDeveloper Role = "developer"
)

// Phase labels an assistant message as intermediate commentary or the
// final answer.
type Phase string

// Assistant message phases.
const (
	PhaseCommentary  Phase = "commentary"
	PhaseFinalAnswer Phase = "final_answer"
)

// ReasoningEffort controls how much reasoning the model applies.
type ReasoningEffort string

// Reasoning effort levels. [ReasoningEffortMinimal] appears in
// descriptions but not in the enum; it is accepted for compatibility.
const (
	ReasoningEffortNone    ReasoningEffort = "none"
	ReasoningEffortMinimal ReasoningEffort = "minimal"
	ReasoningEffortLow     ReasoningEffort = "low"
	ReasoningEffortMedium  ReasoningEffort = "medium"
	ReasoningEffortHigh    ReasoningEffort = "high"
	ReasoningEffortXHigh   ReasoningEffort = "xhigh"
)

// ReasoningSummary controls whether a reasoning summary is produced.
type ReasoningSummary string

// Reasoning summary modes.
const (
	ReasoningSummaryConcise  ReasoningSummary = "concise"
	ReasoningSummaryDetailed ReasoningSummary = "detailed"
	ReasoningSummaryAuto     ReasoningSummary = "auto"
)

// Truncation selects the context-window overflow strategy.
type Truncation string

// Truncation strategies.
const (
	TruncationAuto     Truncation = "auto"
	TruncationDisabled Truncation = "disabled"
)

// ServiceTier selects the processing tier.
type ServiceTier string

// Service tiers.
const (
	ServiceTierAuto     ServiceTier = "auto"
	ServiceTierDefault  ServiceTier = "default"
	ServiceTierFlex     ServiceTier = "flex"
	ServiceTierPriority ServiceTier = "priority"
)

// Verbosity controls the level of detail in generated text.
type Verbosity string

// Verbosity levels.
const (
	VerbosityLow    Verbosity = "low"
	VerbosityMedium Verbosity = "medium"
	VerbosityHigh   Verbosity = "high"
)

// ImageDetail is the resolution an image is processed at.
type ImageDetail string

// Image detail levels.
const (
	ImageDetailLow  ImageDetail = "low"
	ImageDetailHigh ImageDetail = "high"
	ImageDetailAuto ImageDetail = "auto"
)

// Include names optional data to include in a response.
type Include string

// Include values.
const (
	IncludeReasoningEncryptedContent Include = "reasoning.encrypted_content"
	IncludeOutputTextLogprobs        Include = "message.output_text.logprobs"
)

// IncompleteReason explains why a response is incomplete.
type IncompleteReason string

// Incomplete reasons.
const (
	IncompleteReasonMaxOutputTokens IncompleteReason = "max_output_tokens"
	IncompleteReasonContentFilter   IncompleteReason = "content_filter"
)

// ToolChoiceMode is the string form of tool_choice.
type ToolChoiceMode string

// Tool choice modes.
const (
	ToolChoiceNone     ToolChoiceMode = "none"
	ToolChoiceAuto     ToolChoiceMode = "auto"
	ToolChoiceRequired ToolChoiceMode = "required"
)

// Wire discriminator values for items.
const (
	ItemTypeMessage            = "message"
	ItemTypeFunctionCall       = "function_call"
	ItemTypeFunctionCallOutput = "function_call_output"
	ItemTypeReasoning          = "reasoning"
	ItemTypeCompaction         = "compaction"
	ItemTypeItemReference      = "item_reference"
)

// Wire discriminator values for content parts.
const (
	ContentTypeInputText     = "input_text"
	ContentTypeInputImage    = "input_image"
	ContentTypeInputFile     = "input_file"
	ContentTypeInputVideo    = "input_video"
	ContentTypeOutputText    = "output_text"
	ContentTypeRefusal       = "refusal"
	ContentTypeText          = "text"
	ContentTypeSummaryText   = "summary_text"
	ContentTypeReasoningText = "reasoning_text"
)

// Wire discriminator values for tools, annotations and text formats.
const (
	ToolTypeFunction          = "function"
	AnnotationTypeURLCitation = "url_citation"
	TextFormatText            = "text"
	TextFormatJSONObject      = "json_object"
	TextFormatJSONSchema      = "json_schema"
)

// Wire discriminator values for streaming events.
const (
	EventResponseCreated            = "response.created"
	EventResponseQueued             = "response.queued"
	EventResponseInProgress         = "response.in_progress"
	EventResponseCompleted          = "response.completed"
	EventResponseFailed             = "response.failed"
	EventResponseIncomplete         = "response.incomplete"
	EventOutputItemAdded            = "response.output_item.added"
	EventOutputItemDone             = "response.output_item.done"
	EventContentPartAdded           = "response.content_part.added"
	EventContentPartDone            = "response.content_part.done"
	EventOutputTextDelta            = "response.output_text.delta"
	EventOutputTextDone             = "response.output_text.done"
	EventRefusalDelta               = "response.refusal.delta"
	EventRefusalDone                = "response.refusal.done"
	EventFunctionCallArgumentsDelta = "response.function_call_arguments.delta"
	EventFunctionCallArgumentsDone  = "response.function_call_arguments.done"
	EventReasoningSummaryPartAdded  = "response.reasoning_summary_part.added"
	EventReasoningSummaryPartDone   = "response.reasoning_summary_part.done"
	EventReasoningSummaryTextDelta  = "response.reasoning_summary_text.delta"
	EventReasoningSummaryTextDone   = "response.reasoning_summary_text.done"
	EventReasoningDelta             = "response.reasoning.delta"
	EventReasoningDone              = "response.reasoning.done"
	EventOutputTextAnnotationAdded  = "response.output_text.annotation.added"
	EventError                      = "error"
	EventWebSocketResponseCreate    = "response.create"
)
