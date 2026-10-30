package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/oklog/ulid/v2"

	"github.com/Grandbusta/minime/internal/bus"
	"github.com/Grandbusta/minime/internal/perception"
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

// Run polls until ctx is done: the library long-polls (offsets, retries and
// rate limits), one update at a time, in order.
func (s *Sensor) Run(ctx context.Context) error {
	if len(s.owners) == 0 {
		s.log.Warn("no telegram owner_ids configured: every message is ignored; senders' IDs are logged")
	}
	poll := time.Duration(s.pollTimeout) * time.Second
	b, err := bot.New(s.client.token, s.client.options(
		bot.WithHTTPClient(poll, &http.Client{Timeout: poll + 15*time.Second}),
		bot.WithAllowedUpdates(bot.AllowedUpdates{"message", "callback_query"}),
		bot.WithNotAsyncHandlers(),
		bot.WithErrorsHandler(func(err error) { s.log.Warn("telegram poll failed", "err", err) }),
		bot.WithDefaultHandler(func(ctx context.Context, _ *bot.Bot, u *models.Update) { s.handle(ctx, u) }))...)
	if err != nil {
		return fmt.Errorf("telegram rejected the bot token: %w", err)
	}
	b.Start(ctx)
	return nil
}

// handle turns one update into a percept, if it's from an owner.
func (s *Sensor) handle(ctx context.Context, u *models.Update) {
	p, ok := s.percept(u)
	if !ok {
		return
	}
	if q := u.CallbackQuery; q != nil {
		if err := s.client.AnswerCallback(ctx, q.ID, "Got it"); err != nil {
			s.log.Warn("answering a button tap failed", "err", err)
		}
	}
	s.out.Publish(ctx, p) // fails only when stopping
}

// percept converts an update into a percept, dropping anything not from an owner.
func (s *Sensor) percept(u *models.Update) (perception.Percept, bool) {
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
	payload, _ := json.Marshal(MessagePayload{ChatID: m.Chat.ID, MessageID: int64(m.ID), UserID: m.From.ID})
	return perception.Percept{
		ID:          ulid.Make(),
		Source:      Source,
		ExternalID:  fmt.Sprintf("%d:%d", m.Chat.ID, m.ID),
		Thread:      strconv.FormatInt(m.Chat.ID, 10),
		Speaker:     Speaker(m.From.ID),
		SpeakerName: speakerName(m.From),
		Kind:        "message",
		Text:        m.Text,
		Payload:     payload,
		At:          time.Unix(int64(m.Date), 0).UTC(),
	}, true
}

// The data of an approval's buttons: "gate:<approval id>:y" or ":n"; of a
// call's: "call:<call id>:<option>", "x" for something else.
const (
	gatePrefix = "gate:"
	callPrefix = "call:"
)

// answer turns an owner's tap on an approval's button into a percept.
func (s *Sensor) answer(q *models.CallbackQuery) (perception.Percept, bool) {
	if !s.owners[q.From.ID] || chatOf(q) == 0 {
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
		Thread:      strconv.FormatInt(chatOf(q), 10),
		Speaker:     Speaker(q.From.ID),
		SpeakerName: speakerName(&q.From),
		Kind:        perception.KindApproval,
		Payload:     payload,
		At:          time.Now().UTC(),
	}, true
}

// pick turns an owner's tap on a call's option into a percept.
func (s *Sensor) pick(q *models.CallbackQuery, rest string) (perception.Percept, bool) {
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
		Thread:      strconv.FormatInt(chatOf(q), 10),
		Speaker:     Speaker(q.From.ID),
		SpeakerName: speakerName(&q.From),
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

// chatOf is the chat a tapped button is in; 0 if Telegram didn't say.
func chatOf(q *models.CallbackQuery) int64 {
	switch {
	case q.Message.Message != nil:
		return q.Message.Message.Chat.ID
	case q.Message.InaccessibleMessage != nil:
		return q.Message.InaccessibleMessage.Chat.ID
	}
	return 0
}

// Speaker is the stable key of a Telegram user, used to file what they say.
func Speaker(userID int64) string { return "telegram:" + strconv.FormatInt(userID, 10) }

// speakerName is what the agent calls a user until it learns otherwise.
func speakerName(u *models.User) string {
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
