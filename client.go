package openresponses

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client talks to an Open Responses server over HTTP, SSE and WebSocket.
// Create one and share it; it is safe for concurrent use.
type Client struct {
	baseURL    string
	apiKey     string
	authHeader string
	authPrefix string
	headers    http.Header
	http       *http.Client
	userAgent  string
	middleware []func(http.RoundTripper) http.RoundTripper

	maxResponseBytes int64
}

// ClientOption configures a Client.
type ClientOption func(*Client)

// WithAPIKey sends the key as "Authorization: Bearer <key>".
func WithAPIKey(key string) ClientOption {
	return func(c *Client) { c.apiKey = key }
}

// WithAuthHeader changes the header and value prefix used for the API
// key. The defaults are "Authorization" and "Bearer ".
func WithAuthHeader(name, prefix string) ClientOption {
	return func(c *Client) {
		c.authHeader = name
		c.authPrefix = prefix
	}
}

// WithHTTPClient replaces the underlying HTTP client. The default has no
// overall timeout, so that long streams are not cut off, but does time
// out waiting for response headers.
func WithHTTPClient(hc *http.Client) ClientOption {
	return func(c *Client) { c.http = hc }
}

// WithMaxResponseBytes caps the size of a JSON response body and of a
// WebSocket frame the client will accept. The default is 64 MiB. A body
// over the cap fails with an error rather than growing memory without
// bound; SSE frames have a separate fixed cap of 16 MiB.
func WithMaxResponseBytes(n int64) ClientOption {
	return func(c *Client) { c.maxResponseBytes = n }
}

// WithHeader adds a header to every request.
func WithHeader(name, value string) ClientOption {
	return func(c *Client) { c.headers.Add(name, value) }
}

// WithUserAgent sets the User-Agent header.
func WithUserAgent(ua string) ClientOption {
	return func(c *Client) { c.userAgent = ua }
}

// WithMiddleware wraps the client's transport, for retries, logging or
// tracing. Middleware is applied after all other options, so it wraps
// the transport of a client supplied with [WithHTTPClient] too; later
// middleware wraps earlier middleware.
func WithMiddleware(mw func(http.RoundTripper) http.RoundTripper) ClientOption {
	return func(c *Client) { c.middleware = append(c.middleware, mw) }
}

// NewClient returns a client for the server at baseURL, for example
// "https://api.openai.com/v1". Endpoint paths are appended to it.
func NewClient(baseURL string, opts ...ClientOption) *Client {
	c := &Client{
		baseURL:          strings.TrimRight(baseURL, "/"),
		authHeader:       "Authorization",
		authPrefix:       "Bearer ",
		headers:          http.Header{},
		http:             &http.Client{Transport: defaultTransport()},
		userAgent:        "openresponses-go/" + SpecVersion,
		maxResponseBytes: 64 << 20,
	}
	for _, opt := range opts {
		opt(c)
	}
	if len(c.middleware) > 0 {
		hc := *c.http
		rt := hc.Transport
		if rt == nil {
			rt = http.DefaultTransport
		}
		for _, mw := range c.middleware {
			rt = mw(rt)
		}
		hc.Transport = rt
		c.http = &hc
	}
	return c
}

// defaultTransport clones http.DefaultTransport with a response header
// timeout. A program that replaced http.DefaultTransport with another
// RoundTripper gets that as it is.
func defaultTransport() http.RoundTripper {
	t, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return http.DefaultTransport
	}
	t = t.Clone()
	t.ResponseHeaderTimeout = 2 * time.Minute
	return t
}

// BaseURL returns the server the client talks to, without a trailing
// slash.
func (c *Client) BaseURL() string { return c.baseURL }

// Create sends a non-streaming request and returns the final response.
func (c *Client) Create(ctx context.Context, req Request) (*Response, error) {
	req.Stream = false
	res, err := c.post(ctx, "/responses", req, "application/json")
	if err != nil {
		return nil, err
	}
	defer drainAndClose(res.Body)
	var out Response
	if err := decodeJSON(res, &out, c.maxResponseBytes); err != nil {
		return nil, err
	}
	return &out, nil
}

// CreateStream sends a streaming request and returns the event stream.
// The caller must Close the stream.
func (c *Client) CreateStream(ctx context.Context, req Request) (*EventStream, error) {
	req.Stream = true
	res, err := c.post(ctx, "/responses", req, "text/event-stream")
	if err != nil {
		return nil, err
	}
	if ct, _, _ := mime.ParseMediaType(res.Header.Get("Content-Type")); ct != "text/event-stream" {
		// A JSON body on a streaming request is a server that ignored
		// stream=true; surface it as a completed stream of one response.
		defer drainAndClose(res.Body)
		var out Response
		if err := decodeJSON(res, &out, c.maxResponseBytes); err != nil {
			return nil, err
		}
		return synthesizedStream(ctx, &out), nil
	}
	return NewEventStream(ctx, res.Body), nil
}

