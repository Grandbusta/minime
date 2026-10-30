// Package telegram connects the agent to people through a Telegram bot, with
// github.com/go-telegram/bot.
package telegram

import (
	"context"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// maxMessageRunes is Telegram's limit on a message's text length.
const maxMessageRunes = 4096

// requestTimeout bounds every call but the long poll.
const requestTimeout = 15 * time.Second

// Button is an inline button under a message: its label, and the data a tap sends back.
type Button struct {
	Text string
	Data string
}

// Client sends what the agent says through a bot.
type Client struct {
	token  string
	server string // the Bot API's address; "" the real one
	bot    *bot.Bot
	err    error // why the bot couldn't be made, returned by every call
}

// NewClient returns a client for a bot's token.
func NewClient(token string) *Client { return newClient(token, "") }

func newClient(token, server string) *Client {
	c := &Client{token: token, server: server}
	c.bot, c.err = bot.New(token, c.options(bot.WithSkipGetMe())...)
	return c
}

// options are a bot's options, at this client's server.
func (c *Client) options(more ...bot.Option) []bot.Option {
	if c.server != "" {
		more = append(more, bot.WithServerURL(c.server))
	}
	return more
}

// GetMe returns the bot itself: whether the token works, and its @username.
func (c *Client) GetMe(ctx context.Context) (models.User, error) {
	if c.err != nil {
		return models.User{}, c.err
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	me, err := c.bot.GetMe(ctx)
	if err != nil {
		return models.User{}, err
	}
	return *me, nil
}

// SendMessage sends text, as several messages if it is longer than Telegram allows.
func (c *Client) SendMessage(ctx context.Context, chatID int64, text string) error {
	if c.err != nil {
		return c.err
	}
	for _, part := range split(text, maxMessageRunes) {
		ctx, cancel := context.WithTimeout(ctx, requestTimeout)
		_, err := c.bot.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: part})
		cancel()
		if err != nil {
			return err
		}
	}
	return nil
}

// SendButtons sends text with a row of buttons under it, and returns the message's ID.
func (c *Client) SendButtons(ctx context.Context, chatID int64, text string, buttons []Button) (int64, error) {
	return c.SendKeyboard(ctx, chatID, text, [][]Button{buttons})
}

// SendKeyboard sends text with rows of buttons under it, and returns the message's ID.
func (c *Client) SendKeyboard(ctx context.Context, chatID int64, text string, rows [][]Button) (int64, error) {
	if c.err != nil {
		return 0, c.err
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	keyboard := make([][]models.InlineKeyboardButton, len(rows))
	for i, row := range rows {
		for _, b := range row {
			keyboard[i] = append(keyboard[i], models.InlineKeyboardButton{Text: b.Text, CallbackData: b.Data})
		}
	}
	m, err := c.bot.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, Text: cutTo(text, maxMessageRunes),
		ReplyMarkup: models.InlineKeyboardMarkup{InlineKeyboard: keyboard}})
	if err != nil {
		return 0, err
	}
	return int64(m.ID), nil
}

// EditMessage replaces a message's text, and removes its buttons.
func (c *Client) EditMessage(ctx context.Context, chatID, messageID int64, text string) error {
	if c.err != nil {
		return c.err
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	_, err := c.bot.EditMessageText(ctx, &bot.EditMessageTextParams{ChatID: chatID, MessageID: int(messageID), Text: cutTo(text, maxMessageRunes)})
	return err
}

// AnswerCallback acknowledges a button tap, so the button stops spinning.
func (c *Client) AnswerCallback(ctx context.Context, id, text string) error {
	if c.err != nil {
		return c.err
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	_, err := c.bot.AnswerCallbackQuery(ctx, &bot.AnswerCallbackQueryParams{CallbackQueryID: id, Text: text})
	return err
}

// FirstMessage waits up to wait for someone to message the bot (not
// earlier messages, not bots) and returns who: their user ID and first name.
// It replies so they know they were heard, and marks it read, so it isn't
// answered again later.
func (c *Client) FirstMessage(ctx context.Context, wait time.Duration) (int64, string, error) {
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	since := time.Now().Add(-5 * time.Second)
	type who struct {
		id   int64
		name string
	}
	found := make(chan who, 1)
	b, err := bot.New(c.token, c.options(bot.WithInitialOffset(-2), // skip what's waiting but the last, which is checked by its date
		bot.WithNotAsyncHandlers(), bot.WithErrorsHandler(func(error) {}),
		bot.WithDefaultHandler(func(ctx context.Context, b *bot.Bot, u *models.Update) {
			m := u.Message
			if m == nil || m.From == nil || m.From.IsBot || time.Unix(int64(m.Date), 0).Before(since) {
				return
			}
			b.SendMessage(ctx, &bot.SendMessageParams{ChatID: m.Chat.ID, Text: "Got it, that's you. Finish setting up in the terminal."})
			select {
			case found <- who{m.From.ID, m.From.FirstName}:
			default:
			}
			cancel()
		}))...)
	if err != nil {
		return 0, "", err
	}
	b.Start(ctx) // until the first message, or wait is up
	select {
	case w := <-found:
		return w.id, w.name, nil
	default:
		return 0, "", context.DeadlineExceeded
	}
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

// cutTo keeps at most max runes of text, marking the cut.
func cutTo(text string, max int) string {
	if r := []rune(text); len(r) > max {
		return string(r[:max-1]) + "…"
	}
	return text
}
