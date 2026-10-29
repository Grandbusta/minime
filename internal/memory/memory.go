// Package memory holds what the agent remembers: working memory (the recent
// back-and-forth of a conversation), facts, the people it knows, and what it
// means to do: for them, and in its own day.
package memory

import (
	"encoding/json"
	"time"

	"github.com/oklog/ulid/v2"
)

// Exchange is one message from a person and the agent's reply to it ("" if
// none was recorded). When the agent spoke first (a reminder or a check-in),
// Message is "".
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

// Intention is something the agent means to do later: prospective memory.
// For a person, it is a reminder they asked for or a task it promised them;
// for itself (Subject SubjectSelf), an activity of its own day.
type Intention struct {
	ID        ulid.ULID
	Subject   string    // SubjectSelf, or who it is for, e.g. "telegram:123"
	Kind      string    // IntentionRemind, IntentionTask or IntentionActivity
	PerceptID ulid.ULID // the message it came from; zero if the agent planned it itself
	ProjectID ulid.ULID // the project it is part of; zero if none
	Text      string    // what to do, e.g. "call mum", "write the hook for Lagos Rain"
	DueAt     time.Time
	Status    string // IntentionPending, IntentionDone, IntentionMissed or IntentionCancelled
	Result    string // what came of it, e.g. the title of what it made; "" until done
	CreatedAt time.Time
	DoneAt    time.Time // zero until done
}

const (
	IntentionRemind   = "remind"   // remind a person of something they asked
	IntentionTask     = "task"     // something it promised a person
	IntentionActivity = "activity" // something of its own day
)

const (
	IntentionPending   = "pending"
	IntentionDone      = "done"
	IntentionMissed    = "missed" // could not be done in time
	IntentionCancelled = "cancelled"
)

// Project is something that takes the agent more than a day: a book, a beat, a topic.
type Project struct {
	ID        ulid.ULID
	Title     string
	Kind      string // music, writing, reading, learning, ...
	Status    string // ProjectActive, ProjectDone or ProjectDropped
	Notes     string // where it got to, so the next session continues from there
	CreatedAt time.Time
	UpdatedAt time.Time
}

const (
	ProjectActive  = "active"
	ProjectDone    = "done"
	ProjectDropped = "dropped"
)

// Work is something the agent made: a journal entry, lyrics, a beat idea, notes.
type Work struct {
	ID          ulid.ULID
	IntentionID ulid.ULID // the intention it came from; zero if none
	ProjectID   ulid.ULID // zero if none
	Kind        string    // journal, lyrics, beat_idea, notes, ...
	Title       string
	Text        string
	CreatedAt   time.Time
}

// Conversation is where the agent last talked with a person, so it can
// message them first.
type Conversation struct {
	Person       string          // e.g. "telegram:123"
	Source       string          // channel, e.g. "telegram"
	Thread       string          // e.g. the Telegram chat ID
	Payload      json.RawMessage // the channel's details from their last message, used to address a message
	LastActivity time.Time       // the last thing said in the thread, by either side
}
