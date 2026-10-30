// Package decision defines the Decision record: every non-trivial choice the agent makes.
package decision

import (
	"encoding/json"
	"time"

	"github.com/oklog/ulid/v2"
)

type Tier string

const (
	TierReflex  Tier = "reflex"
	TierSystem1 Tier = "system1"
	TierSystem2 Tier = "system2"
)

// Gate is what the autonomy policy did with the chosen action.
type Gate string

const (
	GateAuto   Gate = "auto"
	GateAsked  Gate = "asked"
	GateDenied Gate = "denied"
)

type QuestionType string

const (
	QuestionPredicate QuestionType = "predicate"
	QuestionChoice    QuestionType = "choice"
	QuestionScore     QuestionType = "score"
)

type Answer struct {
	Type          QuestionType       `json:"type"`
	Refused       bool               `json:"refused,omitempty"`
	Probability   float64            `json:"probability,omitempty"` // predicate: chance the statement is true
	Choice        string             `json:"choice,omitempty"`
	Score         float64            `json:"score,omitempty"` // score: probability-weighted level index
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Confidence    float64            `json:"confidence,omitempty"` // choice and score only
}

// MarshalJSON writes the fields that belong to the answer's type, keeping
// zeros: a predicate at probability 0 is an answer, not a missing value.
func (a Answer) MarshalJSON() ([]byte, error) {
	m := map[string]any{"type": a.Type}
	if a.Refused {
		m["refused"] = true
		return json.Marshal(m)
	}
	switch a.Type {
	case QuestionPredicate:
		m["probability"] = a.Probability
	case QuestionChoice:
		m["choice"], m["confidence"], m["probabilities"] = a.Choice, a.Confidence, a.Probabilities
	case QuestionScore:
		m["score"], m["confidence"], m["probabilities"] = a.Score, a.Confidence, a.Probabilities
	}
	return json.Marshal(m)
}

type OutcomeStatus string

const (
	OutcomeOK     OutcomeStatus = "ok"
	OutcomeFailed OutcomeStatus = "failed"
)

type Outcome struct {
	Status OutcomeStatus `json:"status"`
	Detail string        `json:"detail,omitempty"`
	At     time.Time     `json:"at"`
}

type Decision struct {
	ID        ulid.ULID
	ParentID  *ulid.ULID
	At        time.Time
	PerceptID ulid.ULID
	SetID     string // question set or reflex that decided, e.g. "incoming_message@3"
	Tier      Tier
	Provider  string // "" for reflexes
	Answers   map[string]Answer
	Action    string
	Reply     string // what was said, on the decision that said it; "" otherwise
	Gate      Gate
	Latency   time.Duration
	CostUSD   float64
	Outcome   *Outcome
	Affect    json.RawMessage // nil until emotions exist (architecture §4.9)
}
