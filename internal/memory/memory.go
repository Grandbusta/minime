// Package memory holds what the agent remembers: conversations, facts, the
// people it knows, and what it means to do.
package memory

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"
)

// Exchange is one message and the agent's reply ("" if none was recorded).
// Message is "" when the agent spoke first.
type Exchange struct {
	At      time.Time
	Message string
	Reply   string
}

const (
	KnownFromBirth = "born" // known from the start, like a parent; never overwritten by a claim
	KnownByMeeting = "met"
	KnownByTelling = "told"
)

const CircleStranger = "stranger"

type Person struct {
	Key       string // e.g. "telegram:123"
	Name      string
	Relation  string // e.g. "Busta is Daniel's father."; "" if not known
	Circle    string // family, friend, school, ... or CircleStranger
	Source    string // KnownFromBirth, KnownByMeeting or KnownByTelling
	FirstSeen time.Time
	UpdatedAt time.Time
}

const SubjectSelf = "self"

type Fact struct {
	ID        ulid.ULID
	PerceptID ulid.ULID
	Subject   string // SubjectSelf, or the person's key (e.g. "telegram:123")
	Text      string // as said; pronouns are resolved to names when used
	At        time.Time
	Areas     []string // e.g. ["home", "family"]; empty if not labelled
}

// Intention is something the agent means to do later: for a person, a
// reminder or a promised task; for itself (SubjectSelf), an activity of its day.
type Intention struct {
	ID        ulid.ULID
	Subject   string    // SubjectSelf, or who it is for, e.g. "telegram:123"
	Kind      string    // IntentionRemind, IntentionTask or IntentionActivity
	PerceptID ulid.ULID // zero if the agent planned it itself
	ProjectID ulid.ULID
	Text      string
	Ability   string // e.g. "read_book"; "" if it only writes
	Args      map[string]string
	DueAt     time.Time
	Status    string // IntentionPending, IntentionAwaiting, IntentionDone, IntentionMissed or IntentionCancelled
	Result    string // e.g. the title of what it made; "" until done
	CreatedAt time.Time
	DoneAt    time.Time
}

const (
	IntentionRemind   = "remind"
	IntentionTask     = "task"
	IntentionActivity = "activity"
)

const (
	IntentionPending   = "pending"
	IntentionDone      = "done"
	IntentionMissed    = "missed"   // could not be done in time
	IntentionAwaiting  = "awaiting" // waiting for the owner's approval (the autonomy gate)
	IntentionCancelled = "cancelled"
)

