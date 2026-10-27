// Package memory holds what the agent remembers: working memory (the recent
// back-and-forth of a conversation), facts, and the people it knows.
package memory

import (
	"time"

	"github.com/oklog/ulid/v2"
)

// Exchange is one message from a person and the agent's reply to it ("" if none was recorded).
type Exchange struct {
	At      time.Time
	Message string
	Reply   string
}

// How the agent came to know a person.
const (
	KnownFromBirth = "born" // known from the start, like a parent; never overwritten by a claim
	KnownByMeeting = "met"  // talked to it; who they are is not known yet
	KnownByTelling = "told" // they said who they are to it
)

// CircleStranger is the circle of someone the agent has met but whose
// relation it does not know yet.
const CircleStranger = "stranger"

// Person is someone the agent knows, by account rather than by name.
type Person struct {
	Key       string // e.g. "telegram:123"
	Name      string // what to call them
	Relation  string // who they are to the agent, e.g. "Busta is Daniel's father."; "" if not known
	Circle    string // the circle of the agent's life they belong to: family, friend, school, ... or CircleStranger
	Source    string // KnownFromBirth, KnownByMeeting or KnownByTelling
	FirstSeen time.Time
	UpdatedAt time.Time
}

// SubjectSelf is the subject of facts about the agent's own life.
const SubjectSelf = "self"

// Fact is something the agent knows about a life: its own, or a person's.
type Fact struct {
	ID        ulid.ULID
	PerceptID ulid.ULID // the message it came from
	Subject   string    // SubjectSelf, or the person's key (e.g. "telegram:123")
	Text      string    // as said or written; pronouns are resolved to names when used
	At        time.Time // when it was said
	Areas     []string  // life areas it is about, e.g. ["home", "family"]; empty if not labelled
}
