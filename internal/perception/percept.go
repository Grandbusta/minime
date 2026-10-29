// Package perception defines what the agent perceives from the world.
package perception

import (
	"context"
	"encoding/json"
	"time"

	"github.com/oklog/ulid/v2"
)

// Percept is one normalized thing the agent noticed.
type Percept struct {
	ID          ulid.ULID
	Source      string          // "telegram", "clock", ...
	ExternalID  string          // source's own ID, used to drop duplicates; unique per source
	Thread      string          // the conversation it belongs to, e.g. a Telegram chat ID; "" if none
	Speaker     string          // stable key of the person who said it, e.g. "telegram:123"; "" if none
	SpeakerName string          // what to call them, e.g. their Telegram first name
	Kind        string          // "message", "tick", ...
	Text        string          // textual content, empty when there is none
	Payload     json.RawMessage // source-specific details
	At          time.Time
}

// Sensor watches one part of the world and emits percepts until ctx is done.
type Sensor interface {
	Run(ctx context.Context) error
}

// Kinds of percept the agent makes itself when it speaks first: what it said
// is on the decision, and the percept places it in the conversation, so a
// reply to it reads as an answer.
const (
	KindReminder      = "reminder"       // reminding someone of what they asked
	KindCheckIn       = "check_in"       // asking how something in their life is going
	KindDelivery      = "delivery"       // sending what it promised to make for them
	KindReviewRequest = "review_request" // asking its owner to look at something it made
)

// KindApproval is the owner answering something the agent asked before
// doing it (the autonomy gate). Its Payload is an Answer; it has no Text,
// so it is never part of the conversation.
const KindApproval = "approval"

// KindCall is the owner choosing what they'd do on one of the agent's calls
// (a decision where it predicted their choice). Its Payload is a Pick; it
// has no Text.
const KindCall = "call"

// Pick is the owner's choice on a call.
type Pick struct {
	Call   string `json:"call"`   // the call's ID
	Option int    `json:"option"` // an index into its options; -1 something else
}

// Answer is the owner's answer to an approval.
type Answer struct {
	Approval string `json:"approval"` // the approval's ID
	Approve  bool   `json:"approve"`
}
