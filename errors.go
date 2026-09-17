package openresponses

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// ErrorType classifies an error on the wire and maps to an HTTP status.
type ErrorType string

// Error types defined by the specification.
const (
	ErrorTypeInvalidRequest  ErrorType = "invalid_request"
	ErrorTypeNotFound        ErrorType = "not_found"
	ErrorTypeTooManyRequests ErrorType = "too_many_requests"
	ErrorTypeModelError      ErrorType = "model_error"
	ErrorTypeServerError     ErrorType = "server_error"
)

// Error codes the specification names explicitly.
const (
	CodePreviousResponseNotFound        = "previous_response_not_found"
	CodeWebSocketConnectionLimitReached = "websocket_connection_limit_reached"
	CodeInvalidValue                    = "invalid_value"
	CodeMissingRequiredParameter        = "missing_required_parameter"
	CodeUnsupportedParameter            = "unsupported_parameter"
	CodeStreamingNotSupported           = "streaming_not_supported"
	CodeCompactionNotSupported          = "compaction_not_supported"
)

// HTTPStatus returns the HTTP status for the error type. Unknown types map
// to 500.
func (t ErrorType) HTTPStatus() int {
	switch t {
	case ErrorTypeInvalidRequest, "invalid_request_error":
		return http.StatusBadRequest
	case ErrorTypeNotFound:
		return http.StatusNotFound
	case ErrorTypeTooManyRequests:
		return http.StatusTooManyRequests
	default:
		return http.StatusInternalServerError
	}
}

// ErrorPayload is the wire form of an error. It appears in the HTTP error
// envelope, in error streaming events, in the WebSocket error frame and in
// the error field of a failed response.
type ErrorPayload struct {
	Type    ErrorType         `json:"type,omitempty"`
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Param   string            `json:"param"`
	Headers map[string]string `json:"headers,omitempty"`
}

// MarshalJSON emits param as null when empty, as the spec requires the
// key to be present but nullable.
func (p ErrorPayload) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Type    ErrorType         `json:"type,omitempty"`
		Code    string            `json:"code"`
		Message string            `json:"message"`
		Param   *string           `json:"param"`
		Headers map[string]string `json:"headers,omitempty"`
	}{p.Type, p.Code, p.Message, nilIfEmpty(p.Param), p.Headers})
}

