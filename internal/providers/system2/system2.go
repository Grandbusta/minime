// Package system2 defines the provider-neutral interface to slow, deliberate
// reasoning models (OpenAI Responses, ...). Each provider lives in its own subpackage.
package system2

import "context"

// Reasoner thinks a request through and writes a reply.
type Reasoner interface {
	Reason(ctx context.Context, req Request) (Response, error)
}

type Request struct {
	Instructions    string // who the agent is and how to answer
	History         []Turn // the conversation so far, oldest first
	Note            string // context about this turn from the rest of the mind
	Message         string // the person's message to reply to; "" when the agent speaks first
	MaxOutputTokens int    // 0: the provider's default
	// CollectFacts asks for the lasting facts to remember, each about the
	// agent itself or the person speaking, alongside the reply
	// (Response.Remember), plus any reminders it agreed to set (Response.Reminders).
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
	Usage     Usage
	CostUSD   float64 // 0 when no price is configured
}

type Usage struct {
	InputTokens     int
	OutputTokens    int
	ReasoningTokens int // part of OutputTokens
}
