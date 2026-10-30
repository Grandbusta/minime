// Package memory holds what the agent remembers: working memory (the recent
// back-and-forth of a conversation), facts, the people it knows, and what it
// means to do: for them, and in its own day.
package memory

import (
	"encoding/json"
	"strings"
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
	Subject   string            // SubjectSelf, or who it is for, e.g. "telegram:123"
	Kind      string            // IntentionRemind, IntentionTask or IntentionActivity
	PerceptID ulid.ULID         // the message it came from; zero if the agent planned it itself
	ProjectID ulid.ULID         // the project it is part of; zero if none
	Text      string            // what to do, e.g. "call mum", "write the hook for Lagos Rain"
	Ability   string            // the ability an activity uses, e.g. "read_book"; "" if it only writes
	Args      map[string]string // what the plan gave the ability, e.g. {"query": "polyrhythm"}
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
	IntentionMissed    = "missed"   // could not be done in time
	IntentionAwaiting  = "awaiting" // waiting for the owner's approval (the autonomy gate)
	IntentionCancelled = "cancelled"
)

// Project is something that takes the agent more than a day: a book, a beat, a topic.
type Project struct {
	ID        ulid.ULID
	Title     string
	Kind      string    // music, writing, reading, learning, ...
	Status    string    // ProjectActive, ProjectDone or ProjectDropped
	Notes     string    // where it got to, so the next session continues from there
	SourceID  ulid.ULID // the book a reading project follows; zero if none
	Position  int       // how far into it, in bytes of its text
	Source    *Reading  // about that book, when loaded with the project; nil if none
	GoalID    ulid.ULID // the goal it serves; zero if none
	CreatedAt time.Time
	UpdatedAt time.Time
}

// PercentRead is how far it is through the book it follows; 0 if none.
func (p Project) PercentRead() int {
	if p.Source == nil || p.Source.Chars <= 0 {
		return 0
	}
	return min(100, p.Position*100/p.Source.Chars)
}

// What the agent reads from.
const (
	ReadBook    = "book"    // a public-domain book, read over many days
	ReadArticle = "article" // a Wikipedia article, read at once
)

// Reading is a book or article, without its text.
type Reading struct {
	Kind   string // ReadBook or ReadArticle
	Title  string
	Author string
	Chars  int // length of its text, in bytes
}

// Source is a book or article the agent fetched, with its text.
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

// Work is something the agent made: a journal entry, lyrics, a beat idea, notes.
type Work struct {
	ID          ulid.ULID
	IntentionID ulid.ULID // the intention it came from; zero if none
	ProjectID   ulid.ULID // zero if none
	Kind        string    // journal, lyrics, beat_idea, notes, ...
	Title       string
	Text        string
	CreatedAt   time.Time
	Public      *bool  // whether it may go on its public page; nil until decided
	PublicBy    string // who decided: PublicBySystem1 or PublicByOwner; "" until decided
}

// Approval is something the agent asked its owner before doing it: the
// autonomy gate's "ask".
type Approval struct {
	ID        ulid.ULID
	Class     string    // the action class, e.g. "reach_out"
	Person    string    // who it concerns, e.g. "telegram:123"; "" if no one
	What      string    // what it asks, as the owner sees it
	Text      string    // reach_out: the message it would send
	Kind      string    // reach_out: the percept kind, e.g. "reminder"
	Ref       ulid.ULID // the intention or work it is about; zero if none
	Status    string    // ApprovalPending, ApprovalApproved, ApprovalDenied or ApprovalExpired
	AskedAt   time.Time
	SentAt    time.Time       // when the owner was asked; zero until then
	DecidedAt time.Time       // zero until decided
	Message   json.RawMessage // where the request was sent, to update it after; nil until sent
}

const (
	ApprovalPending  = "pending"
	ApprovalApproved = "approved"
	ApprovalDenied   = "denied"
	ApprovalExpired  = "expired" // not answered in time: denied
)

// Label is what a System 1 answer should have been, from the owner: a
// correction in chat or a label in the console.
type Label struct {
	DecisionID ulid.ULID
	Question   string // e.g. "intent"
	Value      string // a choice, or "true" / "false" for a yes/no question
	Source     string // LabelFromChat or LabelFromConsole
	At         time.Time
}

const (
	LabelFromChat    = "chat"
	LabelFromConsole = "console"
)

// Dream is who the agent wants to become.
type Dream struct {
	ID      ulid.ULID
	Text    string // third person with its name
	Status  string // DreamActive or DreamDropped
	Source  string // DreamFromLife (its creator) or DreamFromSelf (it chose it)
	Since   time.Time
	EndedAt time.Time // zero while active
}

const (
	DreamActive   = "active"
	DreamDropped  = "dropped"
	DreamFromLife = "life"
	DreamFromSelf = "self"
)

