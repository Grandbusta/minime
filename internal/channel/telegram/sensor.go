package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"minime/internal/bus"
	"minime/internal/perception"
)

// Source is the percept source name for Telegram.
const Source = "telegram"

// MessagePayload is the Payload of a Telegram message percept.
type MessagePayload struct {
	ChatID    int64 `json:"chat_id"`
	MessageID int64 `json:"message_id"`
	UserID    int64 `json:"user_id"`
}

// Sensor long-polls Telegram and turns the owners' messages into percepts.
type Sensor struct {
	client      *Client
	owners      map[int64]bool
	out         *bus.Bus[perception.Percept]
	pollTimeout int
	log         *slog.Logger
}

func NewSensor(c *Client, ownerIDs []int64, pollTimeoutSeconds int, out *bus.Bus[perception.Percept], log *slog.Logger) *Sensor {
	owners := make(map[int64]bool, len(ownerIDs))
	for _, id := range ownerIDs {
		owners[id] = true
	}
	return &Sensor{client: c, owners: owners, out: out, pollTimeout: pollTimeoutSeconds, log: log}
}

func (s *Sensor) Run(ctx context.Context) error {
	if len(s.owners) == 0 {
		s.log.Warn("no telegram owner_ids configured: every message is ignored; senders' IDs are logged")
	}
	var offset int64
	backoff := time.Second
	for ctx.Err() == nil {
		updates, err := s.client.GetUpdates(ctx, offset, s.pollTimeout)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			wait := backoff
			var apiErr *APIError
			if errors.As(err, &apiErr) {
				if apiErr.Code == http.StatusUnauthorized {
					return fmt.Errorf("telegram rejected the bot token: %w", err)
				}
				if apiErr.RetryAfter > 0 {
					wait = apiErr.RetryAfter
				}
			}
			s.log.Warn("telegram poll failed", "err", err, "retry_in", wait)
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return nil
			}
			backoff = min(backoff*2, time.Minute)
			continue
		}
		backoff = time.Second

		for _, u := range updates {
			offset = u.UpdateID + 1
			p, ok := s.percept(u)
			if !ok {
				continue
			}
			if q := u.CallbackQuery; q != nil {
				if err := s.client.AnswerCallback(ctx, q.ID, "Got it"); err != nil {
					s.log.Warn("answering a button tap failed", "err", err)
				}
			}
			if err := s.out.Publish(ctx, p); err != nil {
				return nil
			}
		}
	}
	return nil
}

// percept converts an update into a percept, dropping anything not from an owner.
func (s *Sensor) percept(u Update) (perception.Percept, bool) {
	if u.CallbackQuery != nil {
		return s.answer(u.CallbackQuery)
	}
	m := u.Message
	if m == nil || m.From == nil {
		return perception.Percept{}, false
	}
	if !s.owners[m.From.ID] {
		s.log.Info("ignoring message from non-owner", "user_id", m.From.ID, "username", m.From.Username)
		return perception.Percept{}, false
	}
	payload, _ := json.Marshal(MessagePayload{ChatID: m.Chat.ID, MessageID: m.MessageID, UserID: m.From.ID})
	return perception.Percept{
		ID:          ulid.Make(),
		Source:      Source,
		ExternalID:  fmt.Sprintf("%d:%d", m.Chat.ID, m.MessageID),
		Thread:      strconv.FormatInt(m.Chat.ID, 10),
		Speaker:     Speaker(m.From.ID),
		SpeakerName: speakerName(m.From),
		Kind:        "message",
		Text:        m.Text,
		Payload:     payload,
		At:          time.Unix(m.Date, 0).UTC(),
	}, true
}

// The data of an approval's buttons: "gate:<approval id>:y" or ":n"; of a
// call's: "call:<call id>:<option>", "x" for something else.
const (
	gatePrefix = "gate:"
	callPrefix = "call:"
)

// answer turns an owner's tap on an approval's button into a percept.
func (s *Sensor) answer(q *CallbackQuery) (perception.Percept, bool) {
	if q.From == nil || !s.owners[q.From.ID] || q.Message == nil {
		return perception.Percept{}, false
	}
	if rest, ok := strings.CutPrefix(q.Data, callPrefix); ok {
		return s.pick(q, rest)
	}
	rest, ok := strings.CutPrefix(q.Data, gatePrefix)
	id, choice, ok2 := strings.Cut(rest, ":")
	if !ok || !ok2 || (choice != "y" && choice != "n") {
		return perception.Percept{}, false
	}
	payload, _ := json.Marshal(perception.Answer{Approval: id, Approve: choice == "y"})
	return perception.Percept{
		ID:          ulid.Make(),
		Source:      Source,
		ExternalID:  "callback:" + q.ID,
		Thread:      strconv.FormatInt(q.Message.Chat.ID, 10),
		Speaker:     Speaker(q.From.ID),
		SpeakerName: speakerName(q.From),
		Kind:        perception.KindApproval,
		Payload:     payload,
		At:          time.Now().UTC(),
	}, true
}