type Project struct {
	ID        ulid.ULID
	Title     string
	Kind      string // music, writing, reading, learning, ...
	Status    string // ProjectActive, ProjectDone or ProjectDropped
	Notes     string // where it got to, so the next session continues from there
	SourceID  ulid.ULID
	Position  int      // in bytes of its text
	Source    *Reading // set only when loaded with the project
	GoalID    ulid.ULID
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (p Project) PercentRead() int {
	if p.Source == nil || p.Source.Chars <= 0 {
		return 0
	}
	return min(100, p.Position*100/p.Source.Chars)
}

const (
	ReadBook    = "book"    // a public-domain book, read over many days
	ReadArticle = "article" // a Wikipedia article, read at once
)

// Reading is a book or article, without its text.
type Reading struct {
	Kind   string // ReadBook or ReadArticle
	Title  string
	Author string
	Chars  int // in bytes
}

type Source struct {
	ID ulid.ULID
	Reading
	URL       string
	Text      string
	FetchedAt time.Time
}

const (
	ProjectActive  = "active"
	ProjectDone    = "done"
	ProjectDropped = "dropped"
)

type Work struct {
	ID          ulid.ULID
	IntentionID ulid.ULID
	ProjectID   ulid.ULID
	Kind        string // journal, lyrics, beat_idea, notes, ...
	Title       string
	Text        string
	CreatedAt   time.Time
	Public      *bool  // whether it may go on its public page; nil until decided
	PublicBy    string // who decided: PublicBySystem1 or PublicByOwner; "" until decided
	// Loaded only by WorksFor. Embedding is nil until embedded; MadeFor is
	// whom it was made for (SubjectSelf for itself), "" if not for an intention.
	Embedding    []float32
	EmbeddedWith string
	MadeFor      string
}

type Approval struct {
	ID        ulid.ULID
	Class     string // the action class, e.g. "reach_out"
	Person    string
	What      string
	Text      string    // reach_out: the message it would send
	Kind      string    // reach_out: the percept kind, e.g. "reminder"
	Ref       ulid.ULID // the intention or work it is about
	Status    string    // ApprovalPending, ApprovalApproved, ApprovalDenied or ApprovalExpired
	AskedAt   time.Time
	SentAt    time.Time // when the owner was asked; zero until then
	DecidedAt time.Time
	Message   json.RawMessage // where the request was sent, to update it after; nil until sent
}

const (
	ApprovalPending  = "pending"
	ApprovalApproved = "approved"
	ApprovalDenied   = "denied"
	ApprovalExpired  = "expired" // not answered in time: denied
)

// Label is what a System 1 answer should have been, from the owner.
type Label struct {
	DecisionID ulid.ULID
	Question   string
	Value      string // a choice, or "true" / "false" for a yes/no question
	Source     string // LabelFromChat or LabelFromConsole
	At         time.Time
}

const (
	LabelFromChat    = "chat"
	LabelFromConsole = "console"
)

type Dream struct {
	ID      ulid.ULID
	Text    string // third person with its name
	Status  string // DreamActive or DreamDropped
	Source  string // DreamFromLife (its creator), DreamFromSelf or DreamFromOwner
	Since   time.Time
	EndedAt time.Time
}

const (
	DreamActive    = "active"
	DreamDropped   = "dropped"
	DreamFromLife  = "life"
	DreamFromSelf  = "self"
	DreamFromOwner = "owner" // its owner asked it to take on a goal no dream of its own covered
)

type Goal struct {
	ID        ulid.ULID
	DreamID   ulid.ULID
	Text      string
	DoneWhen  string
	Due       time.Time
	Status    string // GoalProposed, GoalActive, GoalDone or GoalDropped
	Progress  string // from the last review
	Skill     string // if it's about one; it then has a learning plan
	CreatedAt time.Time
	UpdatedAt time.Time
}

type LearningPlan struct {
	ID        ulid.ULID
	GoalID    ulid.ULID
	Skill     string
	Stage     int    // the current stage, from 0
	Status    string // PlanActive, PlanDone or PlanDropped
	Stages    []Stage
	CreatedAt time.Time
	UpdatedAt time.Time
}

const (
	PlanActive  = "active"
	PlanDone    = "done"
	PlanDropped = "dropped"
)

type Stage struct {
	N        int
	Learn    string
	Practice string
	DoneWhen string
	DoneAt   time.Time
}

type Resource struct {
	ID      ulid.ULID
	PlanID  ulid.ULID
	Stage   int
	Ability string
	Query   string
	Why     string
	Status  string    // ResourceTodo or ResourceDone
	Title   string    // what it turned out to be; "" until read
	WorkID  ulid.ULID // its notes on it; zero until read
	AddedAt time.Time
	DoneAt  time.Time
}

const (
	ResourceTodo = "todo"
	ResourceDone = "done"
)

const (
	GoalProposed = "proposed" // waiting for its owner's answer before it's set
	GoalActive   = "active"
	GoalDone     = "done"
	GoalDropped  = "dropped"
)

type Skill struct {
	Name      string
	Level     float64 // 1 to 10, from recent evidence
	ShownBy   []string
	UpdatedAt time.Time
}

type Evidence struct {
	ID     ulid.ULID
	Skill  string
	WorkID ulid.ULID // zero for feedback on no one work
	Score  float64   // 1 to 10
	Source string    // EvidenceSystem1, EvidenceOwner or EvidenceLife
	Note   string
	At     time.Time
}

const (
	EvidenceSystem1 = "system1"
	EvidenceOwner   = "owner"
	EvidenceLife    = "life" // the level its life story starts it at: a first score, outweighed as real evidence comes
)

type Lesson struct {
	ID        ulid.ULID
	Skill     string
	Text      string
	Source    string // LessonFromOwner, LessonFromSelf or LessonFromOthers
	WorkID    ulid.ULID
	At        time.Time
	RetiredAt time.Time // zero while active
}

const (
	LessonFromOwner  = "owner"
	LessonFromSelf   = "self"
	LessonFromOthers = "others"
)

type Principle struct {
	ID        ulid.ULID
	Text      string
	Kind      string // PrincipleRule, PrincipleRank or PrincipleLimit
	Rank      int    // a priority's place, 1 first; 0 for the others
	Types     []string
	Status    string   // JudgmentProposed, JudgmentApproved or JudgmentRejected
	Source    string   // JudgmentFromSeed or JudgmentFromReview
	Seed      string   // its text in the seed file; empty for others
	Cases     []string // keys of the cases behind it
	CreatedAt time.Time
	UpdatedAt time.Time
}

type JudgmentCase struct {
	ID        ulid.ULID
	Key       string // the seed file's key; the ID for others
	Type      string // general, engineering, product, autonomy or life
	Situation string
	Options   []string
	Choice    string
	Reason    string // the owner's own words; empty when they gave none
	Status    string
	Source    string
	Outcome   string // set when it came from a call that was looked back on
	At        time.Time
	UpdatedAt time.Time
	// Embedding is nil until embedded; EmbeddedWith names the model.
	Embedding    []float32
	EmbeddedWith string
}

// Text is what is embedded.
func (c JudgmentCase) Text() string {
	t := strings.TrimSuffix(c.Situation, ".") + "."
	if len(c.Options) > 0 {
		t += " Options: " + strings.Join(c.Options, "; ") + "."
	}
	t += " Chose: " + strings.TrimSuffix(c.Choice, ".") + "."
	if c.Reason != "" {
		t += " Why: " + c.Reason
	}
	return t
}

// Taste is something its owner likes or doesn't in its work, beyond how good
// it is: "likes hooks built on one repeated word".
type Taste struct {
	ID        ulid.ULID
	Kind      string // e.g. "beat_idea"; "" any
	Likes     bool
	Text      string
	WorkID    ulid.ULID
	Source    string // TasteFromTelegram or TasteFromConsole
	At        time.Time
	RetiredAt time.Time // zero while true
}

const (
	TasteFromTelegram = "telegram"
	TasteFromConsole  = "console"
)

const (
	PrincipleRule  = "principle"
	PrincipleRank  = "priority" // what matters most, in order: the higher wins when rules clash
	PrincipleLimit = "boundary" // never, whatever the gain
)

const (
	JudgmentProposed = "proposed" // waiting for the owner's review; not used
	JudgmentApproved = "approved"
	JudgmentRejected = "rejected"

	JudgmentFromSeed   = "seed"   // the owner's judgment file
	JudgmentFromReview = "review" // drawn by its weekly review from where the owner chose differently
)

// Call is a decision where it predicted what its owner would choose. The
// owner's answer, when they give one, says whether they agree.
type Call struct {
	ID          ulid.ULID
	Type        string // CallFocus, CallStart, ...
	Mode        string // CallAfter or CallBefore
	Situation   string
	Options     []string
	Pick        int
	Why         string
	Confidence  float64 // how sure it is the owner would agree, 0 to 1; 0 unknown
	Stakes      string  // StakesLow, StakesMedium or StakesHigh; "" unknown
	Reversible  bool
	Ref         ulid.ULID
	Status      string // CallLogged, CallAsked, CallAnswered or CallExpired
	Waiting     string // CallWaitingReason or CallWaitingAnswer: what the owner's next message is; "" nothing
	WaitingAt   time.Time
	OwnerPick   int // an index into Options; -1 their own answer (OwnerChoice)
	OwnerChoice string
	OwnerReason string
	Agreed      *bool // nil until answered
	AnsweredBy  string
	Message     json.RawMessage
	CaseID      ulid.ULID
	CreatedAt   time.Time
	AskedAt     time.Time
	AnsweredAt  time.Time
	// Looked back on later; empty until then.
	Outcome     string // OutcomeWell, OutcomeMixed or OutcomeBadly
	Verdict     string // VerdictGood, VerdictLucky, VerdictUnlucky or VerdictBad
	OutcomeNote string
	OutcomeBy   string // OutcomeBySelf or OutcomeByOwner
	OutcomeAt   time.Time
}

const (
	StakesLow    = "low"    // little lost if it's wrong: an afternoon, a draft
	StakesMedium = "medium" // days of work, a goal, someone's expectations
	StakesHigh   = "high"   // money, reputation, a relationship, something public
)

// StakesLevels are lowest first.
var StakesLevels = []string{StakesLow, StakesMedium, StakesHigh}

func (c Call) AtStake() float64 {
	w := map[string]float64{StakesLow: 1, StakesMedium: 2, StakesHigh: 3}[c.Stakes]
	if w == 0 {
		w = 2
	}
	if !c.Reversible {
		w *= 2
	}
	return w
}

const (
	OutcomeWell  = "went_well"
	OutcomeMixed = "mixed"
	OutcomeBadly = "went_badly"
)

const (
	VerdictGood    = "good_call" // a good decision, and it went well
	VerdictLucky   = "lucky"     // it went well despite the decision
	VerdictUnlucky = "unlucky"   // a good decision that went badly anyway
	VerdictBad     = "bad_call"  // it went badly because of the decision

	OutcomeBySelf  = "self"
	OutcomeByOwner = "owner" // its owner, which its review never overrides
)

var (
	Outcomes = []string{OutcomeWell, OutcomeMixed, OutcomeBadly}
	Verdicts = []string{VerdictGood, VerdictLucky, VerdictUnlucky, VerdictBad}
)

const (
	CallFocus    = "focus" // what to work on today (the day plan)
	CallStart    = "start" // starting something new rather than going on (the day plan)
	CallNewGoal  = "new_goal"
	CallDropGoal = "drop_goal"
)

const (
	CallAfter  = "after"  // it acts on its pick; the owner grades it later
	CallBefore = "before" // it waits for the owner's answer; unanswered, it goes with its pick

	CallLogged   = "logged" // made, not asked: graded in the console, if ever
	CallAsked    = "asked"
	CallAnswered = "answered"
	CallExpired  = "expired"

	CallWaitingReason = "reason" // the owner chose differently: their next message says why
	CallWaitingAnswer = "answer" // the owner chose something else: their next message says what, and why

	AnsweredOnTelegram = "telegram"
	AnsweredInConsole  = "console"
	AnsweredByApproval = "approval" // answering an approval: a yes or no to what it wanted
)

func CallCaseType(callType string) string {
	switch callType {
	case "publish":
		return "product"
	case "commit", "reach_out":
		return "autonomy"
	}
	return "life"
}

// Agreement is over the latest calls of one type the owner answered.
type Agreement struct {
	Type     string
	Answered int
	Agreed   int
}

const (
	PublicBySystem1 = "system1"  // the daily check (public_work question set)
	PublicByOwner   = "owner"    // its owner, which the daily check never overrides
	PublicByGate    = "gate"     // the autonomy gate: forbidden, or the owner didn't answer
	PublicAwaiting  = "awaiting" // judged public, waiting for the owner's approval
)

// Conversation is where the agent last talked with a person, so it can
// message them first.
type Conversation struct {
	Person       string
	Source       string
	Thread       string          // e.g. the Telegram chat ID
	Payload      json.RawMessage // the channel's details from their last message, used to address a message
	LastActivity time.Time
}