// Goal is a step toward a dream, checkable in weeks.
type Goal struct {
	ID        ulid.ULID
	DreamID   ulid.ULID
	Text      string    // e.g. "Finish five beat ideas for an EP"
	DoneWhen  string    // how it'll know it's done
	Due       time.Time //
	Status    string    // GoalActive, GoalDone or GoalDropped
	Progress  string    // where it stands, from the last review
	Skill     string    // the skill it grows, if it's about one; it then has a learning plan
	CreatedAt time.Time
	UpdatedAt time.Time
}

// LearningPlan is how it means to grow a skill for a goal: stages it works
// through, like a self-taught learner.
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

// Stage is one step of a learning plan.
type Stage struct {
	N        int
	Learn    string    // what to learn
	Practice string    // a piece to make that practises it
	DoneWhen string    // how it'll know it's done
	DoneAt   time.Time // zero until done
}

// Resource is something to study for a stage of a learning plan.
type Resource struct {
	ID      ulid.ULID
	PlanID  ulid.ULID
	Stage   int
	Ability string // how it's read, e.g. "read_article"
	Query   string // what to look for
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

// Skill is something the agent is getting better at.
type Skill struct {
	Name      string
	Level     float64  // 1 to 10, from recent evidence
	ShownBy   []string // kinds of work that show it
	UpdatedAt time.Time
}

// Evidence is one score of a skill: of a work, or the owner's feedback.
type Evidence struct {
	ID     ulid.ULID
	Skill  string
	WorkID ulid.ULID // the work scored; zero for feedback on no one work
	Score  float64   // 1 to 10
	Source string    // EvidenceSystem1 or EvidenceOwner
	Note   string
	At     time.Time
}

const (
	EvidenceSystem1 = "system1"
	EvidenceOwner   = "owner"
	EvidenceLife    = "life" // the level its life story starts it at: a first score, outweighed as real evidence comes
)

// Lesson is something the agent learned about doing a skill well.
type Lesson struct {
	ID        ulid.ULID
	Skill     string
	Text      string    // a rule for next time, in its own words
	Source    string    // LessonFromOwner, LessonFromSelf or LessonFromOthers
	WorkID    ulid.ULID // the work that taught it; zero if none
	At        time.Time
	RetiredAt time.Time // zero while active
}

const (
	LessonFromOwner  = "owner"  // its owner's correction
	LessonFromSelf   = "self"   // what it noticed went wrong
	LessonFromOthers = "others" // studying how others do it
)

// Principle is a rule of its owner's judgment, drawn from their decisions.
type Principle struct {
	ID        ulid.ULID
	Text      string
	Kind      string   // PrincipleRule, PrincipleRank or PrincipleLimit
	Rank      int      // a priority's place, 1 first; 0 for the others
	Types     []string // kinds of decision it applies to
	Status    string   // JudgmentProposed, JudgmentApproved or JudgmentRejected
	Source    string   // JudgmentFromSeed, ...
	Seed      string   // its text in the seed file; empty for others
	Cases     []string // keys of the cases behind it
	CreatedAt time.Time
	UpdatedAt time.Time
}

// JudgmentCase is a decision its owner made: what was on the table, what
// they chose, and why.
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
	Outcome   string // how it turned out, when it came from a call that was looked back on; else empty
	At        time.Time
	UpdatedAt time.Time
	// Embedding is what it means, as a vector (embedded with EmbeddedWith),
	// to find the decisions most like one being made; nil until embedded.
	Embedding    []float32
	EmbeddedWith string
}

// Text is a decision as one piece of text: what was on the table, what was
// chosen and why. It's what is embedded.
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
	Kind      string // the kind of work it's about, e.g. "beat_idea"; "" any
	Likes     bool   // a like, or a dislike
	Text      string // the taste, in general terms
	WorkID    ulid.ULID
	Source    string // TasteFromTelegram or TasteFromConsole
	At        time.Time
	RetiredAt time.Time // zero while true
}

const (
	TasteFromTelegram = "telegram"
	TasteFromConsole  = "console"
)

// Kinds of principle.
const (
	PrincipleRule  = "principle" // a rule drawn from decisions
	PrincipleRank  = "priority"  // what matters most, in order: the higher wins when rules clash
	PrincipleLimit = "boundary"  // never, whatever the gain
)

const (
	JudgmentProposed = "proposed" // waiting for the owner's review; not used
	JudgmentApproved = "approved"
	JudgmentRejected = "rejected"

	JudgmentFromSeed   = "seed"   // the owner's judgment file
	JudgmentFromReview = "review" // drawn by its weekly review from where the owner chose differently
)

