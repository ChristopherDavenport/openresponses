package openresponses

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientCreate(t *testing.T) {
	var gotAuth, gotAccept, gotUA, gotExtra string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		gotUA = r.Header.Get("User-Agent")
		gotExtra = r.Header.Get("X-Extra")
		if r.URL.Path != "/v1/responses" || r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		var req Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
		}
		if req.Stream {
			t.Error("Create must send stream=false")
		}
		resp := sampleResponse()
		resp.Model = req.Model
		writeJSON(w, http.StatusOK, resp)
	}))
	defer srv.Close()

	c := NewClient(srv.URL+"/v1/", WithAPIKey("k"), WithHeader("X-Extra", "1"), WithUserAgent("t/1"))
	resp, err := c.Create(context.Background(), Request{Model: "m", Input: Items{UserText("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Model != "m" || resp.OutputText() != "hi" {
		t.Errorf("resp = %+v", resp)
	}
	if gotAuth != "Bearer k" || gotAccept != "application/json" || gotUA != "t/1" || gotExtra != "1" {
		t.Errorf("headers auth=%q accept=%q ua=%q extra=%q", gotAuth, gotAccept, gotUA, gotExtra)
	}
}

func TestClientAuthHeaderOption(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-Api-Key")
		writeJSON(w, http.StatusOK, sampleResponse())
	}))
	defer srv.Close()
	c := NewClient(srv.URL, WithAPIKey("k"), WithAuthHeader("X-Api-Key", ""))
	if _, err := c.Create(context.Background(), Request{Model: "m"}); err != nil {
		t.Fatal(err)
	}
	if got != "k" {
		t.Errorf("X-Api-Key = %q", got)
	}
}

func TestClientErrors(t *testing.T) {
	tests := []struct {
		name     string
		status   int
		body     string
		wantType ErrorType
		wantCode string
	}{
		{"400 envelope", 400, `{"error":{"type":"invalid_request","code":"bad","message":"nope","param":"model"}}`, ErrorTypeInvalidRequest, "bad"},
		{"404 envelope", 404, `{"error":{"type":"not_found","code":"previous_response_not_found","message":"gone","param":null}}`, ErrorTypeNotFound, CodePreviousResponseNotFound},
		{"429 envelope", 429, `{"error":{"type":"too_many_requests","code":"rate","message":"slow","param":null}}`, ErrorTypeTooManyRequests, "rate"},
		{"500 envelope", 500, `{"error":{"type":"server_error","code":"boom","message":"bad","param":null}}`, ErrorTypeServerError, "boom"},
		{"non-json body", 502, `<html>bad gateway</html>`, ErrorTypeServerError, ""},
		{"envelope without type", 404, `{"error":{"code":"x","message":"m"}}`, ErrorTypeNotFound, "x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Request-Id", "req_1")
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer srv.Close()
			c := NewClient(srv.URL)
			_, err := c.Create(context.Background(), Request{Model: "m"})
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("err = %v", err)
			}
			if e.StatusCode != tt.status || e.Type != tt.wantType || e.Code != tt.wantCode {
				t.Errorf("got %+v", e)
			}
			if e.Headers.Get("X-Request-Id") != "req_1" {
				t.Errorf("headers = %v", e.Headers)
			}
			if tt.wantCode == "" && string(e.Body) != tt.body {
				t.Errorf("body = %q", e.Body)
			}
		})
	}
}

func TestClientCreateStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !req.Stream {
			t.Errorf("stream flag missing: %v", err)
		}
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("Accept = %q", r.Header.Get("Accept"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, goldenStream(t))
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	stream, err := c.CreateStream(context.Background(), Request{Model: "m", Input: Items{UserText("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	var text strings.Builder
	for ev := range stream.Events() {
		if d, ok := ev.(*OutputTextDeltaEvent); ok {
			text.WriteString(d.Delta)
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	if text.String() != "Hello, world" {
		t.Errorf("text = %q", text.String())
	}
	if stream.Response().Status != ResponseStatusCompleted {
		t.Errorf("status = %s", stream.Response().Status)
	}
}

func TestClientCreateStreamJSONFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, sampleResponse())
	}))
	defer srv.Close()
	stream, err := NewClient(srv.URL).CreateStream(context.Background(), Request{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	resp, err := stream.Wait()
	if err != nil || resp.OutputText() != "hi" {
		t.Errorf("resp = %+v, err = %v", resp, err)
	}
}

func TestClientCreateStreamCancel(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":0,\"response\":{\"id\":\"r\"}}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer srv.Close()
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := NewClient(srv.URL).CreateStream(ctx, Request{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	if !stream.Next() {
		t.Fatalf("no first event: %v", stream.Err())
	}
	cancel()
	if stream.Next() {
		t.Fatal("expected stream to end")
	}
	if !errors.Is(stream.Err(), context.Canceled) {
		t.Errorf("err = %v", stream.Err())
	}
}

func TestClientCompact(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses/compact" {
			t.Errorf("path = %s", r.URL.Path)
		}
		writeJSON(w, http.StatusOK, &CompactResponse{ID: "c", CreatedAt: 1, Output: Items{&Compaction{ID: "cmp", EncryptedContent: "e"}}})
	}))
	defer srv.Close()
	resp, err := NewClient(srv.URL).Compact(context.Background(), CompactRequest{Model: "m", Input: Items{UserText("x")}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Object != ObjectCompaction || len(resp.Output) != 1 {
		t.Errorf("resp = %+v", resp)
	}
}

func TestClientMiddleware(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Trace") != "yes" {
			t.Error("middleware header missing")
		}
		writeJSON(w, http.StatusOK, sampleResponse())
	}))
	defer srv.Close()
	c := NewClient(srv.URL, WithMiddleware(func(next http.RoundTripper) http.RoundTripper {
		return roundTripFunc(func(r *http.Request) (*http.Response, error) {
			r.Header.Set("X-Trace", "yes")
			return next.RoundTrip(r)
		})
	}), WithHTTPClient(&http.Client{Timeout: 5 * time.Second}))
	if _, err := c.Create(context.Background(), Request{Model: "m"}); err != nil {
		t.Fatal(err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
