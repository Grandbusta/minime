// Package openaihttp is the HTTP transport shared by the OpenAI providers:
// auth, JSON, error decoding and retries.
package openaihttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

const (
	DefaultBaseURL = "https://api.openai.com/v1"
	maxAttempts    = 3
)

type Client struct {
	apiKey    string
	baseURL   string
	timeout   time.Duration // per attempt
	http      *http.Client
	RetryWait time.Duration // first backoff; doubles per attempt
}

// New returns a client. An empty baseURL means the public API.
func New(apiKey, baseURL string, timeout time.Duration) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{apiKey: apiKey, baseURL: baseURL, timeout: timeout, http: &http.Client{}, RetryWait: time.Second}
}

// APIError is an error response from the API.
type APIError struct {
	Path       string
	Status     int
	Type       string
	Code       string
	Message    string
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	return fmt.Sprintf("openai %s: HTTP %d %s: %s", e.Path, e.Status, e.Type, e.Message)
}

func (e *APIError) retryable() bool {
	return e.Status == http.StatusTooManyRequests || e.Status >= 500
}

// Post sends body as JSON to path and decodes the JSON response into out,
// retrying rate limits, server errors and network failures.
func (c *Client) Post(ctx context.Context, path string, body, out any) error {
	raw, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("openai %s: encode request: %w", path, err)
	}
	wait := c.RetryWait
	for attempt := 1; ; attempt++ {
		err := c.postOnce(ctx, path, raw, out)
		var apiErr *APIError
		if err == nil || attempt == maxAttempts || ctx.Err() != nil ||
			(errors.As(err, &apiErr) && !apiErr.retryable()) {
			return err
		}
		if apiErr != nil && apiErr.RetryAfter > 0 {
			wait = apiErr.RetryAfter
		}
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return ctx.Err()
		}
		wait *= 2
	}
}

func (c *Client) postOnce(ctx context.Context, path string, body []byte, out any) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("openai %s: build request: %w", path, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("openai %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error struct {
				Message string `json:"message"`
				Type    string `json:"type"`
				Code    any    `json:"code"`
			} `json:"error"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&e)
		apiErr := &APIError{Path: path, Status: resp.StatusCode, Type: e.Error.Type, Message: e.Error.Message}
		if e.Error.Code != nil {
			apiErr.Code = fmt.Sprint(e.Error.Code)
		}
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil {
			apiErr.RetryAfter = time.Duration(s) * time.Second
		}
		return apiErr
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("openai %s: decode response: %w", path, err)
	}
	return nil
}
