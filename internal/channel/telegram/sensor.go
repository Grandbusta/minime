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
			if err := s.out.Publish(ctx, p); err != nil {
				return nil
			}
		}
	}
	return nil
}

// percept converts an update into a percept, dropping anything not from an owner.
func (s *Sensor) percept(u Update) (perception.Percept, bool) {
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
