// Package system2 defines the provider-neutral interface to slow, deliberate
// reasoning models (OpenAI Responses, ...). Each provider lives in its own subpackage.
package system2

import (
	"context"
	"errors"
)

// Reasoner thinks a request through and writes a reply.
type Reasoner interface {
	Reason(ctx context.Context, req Request) (Response, error)
}

type Request struct {
	Instructions    string // who the agent is and how to answer
	History         []Turn // the conversation so far, oldest first
	Note            string // context about this turn from the rest of the mind
	Message         string // the person's message to reply to; "" when the agent speaks first
	MaxOutputTokens int    // the most it may write, reasoning included; 0: the provider's default
	// CollectFacts asks for the lasting facts to remember, each about the
	// agent itself or the person speaking, alongside the reply
	// (Response.Remember), plus any reminders it agreed to set (Response.Reminders)
	// and things it promised to make (Response.Promises).
	CollectFacts bool
	// Format, when set, asks for JSON that matches it instead of plain text;
	// Response.Text is then that JSON. Not used together with CollectFacts.
	Format *Format
}

// Format is a JSON Schema for a structured answer, e.g. the profile page.
type Format struct {
	Name   string
	Schema map[string]any
}

// Whose life a remembered fact is about.
type About string

const (
	AboutSelf    About = "self"    // the agent's own life, including things it just decided
	AboutSpeaker About = "speaker" // the person it is talking to
)

type Memory struct {
	About About
	Text  string // standalone, e.g. "Sam goes to Grace Chapel"
}

// Reminder is a reminder System 2 agreed to set.
type Reminder struct {
	When string // local time, "2006-01-02T15:04"
	What string // e.g. "call mum"
}

// Promise is something the agent agreed to make for the speaker and send later.
type Promise struct {
	By      string // when it will send it, local time, "2006-01-02T15:04"
	What    string // what it will make, e.g. "Daniel writes Sam a verse about Sunday mornings"
	Ability string // the ability it needs, e.g. "read_article"; "" if it only writes
	Query   string // what the ability is to look for; "" if none
}

// Permission is a change the owner made to what the agent may do on its own.
type Permission struct {
	Class   string // an action class, e.g. "reach_out"
	Verdict string // auto, ask or forbid
}

// Correction is a fact the speaker said is wrong (replaced by Text) or asked
// it to forget.
type Correction struct {
	Fact   string // the fact's ref in the note, e.g. "f2"
	Forget bool   // forget it; otherwise replace it with Text
	Text   string // the right fact, in the third person with names
}

// Feedback is what the speaker thought of something the agent made, as a
// level of one of its skills.
type Feedback struct {
	Skill string // one of its skills, e.g. "lyric writing"
	Level int    // 1 to 10
	About string // what they judged, in a few words
}

// LessonLearned is specific advice the speaker gave about doing a skill well.
// ErrCutOff means the answer ran out of room before it was finished.
var ErrCutOff = errors.New("the answer was cut off before it was finished")

// Taste is something the speaker likes or doesn't in work like the agent's,
// beyond how good it is.
type Taste struct {
	Kind  string // the kind of work it's about; "" any
	Likes bool
	Text  string // in general terms, e.g. "likes hooks built on one repeated word"
}

// GoalTaken is a goal the speaker asked it to take on, and it agreed to.
type GoalTaken struct {
	Skill    string // the skill it grows: one it has, or a new one
	Level    int    // a new skill's starting level, 1 to 10; 0 for one it has
	Text     string // the goal
	DoneWhen string // a concrete test of done
	Due      string // YYYY-MM-DD
	DreamID  string // the dream it serves, by id; "" for a new one
	NewDream string // a new dream it serves, third person with its name; "" for an existing one
}

type LessonLearned struct {
	Skill string // one of its skills
	Text  string // the lesson, as a rule for next time
}

type Role string

const (
	RolePerson Role = "person" // whoever the agent is talking with
	RoleAgent  Role = "agent"  // the agent itself
)

type Turn struct {
	Role Role
	Text string
}

type Response struct {
	Provider string // e.g. "openai/gpt-6-luna"
	Text     string
	Remember []Memory // with CollectFacts: facts to keep
	// WhoTheyAre, with CollectFacts: who the speaker said they are to the
	// agent, as one sentence with names ("Ada is Daniel's cousin."); "" if not told.
	WhoTheyAre string
	// Reminders, with CollectFacts: what the speaker asked to be reminded of.
	Reminders []Reminder
	// Promises, with CollectFacts: what the agent agreed to make for the speaker.
	Promises []Promise
	// Permissions, with CollectFacts: changes the speaker made to what the
	// agent may do on its own (only the owner's count).
	Permissions []Permission
	// Corrections, with CollectFacts: facts the speaker said are wrong, or asked it to forget.
	Corrections []Correction
	// Misread, with CollectFacts: what the speaker's previous message was
	// really doing (one of the intent choices), when they said it was misread; "" if not.
	Misread string
	// Feedback, with CollectFacts: what the speaker thought of its work (only the owner's count).
	Feedback []Feedback
	// Lessons, with CollectFacts: specific advice the speaker gave about its work (only the owner's count).
	Lessons []LessonLearned
	// Tastes, with CollectFacts: what the speaker likes or doesn't in its work (only the owner's count).
	Tastes []Taste
	// Goals, with CollectFacts: goals the speaker asked it to take on (only the owner's count).
	Goals   []GoalTaken
	Usage   Usage
	CostUSD float64 // 0 when no price is configured
}

type Usage struct {
	InputTokens     int
	OutputTokens    int
	ReasoningTokens int // part of OutputTokens
}
