// Package system2 defines the provider-neutral interface to slow, deliberate
// reasoning models. Each provider lives in its own subpackage.
package system2

import (
	"context"
	"errors"
)

type Reasoner interface {
	Reason(ctx context.Context, req Request) (Response, error)
}

type Request struct {
	Instructions    string
	History         []Turn // oldest first
	Note            string
	Message         string // "" when the agent speaks first
	MaxOutputTokens int    // the most it may write, reasoning included; 0: the provider's default
	// CollectFacts asks for the structured fields of Response alongside the reply.
	CollectFacts bool
	// Format asks for JSON in Response.Text. Not used with CollectFacts.
	Format *Format
}

type Format struct {
	Name   string
	Schema map[string]any
}

type About string

const (
	AboutSelf    About = "self" // the agent's own life, including things it just decided
	AboutSpeaker About = "speaker"
)

type Memory struct {
	About About
	Text  string
}

type Reminder struct {
	When string // local time, "2006-01-02T15:04"
	What string
}

type Promise struct {
	By      string // when it will send it, local time, "2006-01-02T15:04"
	What    string
	Ability string // the ability it needs, e.g. "read_article"; "" if it only writes
	Query   string
	As      string // the file it sends it as, e.g. "pdf"; "" for a message
	Work    string // the ref in the note of a work it already made that this sends, e.g. "w2"; "" to make one
}

// Send is a work it already made, sent with the reply as a file.
type Send struct {
	Work string // its ref in the note, e.g. "w2"; "" for none
	As   string // e.g. "pdf"
}

type Permission struct {
	Class   string
	Verdict string
}

type Correction struct {
	Fact   string // the fact's ref in the note, e.g. "f2"
	Forget bool   // forget it; otherwise replace it with Text
	Text   string
}

type Feedback struct {
	Skill string
	Level int // 1 to 10
	About string
}

var ErrCutOff = errors.New("the answer was cut off before it was finished")

type Taste struct {
	Kind  string // "" for any kind of work
	Likes bool
	Text  string
}

type GoalTaken struct {
	Skill    string
	Level    int // a new skill's starting level, 1 to 10; 0 for one it has
	Text     string
	DoneWhen string
	Due      string // YYYY-MM-DD
	DreamID  string // "" for a new one
	NewDream string // third person with its name; "" for an existing one
}

type LessonLearned struct {
	Skill string
	Text  string
}

type Role string

const (
	RolePerson Role = "person"
	RoleAgent  Role = "agent"
)

type Turn struct {
	Role Role
	Text string
}

type Response struct {
	Provider string
	Text     string
	// Remember to Goals are filled only with CollectFacts. Permissions,
	// Feedback, Lessons, Tastes and Goals count only from the owner.
	Remember []Memory
	// WhoTheyAre is one sentence with names ("Ada is Daniel's cousin."); "" if not told.
	WhoTheyAre  string
	Reminders   []Reminder
	Promises    []Promise
	Permissions []Permission
	Corrections []Correction
	// Misread is the intent choice the speaker's previous message really was,
	// when they said it was misread; "" if not.
	Misread  string
	Feedback []Feedback
	Lessons  []LessonLearned
	Tastes   []Taste
	Goals    []GoalTaken
	Send     Send
	Usage    Usage
	CostUSD  float64 // 0 when no price is configured
}

type Usage struct {
	InputTokens     int
	OutputTokens    int
	ReasoningTokens int // part of OutputTokens
}
