// Package openaiclient makes the official OpenAI client every OpenAI
// provider shares the settings of: the key, an optional base URL, a timeout
// per attempt, and the SDK's own retries (rate limits, server errors and
// network failures, honouring Retry-After).
package openaiclient

import (
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

// Retries is how many times a failed request is tried again.
const Retries = 2

// New returns a client. An empty baseURL means the public API.
func New(apiKey, baseURL string, timeout time.Duration) openai.Client {
	opts := []option.RequestOption{option.WithAPIKey(apiKey), option.WithRequestTimeout(timeout), option.WithMaxRetries(Retries)}
	if baseURL != "" {
		opts = append(opts, option.WithBaseURL(baseURL))
	}
	return openai.NewClient(opts...)
}
