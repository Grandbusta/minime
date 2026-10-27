// Package telegram connects the agent to people through a Telegram bot.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// maxMessageRunes is Telegram's limit on a message's text length.
const maxMessageRunes = 4096

type Update struct {
	UpdateID int64    `json:"update_id"`
	Message  *Message `json:"message"`
}

type Message struct {
	MessageID int64  `json:"message_id"`
	From      *User  `json:"from"`
	Chat      Chat   `json:"chat"`
	Date      int64  `json:"date"`
	Text      string `json:"text"`
}

type User struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	Username  string `json:"username"`
}

type Chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}

// APIError is an error reported by the Bot API itself.
type APIError struct {
	Method      string
	Code        int
	Description string
	RetryAfter  time.Duration
}

func (e *APIError) Error() string {
	return fmt.Sprintf("telegram %s: %d %s", e.Method, e.Code, e.Description)
}

type Client struct {
	base string // includes the bot token; never put it in errors or logs
	http *http.Client
}

func NewClient(token string) *Client {
	return &Client{base: "https://api.telegram.org/bot" + token, http: &http.Client{}}
}

// GetUpdates long-polls for new updates starting at offset.
func (c *Client) GetUpdates(ctx context.Context, offset int64, timeoutSeconds int) ([]Update, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds+10)*time.Second)
	defer cancel()
	var updates []Update
	err := c.call(ctx, "getUpdates", map[string]any{
		"offset":          offset,
		"timeout":         timeoutSeconds,
		"allowed_updates": []string{"message"},
	}, &updates)
	return updates, err
}

func (c *Client) SendMessage(ctx context.Context, chatID int64, text string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if r := []rune(text); len(r) > maxMessageRunes {
		text = string(r[:maxMessageRunes-1]) + "…"
	}
	return c.call(ctx, "sendMessage", map[string]any{"chat_id": chatID, "text": text}, nil)
}

func (c *Client) call(ctx context.Context, method string, params, result any) error {
	body, err := json.Marshal(params)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/"+method, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("telegram %s: build request failed", method)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		// A *url.Error prints the request URL, which contains the token.
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		return fmt.Errorf("telegram %s: %w", method, err)
	}
	defer resp.Body.Close()

	var r struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		ErrorCode   int             `json:"error_code"`
		Description string          `json:"description"`
		Parameters  *struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return fmt.Errorf("telegram %s: decode response (HTTP %d): %w", method, resp.StatusCode, err)
	}
	if !r.OK {
		apiErr := &APIError{Method: method, Code: r.ErrorCode, Description: r.Description}
		if r.Parameters != nil {
			apiErr.RetryAfter = time.Duration(r.Parameters.RetryAfter) * time.Second
		}
		return apiErr
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(r.Result, result); err != nil {
		return fmt.Errorf("telegram %s: decode result: %w", method, err)
	}
	return nil
}
