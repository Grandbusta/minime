// Package telegram connects the agent to people through a Telegram bot.
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
	"unicode/utf8"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/Grandbusta/minime/internal/perception"
)

const maxMessageRunes = 4096

type Channel struct {
	bot    *bot.Bot
	owners map[int64]bool
	out    chan<- perception.Percept
	log    *slog.Logger
}

// New makes the bot. Every request, the long poll included, gives up after
// poll plus 15 seconds.
func New(token string, ownerIDs []int64, poll time.Duration, out chan<- perception.Percept, log *slog.Logger,
	opts ...bot.Option) (*Channel, error) {
	c := &Channel{owners: make(map[int64]bool, len(ownerIDs)), out: out, log: log}
	for _, id := range ownerIDs {
		c.owners[id] = true
	}
	b, err := bot.New(token, append([]bot.Option{
		bot.WithSkipGetMe(),
		bot.WithHTTPClient(poll, &http.Client{Timeout: poll + 15*time.Second}),
		bot.WithAllowedUpdates(bot.AllowedUpdates{"message", "callback_query"}),
		bot.WithNotAsyncHandlers(),
		bot.WithErrorsHandler(func(err error) { log.Warn("telegram request failed", "err", err) }),
		bot.WithDefaultHandler(func(ctx context.Context, _ *bot.Bot, u *models.Update) { c.handle(ctx, u) }),
	}, opts...)...)
	if err != nil {
		return nil, err
	}
	c.bot = b
	return c, nil
}

func (c *Channel) Run(ctx context.Context) error {
	if _, err := c.bot.GetMe(ctx); err != nil {
		return fmt.Errorf("telegram rejected the bot token: %w", err)
	}
	if len(c.owners) == 0 {
		c.log.Warn("no telegram owner_ids configured: every message is ignored; senders' IDs are logged")
	}
	c.bot.Start(ctx)
	return nil
}

func (c *Channel) handle(ctx context.Context, u *models.Update) {
	p, ok := c.percept(u)
	if !ok {
		return
	}
	if q := u.CallbackQuery; q != nil { // so the button stops spinning
		if _, err := c.bot.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: q.ID, Text: "Got it"}); err != nil {
			c.log.Warn("answering a button tap failed", "err", err)
		}
	}
	select {
	case c.out <- p:
	case <-ctx.Done():
	}
}

func (c *Channel) Reply(ctx context.Context, p perception.Percept, text string) error {
	chat, err := chatID(p)
	if err != nil {
		return fmt.Errorf("telegram reply: %w", err)
	}
	for _, part := range split(text, maxMessageRunes) {
		if _, err := c.bot.SendMessage(ctx, &bot.SendMessageParams{ChatID: chat, Text: part}); err != nil {
			return err
		}
	}
	return nil
}

// Ask sends text with Approve and Deny buttons for approval id, and returns
// where it was sent, for Settle.
func (c *Channel) Ask(ctx context.Context, p perception.Percept, text, id string) (json.RawMessage, error) {
	return c.send(ctx, p, text, [][]models.InlineKeyboardButton{{
		{Text: "✅ Approve", CallbackData: gatePrefix + id + ":y"}, {Text: "❌ Deny", CallbackData: gatePrefix + id + ":n"}}})
}

// Choose sends text with a numbered button for each of a call's options, its
// pick starred, and one for something else. text lists the options.
func (c *Channel) Choose(ctx context.Context, p perception.Percept, text, id string, options int, pick int) (json.RawMessage, error) {
	var row []models.InlineKeyboardButton
	for i := 0; i < options; i++ {
		label := strconv.Itoa(i + 1)
		if i == pick {
			label += " ★"
		}
		row = append(row, models.InlineKeyboardButton{Text: label, CallbackData: callPrefix + id + ":" + strconv.Itoa(i)})
	}
	return c.send(ctx, p, text, [][]models.InlineKeyboardButton{row, {{Text: "Something else", CallbackData: callPrefix + id + ":x"}}})
}

func (c *Channel) send(ctx context.Context, p perception.Percept, text string, rows [][]models.InlineKeyboardButton) (json.RawMessage, error) {
	chat, err := chatID(p)
	if err != nil {
		return nil, fmt.Errorf("telegram send: %w", err)
	}
	m, err := c.bot.SendMessage(ctx, &bot.SendMessageParams{ChatID: chat, Text: cutTo(text, maxMessageRunes),
		ReplyMarkup: models.InlineKeyboardMarkup{InlineKeyboard: rows}})
	if err != nil {
		return nil, err
	}
	return json.Marshal(MessagePayload{ChatID: chat, MessageID: int64(m.ID)})
}

// Settle replaces a message sent by Ask or Choose with how it ended, removing
// its buttons.
func (c *Channel) Settle(ctx context.Context, where json.RawMessage, text string) error {
	var m MessagePayload
	if err := json.Unmarshal(where, &m); err != nil || m.MessageID == 0 {
		return fmt.Errorf("telegram settle: bad message %s", where)
	}
	_, err := c.bot.EditMessageText(ctx, &bot.EditMessageTextParams{ChatID: m.ChatID, MessageID: int(m.MessageID),
		Text: cutTo(text, maxMessageRunes)})
	return err
}

func chatID(p perception.Percept) (int64, error) {
	if p.Source != Source {
		return 0, fmt.Errorf("not a telegram percept (%q)", p.Source)
	}
	var pl MessagePayload
	if err := json.Unmarshal(p.Payload, &pl); err != nil {
		return 0, fmt.Errorf("bad payload: %w", err)
	}
	return pl.ChatID, nil
}

// split cuts text into parts of at most max runes, at a paragraph break if
// there is one in the second half of a part, else a line break or a space.
func split(text string, max int) []string {
	var parts []string
	r := []rune(text)
	for len(r) > max {
		cut := max
		head := string(r[:max])
		for _, sep := range []string{"\n\n", "\n", " "} {
			if i := strings.LastIndex(head, sep); i >= 0 {
				if n := utf8.RuneCountInString(head[:i]); n >= max/2 {
					cut = n
					break
				}
			}
		}
		parts = append(parts, strings.TrimSpace(string(r[:cut])))
		r = r[cut:]
		for len(r) > 0 && (r[0] == ' ' || r[0] == '\n') {
			r = r[1:]
		}
	}
	return append(parts, string(r))
}

func cutTo(text string, max int) string {
	if r := []rune(text); len(r) > max {
		return string(r[:max-1]) + "…"
	}
	return text
}
