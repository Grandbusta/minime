package telegram

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"
	"github.com/oklog/ulid/v2"

	"github.com/Grandbusta/minime/internal/perception"
)

const Source = "telegram"

type MessagePayload struct {
	ChatID    int64 `json:"chat_id"`
	MessageID int64 `json:"message_id"`
	UserID    int64 `json:"user_id,omitempty"`
}

// percept converts an update into a percept, dropping anything not from an owner.
func (c *Channel) percept(u *models.Update) (perception.Percept, bool) {
	if u.CallbackQuery != nil {
		return c.answer(u.CallbackQuery)
	}
	m := u.Message
	if m == nil || m.From == nil {
		return perception.Percept{}, false
	}
	if !c.owners[m.From.ID] {
		c.log.Info("ignoring message from non-owner", "user_id", m.From.ID, "username", m.From.Username)
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

func (c *Channel) answer(q *models.CallbackQuery) (perception.Percept, bool) {
	if !c.owners[q.From.ID] || chatOf(q) == 0 {
		return perception.Percept{}, false
	}
	if rest, ok := strings.CutPrefix(q.Data, callPrefix); ok {
		return c.pick(q, rest)
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

func (c *Channel) pick(q *models.CallbackQuery, rest string) (perception.Percept, bool) {
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

func Speaker(userID int64) string { return "telegram:" + strconv.FormatInt(userID, 10) }

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
