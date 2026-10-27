package openaihttp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newTestClient(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := New("sk-test", srv.URL, 5*time.Second)
	c.RetryWait = time.Millisecond
	return c
}

func TestPostSendsAuthAndDecodes(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/things" || r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Errorf("path %s, auth %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		io.WriteString(w, `{"ok":true}`)
	})
	var out struct{ OK bool }
	if err := c.Post(context.Background(), "/things", map[string]int{"a": 1}, &out); err != nil || !out.OK {
		t.Fatalf("out = %+v, err = %v", out, err)
	}
}

func TestRetriesRateLimitThenSucceeds(t *testing.T) {
	calls := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			io.WriteString(w, `{"error":{"message":"slow down","type":"rate_limit_error"}}`)
			return
		}
		io.WriteString(w, `{}`)
	})
	if err := c.Post(context.Background(), "/x", nil, &struct{}{}); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("calls = %d, want 2", calls)
	}
}

func TestClientErrorIsNotRetried(t *testing.T) {
	calls := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"message":"Incorrect API key provided","type":"invalid_request_error","code":"invalid_api_key"}}`)
	})
	err := c.Post(context.Background(), "/x", nil, &struct{}{})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 401 || apiErr.Code != "invalid_api_key" {
		t.Fatalf("err = %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestGivesUpAfterMaxAttempts(t *testing.T) {
	calls := 0
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	if err := c.Post(context.Background(), "/x", nil, &struct{}{}); err == nil {
		t.Fatal("expected an error")
	}
	if calls != maxAttempts {
		t.Fatalf("calls = %d, want %d", calls, maxAttempts)
	}
}