// UnmarshalJSON accepts null for code and param.
func (p *ErrorPayload) UnmarshalJSON(data []byte) error {
	var aux struct {
		Type    ErrorType         `json:"type"`
		Code    *string           `json:"code"`
		Message string            `json:"message"`
		Param   *string           `json:"param"`
		Headers map[string]string `json:"headers"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	*p = ErrorPayload{Type: aux.Type, Message: aux.Message, Headers: aux.Headers}
	if aux.Code != nil {
		p.Code = *aux.Code
	}
	if aux.Param != nil {
		p.Param = *aux.Param
	}
	return nil
}

// Error is a spec-shaped error. Clients receive it for non-2xx responses
// and for error events; adapters return it to have the handler emit the
// right envelope and status.
type Error struct {
	// StatusCode is the HTTP status. When zero, Type decides the status.
	StatusCode int
	Type       ErrorType
	Code       string
	Message    string
	Param      string

	// Headers are HTTP headers that belong to the error and travel with
	// it: a server writes them to the HTTP response and carries them in
	// the error event payload. An error built from a remote source (an
	// HTTP error response, an error event or a WebSocket error frame)
	// only picks up the headers that describe a failure: Retry-After,
	// RateLimit-* and X-RateLimit-*, X-Request-Id and Request-Id.
	// Forwarding an *Error therefore keeps Retry-After from an upstream
	// 429 intact, while a peer cannot plant Set-Cookie or a CORS header
	// on a server that forwards its error. Errors built locally carry
	// whatever WithHeader adds.
	Headers http.Header
	// ResponseHeaders holds every header of the failing HTTP response
	// when the error came from one. It is never forwarded.
	ResponseHeaders http.Header
	// Body holds the raw response body when it was not a spec envelope.
	Body []byte
}

// errorHeader reports whether an HTTP response header describes an error
// and should travel with it.
func errorHeader(name string) bool {
	name = http.CanonicalHeaderKey(name)
	switch name {
	case "Retry-After", "X-Request-Id", "Request-Id":
		return true
	}
	return strings.HasPrefix(name, "Ratelimit-") || strings.HasPrefix(name, "X-Ratelimit-")
}

// Error implements the error interface.
func (e *Error) Error() string {
	status := e.HTTPStatus()
	switch {
	case e.Code != "" && e.Message != "":
		return fmt.Sprintf("openresponses: %s (%d): %s: %s", e.Type, status, e.Code, e.Message)
	case e.Message != "":
		return fmt.Sprintf("openresponses: %s (%d): %s", e.Type, status, e.Message)
	default:
		return fmt.Sprintf("openresponses: %s (%d)", e.Type, status)
	}
}

// HTTPStatus returns StatusCode, or the status derived from Type.
func (e *Error) HTTPStatus() int {
	if e.StatusCode != 0 {
		return e.StatusCode
	}
	return e.Type.HTTPStatus()
}

// Payload returns the wire form of the error.
func (e *Error) Payload() ErrorPayload {
	typ := e.Type
	if typ == "" {
		typ = ErrorTypeServerError
	}
	payload := ErrorPayload{Type: typ, Code: e.Code, Message: e.Message, Param: e.Param}
	if len(e.Headers) > 0 {
		payload.Headers = make(map[string]string, len(e.Headers))
		for k, vs := range e.Headers {
			payload.Headers[k] = strings.Join(vs, ", ")
		}
	}
	return payload
}

// WithHeader returns e with the header added, for chaining:
//
//	return openresponses.TooManyRequests("rate_limited", msg).WithHeader("Retry-After", "30")
func (e *Error) WithHeader(name, value string) *Error {
	if e.Headers == nil {
		e.Headers = http.Header{}
	}
	e.Headers.Add(name, value)
	return e
}

// Err converts a wire payload into an *Error. Only the headers that
// describe a failure are carried over; see [Error.Headers].
func (p ErrorPayload) Err(status int) *Error {
	e := &Error{StatusCode: status, Type: p.Type, Code: p.Code, Message: p.Message, Param: p.Param}
	for k, v := range p.Headers {
		if errorHeader(k) {
			e.WithHeader(k, v)
		}
	}
	return e
}

// Is reports whether target is an *Error with the same Type and, when
// target sets one, the same Code. This lets callers write
// errors.Is(err, &Error{Type: ErrorTypeNotFound}).
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	if !ok {
		return false
	}
	if t.Type != "" && t.Type != e.Type {
		return false
	}
	if t.Code != "" && t.Code != e.Code {
		return false
	}
	if t.StatusCode != 0 && t.StatusCode != e.HTTPStatus() {
		return false
	}
	return true
}

// NewError builds an error of the given type.
func NewError(typ ErrorType, code, message string) *Error {
	return &Error{Type: typ, Code: code, Message: message}
}

// InvalidRequest builds an invalid_request error. param names the
// offending field and may be empty.
func InvalidRequest(code, message, param string) *Error {
	return &Error{Type: ErrorTypeInvalidRequest, Code: code, Message: message, Param: param}
}

// NotFound builds a not_found error. param names the field that referred
// to the missing resource and may be empty.
func NotFound(code, message, param string) *Error {
	return &Error{Type: ErrorTypeNotFound, Code: code, Message: message, Param: param}
}

// PreviousResponseNotFound builds the not_found error the spec defines
// for an unavailable previous_response_id.
func PreviousResponseNotFound(id string) *Error {
	return NotFound(CodePreviousResponseNotFound,
		fmt.Sprintf("previous response %q is not available", id), "previous_response_id")
}

// TooManyRequests builds a too_many_requests error.
func TooManyRequests(code, message string) *Error {
	return &Error{Type: ErrorTypeTooManyRequests, Code: code, Message: message}
}

// ModelError builds a model_error, for failures reported by the model
// provider.
func ModelError(code, message string) *Error {
	return &Error{Type: ErrorTypeModelError, Code: code, Message: message}
}

// ServerError builds a server_error.
func ServerError(code, message string) *Error {
	return &Error{Type: ErrorTypeServerError, Code: code, Message: message}
}

// errorEnvelope is the body of a non-2xx HTTP response.
type errorEnvelope struct {
	Error ErrorPayload `json:"error"`
}

// AsError converts any error into an
// *Error. Errors that already are *Error pass through; errors implementing
// HTTPStatus() int keep their status; everything else becomes a
// server_error with the error text as message.
func AsError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	out := &Error{Type: ErrorTypeServerError, Code: "internal_error", Message: err.Error()}
	var st interface{ HTTPStatus() int }
	if errors.As(err, &st) {
		out.StatusCode = st.HTTPStatus()
		switch out.StatusCode {
		case http.StatusBadRequest:
			out.Type = ErrorTypeInvalidRequest
		case http.StatusNotFound:
			out.Type = ErrorTypeNotFound
		case http.StatusTooManyRequests:
			out.Type = ErrorTypeTooManyRequests
		}
	}
	return out
}

// IsNotFound reports whether err is a not_found error.
func IsNotFound(err error) bool {
	return errors.Is(err, &Error{Type: ErrorTypeNotFound})
}

// IsRateLimited reports whether err is a too_many_requests error.
func IsRateLimited(err error) bool {
	return errors.Is(err, &Error{Type: ErrorTypeTooManyRequests})
}

// IsInvalidRequest reports whether err is an invalid_request error.
func IsInvalidRequest(err error) bool {
	return errors.Is(err, &Error{Type: ErrorTypeInvalidRequest})
}

// Sentinel errors returned by streams and connections.
var (
	// ErrTruncatedStream is returned when a stream ends without a [DONE]
	// sentinel or a terminal response event.
	ErrTruncatedStream = errors.New("openresponses: stream ended before a terminal event")
	// ErrStreamClosed is returned when sending on a closed stream or
	// connection.
	ErrStreamClosed = errors.New("openresponses: stream closed")
	// ErrTerminalEventSent is returned when an adapter emits an event after
	// a terminal response event.
	ErrTerminalEventSent = errors.New("openresponses: terminal event already sent")
)
