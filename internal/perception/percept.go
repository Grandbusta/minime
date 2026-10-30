// Package perception defines what the agent perceives from the world.
package perception

import (
	"encoding/json"
	"time"

	"github.com/oklog/ulid/v2"
)

type Percept struct {
	ID          ulid.ULID
	Source      string
	ExternalID  string // source's own ID, used to drop duplicates; unique per source
	Thread      string
	Speaker     string // stable key of the person who said it, e.g. "telegram:123"; "" if none
	SpeakerName string
	Kind        string
	Text        string
	Payload     json.RawMessage
	At          time.Time
}

// Kinds of percept the agent makes itself when it speaks first, so a reply to
// it reads as an answer. What it said is on the decision.
const (
	KindReminder      = "reminder"
	KindCheckIn       = "check_in"
	KindDelivery      = "delivery"
	KindReviewRequest = "review_request"
)

// KindApproval is the owner answering the autonomy gate. Its Payload is an
// Answer; it has no Text, so it is never part of the conversation.
const KindApproval = "approval"

// KindCall is the owner's own choice on one of the agent's calls. Its Payload
// is a Pick; it has no Text.
const KindCall = "call"

type Pick struct {
	Call   string `json:"call"`
	Option int    `json:"option"` // an index into its options; -1 something else
}

type Answer struct {
	Approval string `json:"approval"`
	Approve  bool   `json:"approve"`
}