// Call is a decision it made where it predicted what its owner would choose:
// the options, its pick, why, and how sure it is they'd agree. The owner's
// answer, when they give one, says whether they agree.
type Call struct {
	ID          ulid.ULID
	Type        string // CallFocus, CallStart, ...
	Mode        string // CallAfter or CallBefore
	Situation   string
	Options     []string
	Pick        int // its pick, an index into Options
	Why         string
	Confidence  float64   // how sure it is the owner would agree, 0 to 1; 0 unknown
	Stakes      string    // StakesLow, StakesMedium or StakesHigh: what it puts at stake; "" unknown
	Reversible  bool      // whether it can be undone
	Ref         ulid.ULID // the goal or approval it is about; zero if none
	Status      string    // CallLogged, CallAsked, CallAnswered or CallExpired
	Waiting     string    // CallWaitingReason or CallWaitingAnswer: what the owner's next message is; "" nothing
	WaitingAt   time.Time
	OwnerPick   int // an index into Options; -1 their own answer (OwnerChoice)
	OwnerChoice string
	OwnerReason string
	Agreed      *bool // nil until answered
	AnsweredBy  string
	Message     json.RawMessage // where it was asked
	CaseID      ulid.ULID       // the case the answer became; zero if none
	CreatedAt   time.Time
	AskedAt     time.Time
	AnsweredAt  time.Time
	// How it turned out, looked at later; empty until then.
	Outcome     string // OutcomeWell, OutcomeMixed or OutcomeBadly
	Verdict     string // VerdictGood, VerdictLucky, VerdictUnlucky or VerdictBad
	OutcomeNote string // what happened
	OutcomeBy   string // OutcomeBySelf or OutcomeByOwner
	OutcomeAt   time.Time
}

// What a call puts at stake.
const (
	StakesLow    = "low"    // little lost if it's wrong: an afternoon, a draft
	StakesMedium = "medium" // days of work, a goal, someone's expectations
	StakesHigh   = "high"   // money, reputation, a relationship, something public
)

// StakesLevels are the values Stakes may take, lowest first.
var StakesLevels = []string{StakesLow, StakesMedium, StakesHigh}

// AtStake is how much a call puts at stake, for asking about what matters
// most first: its stakes (1 to 3, 2 if unknown), doubled if it can't be undone.
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

// How a call turned out.
const (
	OutcomeWell  = "went_well"
	OutcomeMixed = "mixed"
	OutcomeBadly = "went_badly"
)

// Whether how a call turned out was the decision or luck.
const (
	VerdictGood    = "good_call" // a good decision, and it went well
	VerdictLucky   = "lucky"     // it went well despite the decision
	VerdictUnlucky = "unlucky"   // a good decision that went badly anyway
	VerdictBad     = "bad_call"  // it went badly because of the decision

	OutcomeBySelf  = "self"  // its weekly review
	OutcomeByOwner = "owner" // its owner, which its review never overrides
)

// Outcomes and Verdicts are the values each may take.
var (
	Outcomes = []string{OutcomeWell, OutcomeMixed, OutcomeBadly}
	Verdicts = []string{VerdictGood, VerdictLucky, VerdictUnlucky, VerdictBad}
)

// Kinds of call.
const (
	CallFocus    = "focus"     // what to work on today (the day plan)
	CallStart    = "start"     // starting something new rather than going on (the day plan)
	CallNewGoal  = "new_goal"  // setting a goal (the weekly review)
	CallDropGoal = "drop_goal" // dropping a goal (the weekly review)
)

const (
	CallAfter  = "after"  // it acts on its pick; the owner grades it later
	CallBefore = "before" // it waits for the owner's answer; unanswered, it goes with its pick

	CallLogged   = "logged"   // made, not asked: graded in the console, if ever
	CallAsked    = "asked"    // asked on Telegram
	CallAnswered = "answered" // the owner chose
	CallExpired  = "expired"  // asked, unanswered in time

	CallWaitingReason = "reason" // the owner chose differently: their next message says why
	CallWaitingAnswer = "answer" // the owner chose something else: their next message says what, and why

	AnsweredOnTelegram = "telegram"
	AnsweredInConsole  = "console"
	AnsweredByApproval = "approval" // answering an approval: a yes or no to what it wanted
)

// CallCaseType is the kind of decision a call's answer is filed under as a case.
func CallCaseType(callType string) string {
	switch callType {
	case "publish":
		return "product"
	case "commit", "reach_out":
		return "autonomy"
	}
	return "life"
}

// Agreement is how often the owner agreed with its calls of one type, over
// the latest ones they answered.
type Agreement struct {
	Type     string
	Answered int
	Agreed   int
}

// Who decided whether a work is public.
const (
	PublicBySystem1 = "system1"  // the daily check (public_work question set)
	PublicByOwner   = "owner"    // its owner, which the daily check never overrides
	PublicByGate    = "gate"     // the autonomy gate: forbidden, or the owner didn't answer
	PublicAwaiting  = "awaiting" // judged public, waiting for the owner's approval
)

// Conversation is where the agent last talked with a person, so it can
// message them first.
type Conversation struct {
	Person       string          // e.g. "telegram:123"
	Source       string          // channel, e.g. "telegram"
	Thread       string          // e.g. the Telegram chat ID
	Payload      json.RawMessage // the channel's details from their last message, used to address a message
	LastActivity time.Time       // the last thing said in the thread, by either side
}