// pick turns an owner's tap on a call's option into a percept.
func (s *Sensor) pick(q *CallbackQuery, rest string) (perception.Percept, bool) {
	id, opt, ok := strings.Cut(rest, ":")
	n, err := strconv.Atoi(opt)
	if opt == "x" {
		n, err = -1, nil
	}
	if !ok || id == "" || err != nil || n < -1 {
		return perception.Percept{}, false
	}
	payload, _ := json.Marshal(perception.Pick{Call: id, Option: n})
	return perception.Percept{
		ID:          ulid.Make(),
		Source:      Source,
		ExternalID:  "callback:" + q.ID,
		Thread:      strconv.FormatInt(q.Message.Chat.ID, 10),
		Speaker:     Speaker(q.From.ID),
		SpeakerName: speakerName(q.From),
		Kind:        perception.KindCall,
		Payload:     payload,
		At:          time.Now().UTC(),
	}, true
}

// Choose sends text with a button for each of a call's options, its pick
// starred, and one for something else, to the chat p is in, and returns
// where it was sent. Buttons are numbered; text lists the options.
func (c *Client) Choose(ctx context.Context, p perception.Percept, text, id string, options int, pick int) (json.RawMessage, error) {
	var pl MessagePayload
	if err := json.Unmarshal(p.Payload, &pl); err != nil {
		return nil, fmt.Errorf("telegram choose: bad payload: %w", err)
	}
	var rows [][]Button
	var row []Button
	for i := 0; i < options; i++ {
		label := strconv.Itoa(i + 1)
		if i == pick {
			label += " ★"
		}
		row = append(row, Button{Text: label, Data: callPrefix + id + ":" + strconv.Itoa(i)})
	}
	rows = append(rows, row, []Button{{Text: "Something else", Data: callPrefix + id + ":x"}})
	msgID, err := c.SendKeyboard(ctx, pl.ChatID, text, rows)
	if err != nil {
		return nil, err
	}
	return json.Marshal(AskMessage{ChatID: pl.ChatID, MessageID: msgID})
}

// AskMessage is where an approval request was sent, to update it after.
type AskMessage struct {
	ChatID    int64 `json:"chat_id"`
	MessageID int64 `json:"message_id"`
}

// Ask sends text, with Approve and Deny buttons for approval id, to the chat
// p is in, and returns where it was sent.
func (c *Client) Ask(ctx context.Context, p perception.Percept, text, id string) (json.RawMessage, error) {
	var pl MessagePayload
	if err := json.Unmarshal(p.Payload, &pl); err != nil {
		return nil, fmt.Errorf("telegram ask: bad payload: %w", err)
	}
	msgID, err := c.SendButtons(ctx, pl.ChatID, text, []Button{
		{Text: "✅ Approve", Data: gatePrefix + id + ":y"}, {Text: "❌ Deny", Data: gatePrefix + id + ":n"}})
	if err != nil {
		return nil, err
	}
	return json.Marshal(AskMessage{ChatID: pl.ChatID, MessageID: msgID})
}

// Settle replaces an approval request's text with how it ended, removing its buttons.
func (c *Client) Settle(ctx context.Context, where json.RawMessage, text string) error {
	var m AskMessage
	if err := json.Unmarshal(where, &m); err != nil || m.MessageID == 0 {
		return fmt.Errorf("telegram settle: bad message %s", where)
	}
	return c.EditMessage(ctx, m.ChatID, m.MessageID, text)
}

// Speaker is the stable key of a Telegram user, used to file what they say.
func Speaker(userID int64) string { return "telegram:" + strconv.FormatInt(userID, 10) }

// speakerName is what the agent calls a user until it learns otherwise.
func speakerName(u *User) string {
	switch {
	case strings.TrimSpace(u.FirstName) != "":
		return strings.TrimSpace(u.FirstName)
	case u.Username != "":
		return u.Username
	default:
		return "Friend"
	}
}

// Reply sends text to the chat a Telegram percept came from.
func (c *Client) Reply(ctx context.Context, p perception.Percept, text string) error {
	if p.Source != Source {
		return fmt.Errorf("telegram cannot reply to a %q percept", p.Source)
	}
	var pl MessagePayload
	if err := json.Unmarshal(p.Payload, &pl); err != nil {
		return fmt.Errorf("telegram reply: bad payload: %w", err)
	}
	return c.SendMessage(ctx, pl.ChatID, text)
}
