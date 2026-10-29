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
	"strings"
	"time"
)

// maxMessageRunes is Telegram's limit on a message's text length.
const maxMessageRunes = 4096

type Update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *Message       `json:"message"`
	CallbackQuery *CallbackQuery `json:"callback_query"`
}

// CallbackQuery is a tap on a button the bot sent.
type CallbackQuery struct {
	ID      string   `json:"id"`
	From    *User    `json:"from"`
	Message *Message `json:"message"`
	Data    string   `json:"data"`
}

// Button is an inline button under a message: its label, and the data a tap sends back.
type Button struct {
	Text string `json:"text"`
	Data string `json:"callback_data"`
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
		"allowed_updates": []string{"message", "callback_query"},
	}, &updates)
	return updates, err
}

// SendMessage sends text, as several messages if it is longer than Telegram allows.
func (c *Client) SendMessage(ctx context.Context, chatID int64, text string) error {
	for _, part := range split(text, maxMessageRunes) {
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := c.call(ctx, "sendMessage", map[string]any{"chat_id": chatID, "text": part}, nil)
		cancel()
		if err != nil {
			return err
		}
	}
	return nil
}

// SendButtons sends text with a row of buttons under it, and returns the message's ID.
func (c *Client) SendButtons(ctx context.Context, chatID int64, text string, buttons []Button) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if r := []rune(text); len(r) > maxMessageRunes {
		text = string(r[:maxMessageRunes-1]) + "…"
	}
	var m Message
	err := c.call(ctx, "sendMessage", map[string]any{"chat_id": chatID, "text": text,
		"reply_markup": map[string]any{"inline_keyboard": [][]Button{buttons}}}, &m)
	return m.MessageID, err
}

// EditMessage replaces a message's text, and removes its buttons.
func (c *Client) EditMessage(ctx context.Context, chatID, messageID int64, text string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if r := []rune(text); len(r) > maxMessageRunes {
		text = string(r[:maxMessageRunes-1]) + "…"
	}
	return c.call(ctx, "editMessageText", map[string]any{"chat_id": chatID, "message_id": messageID, "text": text}, nil)
}

// AnswerCallback acknowledges a button tap, so the button stops spinning.
func (c *Client) AnswerCallback(ctx context.Context, id, text string) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return c.call(ctx, "answerCallbackQuery", map[string]any{"callback_query_id": id, "text": text}, nil)
}

// split cuts text into parts of at most max runes, at a paragraph break if
// there is one in the second half of a part, else a line break or a space.
func split(text string, max int) []string {
	var parts []string
	r := []rune(text)
	for len(r) > max {
		cut := max
		for _, sep := range []string{"\n\n", "\n", " "} {
			if i := strings.LastIndex(string(r[:max]), sep); i >= 0 {
				if at := len([]rune(string(r[:max])[:i])); at >= max/2 {
					cut = at
					break
				}
			}
		}
		parts = append(parts, strings.TrimSpace(string(r[:cut])))
		r = []rune(strings.TrimLeft(string(r[cut:]), " \n"))
	}
	return append(parts, string(r))
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
