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
