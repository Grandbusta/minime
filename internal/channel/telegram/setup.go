package telegram

import (
	"context"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// BotName checks a token and returns its bot's @username.
func BotName(ctx context.Context, token string) (string, error) {
	b, err := bot.New(token, bot.WithSkipGetMe())
	if err != nil {
		return "", err
	}
	me, err := b.GetMe(ctx)
	if err != nil {
		return "", err
	}
	return me.Username, nil
}

// FirstMessage waits up to wait for a new message from a person and returns
// their user ID and first name. It replies and marks it read, so it isn't
// answered again later.
func FirstMessage(ctx context.Context, token string, wait time.Duration) (int64, string, error) {
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	since := time.Now().Add(-5 * time.Second)
	type who struct {
		id   int64
		name string
	}
	found := make(chan who, 1)
	b, err := bot.New(token, bot.WithSkipGetMe(), bot.WithInitialOffset(-2), // skip what's waiting but the last, which is checked by its date
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
		}))
	if err != nil {
		return 0, "", err
	}
	b.Start(ctx)
	select {
	case w := <-found:
		return w.id, w.name, nil
	default:
		return 0, "", context.DeadlineExceeded
	}
}
