package openresponses

import (
	"encoding/json"
	"fmt"

	"github.com/christopherdavenport/openresponses/internal/jsonx"
)

// Content is a content part inside a message, a function call output or
// a reasoning item. Concrete types are [InputText], [InputImage],
// [InputFile], [InputVideo], [OutputText], [Refusal], [Text],
// [SummaryText], [ReasoningText] and [UnknownContent]. Decoded values are
// always pointers, so switch on *InputText and so on.
type Content interface {
	ContentType() string
}

// Contents is a list of content parts that decodes through the content
// registry.
type Contents []Content

// UnmarshalJSON decodes each element through [UnmarshalContent].
func (c *Contents) UnmarshalJSON(data []byte) error {
	var raws []json.RawMessage
	if err := json.Unmarshal(data, &raws); err != nil {
		return err
	}
	out := make(Contents, 0, len(raws))
	for i, raw := range raws {
		part, err := UnmarshalContent(raw)
		if err != nil {
			return fmt.Errorf("content[%d]: %w", i, err)
		}
		out = append(out, part)
	}
	*c = out
	return nil
}

// Text concatenates the text of every textual part.
func (c Contents) Text() string {
	var out []byte
	for _, part := range c {
		switch p := part.(type) {
		case *InputText:
			out = append(out, p.Text...)
		case *OutputText:
			out = append(out, p.Text...)
		case *Text:
			out = append(out, p.Text...)
		case *SummaryText:
			out = append(out, p.Text...)
		case *ReasoningText:
			out = append(out, p.Text...)
		}
	}
	return string(out)
}

// InputText is a text part of a user, system or developer message.
type InputText struct {
	Text string `json:"text"`
}

// ContentType returns "input_text".
func (*InputText) ContentType() string { return ContentTypeInputText }

// MarshalJSON emits the part with its type discriminator.
func (p *InputText) MarshalJSON() ([]byte, error) {
	type plain InputText
	return jsonx.MarshalTyped(ContentTypeInputText, (*plain)(p))
}

// InputImage is an image part of a user message. ImageURL is a fully
// qualified URL or a base64 data URL.
type InputImage struct {
	ImageURL string      `json:"image_url"`
	Detail   ImageDetail `json:"detail,omitempty"`
	FileID   string      `json:"file_id,omitempty"`
}

// ContentType returns "input_image".
func (*InputImage) ContentType() string { return ContentTypeInputImage }

// MarshalJSON emits the part with its type discriminator. The resource
// form of the spec requires detail, so an unset Detail is emitted as
// "auto".
func (p *InputImage) MarshalJSON() ([]byte, error) {
	type plain InputImage
	cp := plain(*p)
	if cp.Detail == "" {
		cp.Detail = ImageDetailAuto
	}
	return jsonx.MarshalTyped(ContentTypeInputImage, cp)
}

// InputFile is a file part of a user message. Exactly one of FileData
// (base64), FileURL or FileID should be set.
type InputFile struct {
	Filename string `json:"filename,omitempty"`
	FileData string `json:"file_data,omitempty"`
	FileURL  string `json:"file_url,omitempty"`
	FileID   string `json:"file_id,omitempty"`
}

// ContentType returns "input_file".
func (*InputFile) ContentType() string { return ContentTypeInputFile }

// MarshalJSON emits the part with its type discriminator.
func (p *InputFile) MarshalJSON() ([]byte, error) {
	type plain InputFile
	return jsonx.MarshalTyped(ContentTypeInputFile, (*plain)(p))
}

// InputVideo is a video part of a user message.
type InputVideo struct {
	VideoURL string `json:"video_url"`
}

// ContentType returns "input_video".
func (*InputVideo) ContentType() string { return ContentTypeInputVideo }

// MarshalJSON emits the part with its type discriminator.
func (p *InputVideo) MarshalJSON() ([]byte, error) {
	type plain InputVideo
	return jsonx.MarshalTyped(ContentTypeInputVideo, (*plain)(p))
}

// OutputText is a text part of an assistant message.
type OutputText struct {
	Text        string      `json:"text"`
	Annotations Annotations `json:"annotations"`
	Logprobs    []LogProb   `json:"logprobs,omitempty"`
}

// ContentType returns "output_text".
func (*OutputText) ContentType() string { return ContentTypeOutputText }

// MarshalJSON emits the part with its type discriminator. Annotations is
// always emitted as an array because the spec requires it.
func (p *OutputText) MarshalJSON() ([]byte, error) {
	type plain OutputText
	cp := plain(*p)
	if cp.Annotations == nil {
		cp.Annotations = Annotations{}
	}
	return jsonx.MarshalTyped(ContentTypeOutputText, cp)
}

// Refusal is a refusal part of an assistant message.
type Refusal struct {
	Refusal string `json:"refusal"`
}

// ContentType returns "refusal".
func (*Refusal) ContentType() string { return ContentTypeRefusal }

// MarshalJSON emits the part with its type discriminator.
func (p *Refusal) MarshalJSON() ([]byte, error) {
	type plain Refusal
	return jsonx.MarshalTyped(ContentTypeRefusal, (*plain)(p))
}

// Text is a plain text part, used inside function call outputs.
type Text struct {
	Text string `json:"text"`
}

// ContentType returns "text".
func (*Text) ContentType() string { return ContentTypeText }

// MarshalJSON emits the part with its type discriminator.
func (p *Text) MarshalJSON() ([]byte, error) {
	type plain Text
	return jsonx.MarshalTyped(ContentTypeText, (*plain)(p))
}

// SummaryText is a part of a reasoning item's summary.
type SummaryText struct {
	Text string `json:"text"`
}

// ContentType returns "summary_text".
func (*SummaryText) ContentType() string { return ContentTypeSummaryText }