// Compact sends a compaction request.
func (c *Client) Compact(ctx context.Context, req CompactRequest) (*CompactResponse, error) {
	res, err := c.post(ctx, "/responses/compact", req, "application/json")
	if err != nil {
		return nil, err
	}
	defer drainAndClose(res.Body)
	var out CompactResponse
	if err := decodeJSON(res, &out, c.maxResponseBytes); err != nil {
		return nil, err
	}
	return &out, nil
}

// post sends body as JSON and returns the response when its status is
// 2xx. Any other status is decoded into an *Error and the body is closed.
func (c *Client) post(ctx context.Context, path string, body any, accept string) (*http.Response, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("openresponses: encode request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("openresponses: build request: %w", err)
	}
	c.applyHeaders(httpReq.Header)
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", accept)
	res, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openresponses: %s %s: %w", http.MethodPost, path, err)
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		defer drainAndClose(res.Body)
		return nil, errorFromHTTP(res)
	}
	return res, nil
}

func (c *Client) applyHeaders(h http.Header) {
	for k, vs := range c.headers {
		for _, v := range vs {
			h.Add(k, v)
		}
	}
	if c.apiKey != "" {
		h.Set(c.authHeader, c.authPrefix+c.apiKey)
	}
	if c.userAgent != "" {
		h.Set("User-Agent", c.userAgent)
	}
}

// webSocketURL converts the base URL to the ws(s) scheme and appends
// /responses.
func (c *Client) webSocketURL() (string, error) {
	u, err := url.Parse(c.baseURL + "/responses")
	if err != nil {
		return "", fmt.Errorf("openresponses: parse base URL: %w", err)
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("openresponses: unsupported scheme %q for WebSocket", u.Scheme)
	}
	return u.String(), nil
}

// errorFromHTTP decodes a non-2xx response into an *Error. Bodies that
// are not a spec envelope are kept verbatim in Error.Body.
func errorFromHTTP(res *http.Response) *Error {
	body, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	out := &Error{StatusCode: res.StatusCode, ResponseHeaders: res.Header.Clone()}
	for name, values := range res.Header {
		if errorHeader(name) {
			if out.Headers == nil {
				out.Headers = http.Header{}
			}
			out.Headers[http.CanonicalHeaderKey(name)] = append([]string(nil), values...)
		}
	}
	var env errorEnvelope
	if err := json.Unmarshal(body, &env); err == nil && (env.Error.Message != "" || env.Error.Code != "" || env.Error.Type != "") {
		out.Type = env.Error.Type
		out.Code = env.Error.Code
		out.Message = env.Error.Message
		out.Param = env.Error.Param
	} else {
		out.Body = body
		out.Message = strings.TrimSpace(string(body))
		if out.Message == "" {
			out.Message = http.StatusText(res.StatusCode)
		}
	}
	if out.Type == "" {
		out.Type = errorTypeForStatus(res.StatusCode)
	}
	return out
}

func errorTypeForStatus(status int) ErrorType {
	switch status {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		return ErrorTypeInvalidRequest
	case http.StatusNotFound:
		return ErrorTypeNotFound
	case http.StatusTooManyRequests:
		return ErrorTypeTooManyRequests
	default:
		return ErrorTypeServerError
	}
}

// decodeJSON decodes a body of at most max bytes into v.
func decodeJSON(res *http.Response, v any, max int64) error {
	lr := &io.LimitedReader{R: res.Body, N: max + 1}
	if err := json.NewDecoder(lr).Decode(v); err != nil {
		if lr.N <= 0 {
			return fmt.Errorf("openresponses: response body exceeds %d bytes", max)
		}
		return fmt.Errorf("openresponses: decode response: %w", err)
	}
	return nil
}

func drainAndClose(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 1<<20))
	_ = body.Close()
}

// synthesizedStream builds a stream that yields a single terminal event
// for an already-complete response.
func synthesizedStream(ctx context.Context, resp *Response) *EventStream {
	var buf bytes.Buffer
	w := &sseWriter{w: &buf}
	var ev StreamEvent
	switch resp.Status {
	case ResponseStatusFailed:
		ev = &ResponseFailedEvent{Response: resp}
	case ResponseStatusIncomplete:
		ev = &ResponseIncompleteEvent{Response: resp}
	default:
		ev = &ResponseCompletedEvent{Response: resp}
	}
	data, err := EncodeEvent(ev)
	if err == nil {
		_ = w.WriteEvent(ev.EventType(), data)
		_ = w.WriteDone()
	}
	return NewEventStream(ctx, io.NopCloser(&buf))
}
