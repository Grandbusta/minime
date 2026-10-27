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
	Message         string // the person's message to reply to
	MaxOutputTokens int    // 0: the provider's default
	// CollectFacts asks for the lasting facts to remember, each about the
	// agent itself or the person speaking, alongside the reply (Response.Remember).
	CollectFacts bool
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
	Usage      Usage
	CostUSD    float64 // 0 when no price is configured
}

type Usage struct {
	InputTokens     int
	OutputTokens    int
	ReasoningTokens int // part of OutputTokens
}