// MarshalJSON emits the part with its type discriminator.
func (p *SummaryText) MarshalJSON() ([]byte, error) {
	type plain SummaryText
	return jsonx.MarshalTyped(ContentTypeSummaryText, (*plain)(p))
}

// ReasoningText is a part of a reasoning item's content.
type ReasoningText struct {
	Text string `json:"text"`
}

// ContentType returns "reasoning_text".
func (*ReasoningText) ContentType() string { return ContentTypeReasoningText }

// MarshalJSON emits the part with its type discriminator.
func (p *ReasoningText) MarshalJSON() ([]byte, error) {
	type plain ReasoningText
	return jsonx.MarshalTyped(ContentTypeReasoningText, (*plain)(p))
}

// UnknownContent is a content part whose type is not registered. Raw holds
// the original bytes and is re-emitted verbatim.
type UnknownContent struct {
	Type string
	Raw  json.RawMessage
}

// ContentType returns the wire type.
func (p *UnknownContent) ContentType() string { return p.Type }

// MarshalJSON emits the original bytes.
func (p *UnknownContent) MarshalJSON() ([]byte, error) {
	if len(p.Raw) == 0 {
		return jsonx.MarshalTyped(p.Type, struct{}{})
	}
	return p.Raw, nil
}

// UnmarshalJSON records the type and the raw bytes.
func (p *UnknownContent) UnmarshalJSON(data []byte) error {
	typ, err := jsonx.PeekType(data)
	if err != nil {
		return err
	}
	p.Type = typ
	p.Raw = append(json.RawMessage(nil), data...)
	return nil
}

// LogProb is the log probability of a generated token.
type LogProb struct {
	Token       string       `json:"token"`
	Logprob     float64      `json:"logprob"`
	Bytes       []int        `json:"bytes"`
	TopLogprobs []TopLogProb `json:"top_logprobs"`
}

// MarshalJSON emits required arrays as [] rather than null.
func (l LogProb) MarshalJSON() ([]byte, error) {
	type plain LogProb
	cp := plain(l)
	if cp.Bytes == nil {
		cp.Bytes = []int{}
	}
	if cp.TopLogprobs == nil {
		cp.TopLogprobs = []TopLogProb{}
	}
	return json.Marshal(cp)
}

// TopLogProb is one of the most likely alternatives at a token position.
type TopLogProb struct {
	Token   string  `json:"token"`
	Logprob float64 `json:"logprob"`
	Bytes   []int   `json:"bytes"`
}

// MarshalJSON emits Bytes as [] rather than null.
func (l TopLogProb) MarshalJSON() ([]byte, error) {
	type plain TopLogProb
	cp := plain(l)
	if cp.Bytes == nil {
		cp.Bytes = []int{}
	}
	return json.Marshal(cp)
}

// Annotation is an annotation attached to an output text part. The only
// standard annotation is [URLCitation]; everything else decodes to
// [UnknownAnnotation].
type Annotation interface {
	AnnotationType() string
}

// Annotations is a list of annotations that decodes through the
// annotation registry.
type Annotations []Annotation

// UnmarshalJSON decodes each element through [UnmarshalAnnotation].
func (a *Annotations) UnmarshalJSON(data []byte) error {
	var raws []json.RawMessage
	if err := json.Unmarshal(data, &raws); err != nil {
		return err
	}
	out := make(Annotations, 0, len(raws))
	for i, raw := range raws {
		ann, err := UnmarshalAnnotation(raw)
		if err != nil {
			return fmt.Errorf("annotations[%d]: %w", i, err)
		}
		out = append(out, ann)
	}
	*a = out
	return nil
}

// URLCitation cites a URL for a span of output text.
type URLCitation struct {
	URL        string `json:"url"`
	Title      string `json:"title"`
	StartIndex int    `json:"start_index"`
	EndIndex   int    `json:"end_index"`
}

// AnnotationType returns "url_citation".
func (*URLCitation) AnnotationType() string { return AnnotationTypeURLCitation }

// MarshalJSON emits the annotation with its type discriminator.
func (a *URLCitation) MarshalJSON() ([]byte, error) {
	type plain URLCitation
	return jsonx.MarshalTyped(AnnotationTypeURLCitation, (*plain)(a))
}

// UnknownAnnotation is an annotation whose type is not registered.
type UnknownAnnotation struct {
	Type string
	Raw  json.RawMessage
}

// AnnotationType returns the wire type.
func (a *UnknownAnnotation) AnnotationType() string { return a.Type }

// MarshalJSON emits the original bytes.
func (a *UnknownAnnotation) MarshalJSON() ([]byte, error) {
	if len(a.Raw) == 0 {
		return jsonx.MarshalTyped(a.Type, struct{}{})
	}
	return a.Raw, nil
}

// UnmarshalJSON records the type and the raw bytes.
func (a *UnknownAnnotation) UnmarshalJSON(data []byte) error {
	typ, err := jsonx.PeekType(data)
	if err != nil {
		return err
	}
	a.Type = typ
	a.Raw = append(json.RawMessage(nil), data...)
	return nil
}

var (
	_ Content = (*InputText)(nil)
	_ Content = (*InputImage)(nil)
	_ Content = (*InputFile)(nil)
	_ Content = (*InputVideo)(nil)
	_ Content = (*OutputText)(nil)
	_ Content = (*Refusal)(nil)
	_ Content = (*Text)(nil)
	_ Content = (*SummaryText)(nil)
	_ Content = (*ReasoningText)(nil)
	_ Content = (*UnknownContent)(nil)

	_ Annotation = (*URLCitation)(nil)
	_ Annotation = (*UnknownAnnotation)(nil)
)
