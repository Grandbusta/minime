// Package mind runs the cognitive cycle: take a percept, decide, act, record.
//
// The agent is a person with its own life (self.Life), talking with people
// it knows. Every text message is read by System 1 (fast, typed answers),
// and the router decides how sure that reading is. System 2 (slow reasoning)
// replies as the agent itself whenever words are needed, System 1 is not
// sure, or the person is answering something the agent just asked.
//
// Memory has two sides: facts about the agent itself (decided when they
// first come up) and facts about each person. Pronouns are resolved to names
// before System 1 reads anything, so "your church" and "my church" are
// never confused.
//
// A clock ticks into the same loop (initiative.go), so the agent can speak
// first: reminders people asked for, and checking in on what is going on in
// their lives. It also lives a day of its own (life.go): it plans it, does
// real things, and writes a journal at night.
package mind

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/oklog/ulid/v2"

	"minime/internal/ability"
	"minime/internal/action"
	"minime/internal/decision"
	"minime/internal/decision/questions"
	"minime/internal/memory"
	"minime/internal/perception"
	"minime/internal/providers/system1"
	"minime/internal/providers/system2"
	"minime/internal/search"
	"minime/internal/self"
)

const nonTextSetID = "reflex.non_text@1"

type Store interface {
	SavePercept(ctx context.Context, p perception.Percept) (isNew bool, err error)
	SaveDecision(ctx context.Context, d decision.Decision) error
	RecentExchanges(ctx context.Context, source, thread string, since time.Time,
		exclude ulid.ULID, limit int) ([]memory.Exchange, error)
	SaveFact(ctx context.Context, f memory.Fact) error
	RecallCandidates(ctx context.Context, subjects, areas []string, limit int) ([]memory.Fact, error)
	UnlabelledFacts(ctx context.Context, limit int) ([]memory.Fact, error)
	SetFactAreas(ctx context.Context, id ulid.ULID, areas []string) error
	Person(ctx context.Context, key string) (memory.Person, bool, error)
	KnownPeople(ctx context.Context, limit int) ([]memory.Person, error)
	SavePerson(ctx context.Context, p memory.Person) error
	AddPersonIfNew(ctx context.Context, p memory.Person) (bool, error)
	SaveIntention(ctx context.Context, i memory.Intention) error
	DueIntentions(ctx context.Context, now time.Time, limit int) ([]memory.Intention, error)
	IntentionsDue(ctx context.Context, from, to time.Time) ([]memory.Intention, error)
	FinishIntention(ctx context.Context, id ulid.ULID, status, result string, at time.Time) error
	PostponeIntention(ctx context.Context, id ulid.ULID, due time.Time) error
	OpenIntentions(ctx context.Context, subject, kind string) ([]memory.Intention, error)
	AwaitIntention(ctx context.Context, id ulid.ULID) error
	ResumeIntention(ctx context.Context, id ulid.ULID, notBefore time.Time) error
	SaveApproval(ctx context.Context, a memory.Approval) error
	Approval(ctx context.Context, id ulid.ULID) (memory.Approval, bool, error)
	UnsentApprovals(ctx context.Context, limit int) ([]memory.Approval, error)
	MarkApprovalSent(ctx context.Context, id ulid.ULID, message []byte, at time.Time) error
	DecideApproval(ctx context.Context, id ulid.ULID, status string, at time.Time) (bool, error)
	StaleApprovals(ctx context.Context, before time.Time, limit int) ([]memory.Approval, error)
	SetWorkPublic(ctx context.Context, id ulid.ULID, public bool, by string) (bool, error)
	SaveAutonomyRule(ctx context.Context, class, verdict, by string, percept ulid.ULID, at time.Time) error
	AutonomyRules(ctx context.Context) (map[string]string, error)
	DeleteFact(ctx context.Context, id ulid.ULID) (bool, error)
	SaveLabel(ctx context.Context, l memory.Label) error
	LastRead(ctx context.Context, setID, source, thread string, before time.Time) (ulid.ULID, bool, error)
	SaveDream(ctx context.Context, d memory.Dream) error
	Dreams(ctx context.Context, activeOnly bool) ([]memory.Dream, error)
	SaveGoal(ctx context.Context, g memory.Goal) error
	Goals(ctx context.Context, activeOnly bool) ([]memory.Goal, error)
	AddSkill(ctx context.Context, sk memory.Skill) error
	Skills(ctx context.Context) ([]memory.Skill, error)
	SetSkillLevel(ctx context.Context, name string, level float64, at time.Time) error
	SaveEvidence(ctx context.Context, e memory.Evidence) error
	RecentEvidence(ctx context.Context, skill string, limit int) ([]memory.Evidence, error)
	SaveLesson(ctx context.Context, l memory.Lesson) error
	Lessons(ctx context.Context, skill string, limit int) ([]memory.Lesson, error)
	RetireLesson(ctx context.Context, id ulid.ULID, at time.Time) error
	MarkReviewAsked(ctx context.Context, work ulid.ULID, at time.Time) error
	AskedForReview(ctx context.Context, since time.Time) (memory.Work, bool, error)
	SavePlan(ctx context.Context, p memory.LearningPlan) error
	Plans(ctx context.Context, activeOnly bool) ([]memory.LearningPlan, error)
	AdvancePlan(ctx context.Context, plan ulid.ULID, at time.Time) error
	EndPlan(ctx context.Context, plan ulid.ULID, status string, at time.Time) error
	SaveResource(ctx context.Context, r memory.Resource) error
	Resources(ctx context.Context, plan ulid.ULID) ([]memory.Resource, error)
	FinishResource(ctx context.Context, id, work ulid.ULID, title string, at time.Time) error
	RecentFound(ctx context.Context, limit int) ([]search.Result, error)
	SeedJudgment(ctx context.Context, cases []memory.JudgmentCase, principles []memory.Principle) (int, int, error)
	Principles(ctx context.Context, status string) ([]memory.Principle, error)
	JudgmentCases(ctx context.Context, status string, types []string) ([]memory.JudgmentCase, error)
	SavePrinciple(ctx context.Context, p memory.Principle) error
	SaveCall(ctx context.Context, c memory.Call) error
	Call(ctx context.Context, id ulid.ULID) (memory.Call, bool, error)
	RecentCalls(ctx context.Context, status string, limit int) ([]memory.Call, error)
	CallsAskedSince(ctx context.Context, mode string, since time.Time) (int, error)
	MarkCallAsked(ctx context.Context, id ulid.ULID, where json.RawMessage, at time.Time) error
	StaleCalls(ctx context.Context, before time.Time, limit int) ([]memory.Call, error)
	ExpireCall(ctx context.Context, id ulid.ULID) (bool, error)
	WaitForOwner(ctx context.Context, id ulid.ULID, waiting string, at time.Time) error
	WaitingCall(ctx context.Context, since time.Time) (memory.Call, bool, error)
	AnswerCall(ctx context.Context, c memory.Call, asCase bool) (bool, error)
	GiveCallReason(ctx context.Context, id ulid.ULID, reason string, at time.Time) error
	IntentionWork(ctx context.Context, intention ulid.ULID) (memory.Work, bool, error)
	SaveProject(ctx context.Context, p memory.Project) error
	Project(ctx context.Context, id ulid.ULID) (memory.Project, bool, error)
	ActiveProjects(ctx context.Context, limit int) ([]memory.Project, error)
	SaveWork(ctx context.Context, w memory.Work) error
	WorksBetween(ctx context.Context, from, to time.Time) ([]memory.Work, error)
	ProjectWorks(ctx context.Context, project ulid.ULID, limit int) ([]memory.Work, error)
	LatestWork(ctx context.Context, kind string) (memory.Work, bool, error)
	CostSince(ctx context.Context, since time.Time, actions []string) (float64, error)
	Conversations(ctx context.Context) ([]memory.Conversation, error)
	LastSpokeFirst(ctx context.Context, person, kind string) (time.Time, bool, error)
	UnaskedFacts(ctx context.Context, person string, since time.Time, limit int) ([]memory.Fact, error)
	MarkFactsAsked(ctx context.Context, ids []ulid.ULID, at time.Time) error
}

// WorkingMemory bounds how much of the current conversation the mind keeps
// in view: at most Turns earlier exchanges, none older than Window.
type WorkingMemory struct {
	Turns  int
	Window time.Duration
}

// Replier answers on the channel a percept came from.
type Replier interface {
	Reply(ctx context.Context, p perception.Percept, text string) error
}

// Prompt is System 2's instructions plus a reference that versions them,
// e.g. "reply@1a2b3c4d", recorded on every System 2 decision.
type Prompt struct {
	Ref  string
	Text string
}

type Config struct {
	Life self.Life // who the agent is
	// Judgment is how its owner decides, seeded into memory for the owner to
	// review; only what they approve is used. Zero value: none seeded.
	Judgment self.Judgment
	Percepts <-chan perception.Percept
	Store    Store
	Replier  Replier
	System1  system1.DecisionModel
	System2  system2.Reasoner
	Incoming questions.Set // how System 1 reads an incoming message
	FollowUp questions.Set // whether it answers the agent's last reply; zero value: never checked
	// LifeArea files messages and facts by part of life (area, personal,
	// broad); zero value: nothing is labelled and every reply searches memory.
	LifeArea questions.Set
	// PersonCircle files a person into a circle of the agent's life (family,
	// school, ...) before their relation is stored; zero value: no circle.
	PersonCircle questions.Set
	// FactRelevance is a one-question template ({fact} marks where each fact
	// goes) for picking which remembered facts System 2 sees; zero value: no recall.
	FactRelevance questions.Set
	// OpenThreads is a one-question template like FactRelevance, used instead
	// of it for small talk: what's going on in the speaker's life worth asking
	// about. Zero value: small talk recalls like any other message.
	OpenThreads      questions.Set
	RecallCandidates int    // facts considered per reply: same life area first, then newest
	ReplyPrompt      Prompt // how System 2 writes a reply
	Working          WorkingMemory
	// Ticks is the clock, about once a minute; nil: the agent never speaks first.
	Ticks <-chan time.Time
	// Zone is the agent's time zone: reminders and check-in times are in it. nil: UTC.
	Zone     *time.Location
	CheckIns CheckIns
	Day      Day // its own day: plan, activities, journal
	// Abilities are what it can do in its day beyond writing: read, write its
	// journal (which its day needs). nil: none.
	Abilities *ability.Set
	// SkillLevel scores a work as evidence of a skill (one score question);
	// zero value: works aren't scored, and skills only move with feedback.
	SkillLevel questions.Set
	// Autonomy is what it may do on its own; the zero value allows everything.
	Autonomy action.Policy
	// Asker asks the owner what Autonomy says to ask first, and (as a
	// Chooser) what they'd choose on its calls; nil: requests wait unsent
	// and expire.
	Asker Asker
	// Calls is how it learns its owner's judgment from its own decisions;
	// zero value: off.
	Calls Calls
	Log   *slog.Logger
}

type Mind struct {
	Config
	lastTick time.Time // when the clock last ticked; zero before the first tick
	planned  string    // the local date ("2006-01-02") its day was last planned for
	reviewed time.Time // the weekly review time it last considered
}

func New(c Config) *Mind {
	if c.Zone == nil {
		c.Zone = time.UTC
	}
	return &Mind{Config: c}
}

func (m *Mind) Run(ctx context.Context) error {
	m.loadPermissions(ctx)
	m.bornKnowing(ctx)
	m.bornWanting(ctx)
	m.seedJudgment(ctx)
	m.labelUnlabelledFacts(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case p, ok := <-m.Percepts:
			if !ok {
				return nil
			}
			m.handle(ctx, p)
		case now := <-m.Ticks:
			m.tick(ctx, now)
		}
	}
}

func (m *Mind) handle(ctx context.Context, p perception.Percept) {
	log := m.Log.With("percept", p.ID.String(), "source", p.Source)
	isNew, err := m.Store.SavePercept(ctx, p)
	if err != nil {
		log.Error("save percept", "err", err)
		return
	}
	if !isNew {
		log.Debug("already handled, skipping", "external_id", p.ExternalID)
		return
	}
	if p.Kind == perception.KindApproval {
		m.answer(ctx, p) // the owner answering a request, not a message
		return
	}
	if p.Kind == perception.KindCall {
		m.pickAnswered(ctx, p) // the owner choosing on one of its calls
		return
	}

	// Who is speaking: remembered from before, or met now. Their name comes
	// from memory once known, so a changed account name does not change them.
	person := m.meet(ctx, p)
	if person.Name != "" {
		p.SpeakerName = person.Name
	}
	if m.ownerReplied(ctx, p) { // why they chose differently, just asked
		return
	}

	start := time.Now()
	root := newDecision(p, nil, start)
	chain := []*decision.Decision{root} // root first; a System 2 hand-off is its child
	var (
		reply string
		after []*decision.Decision // what the reply's answer changed: saved after it
	)
	if p.Text == "" {
		// Reflex: nothing to read, so nothing to think about.
		root.Tier, root.SetID, root.Action = decision.TierReflex, nonTextSetID, "reply"
		reply = "I can only read text for now."
	} else {
		recent := m.recall(ctx, p)
		var (
			why    string
			labels *lifeLabels
		)
		reply, why, labels = m.decideIncoming(ctx, p, root, recent)
		if why != "" {
			var (
				facts          []memory.Fact
				recallDecision *decision.Decision
			)
			if m.OpenThreads.ID != "" && m.readAs(root, "small_talk") {
				facts, recallDecision = m.recallOpenThreads(ctx, p, root)
			} else {
				facts, recallDecision = m.recallFacts(ctx, p, root, labels)
			}
			if recallDecision != nil {
				chain = append(chain, recallDecision)
			}
			child := newDecision(p, &root.ID, time.Now())
			reply, after = m.reason(ctx, p, person, root, child, recent, facts, why)
			chain = append(chain, child)
		}
	}

	// The last decision in the chain is the one that acted, so it owns the outcome.
	actor := chain[len(chain)-1]
	actor.Reply = reply
	actor.Outcome = &decision.Outcome{Status: decision.OutcomeOK}
	if err := m.Replier.Reply(ctx, p, reply); err != nil {
		log.Warn("reply failed", "err", err)
		actor.Outcome = &decision.Outcome{Status: decision.OutcomeFailed, Detail: err.Error()}
	}
	now := time.Now()
	actor.Outcome.At = now.UTC()
	actor.Latency = now.Sub(actor.At)
	if actor != root {
		root.Outcome = &decision.Outcome{Status: decision.OutcomeOK, Detail: "handed to System 2", At: now.UTC()}
		root.Latency = now.Sub(start)
	}
	for _, d := range chain {
		if d.Outcome == nil { // an in-between step that did not set its own
			d.Outcome = &decision.Outcome{Status: decision.OutcomeOK, At: now.UTC()}
		}
	}
	chain = append(chain, after...)

	for _, d := range chain {
		if err := m.Store.SaveDecision(ctx, *d); err != nil {
			log.Error("save decision", "err", err)
			return
		}
		log.Info("decided", "decision", d.ID.String(), "tier", d.Tier, "set", d.SetID,
			"action", d.Action, "provider", d.Provider, "outcome", d.Outcome.Status)
	}
}

func newDecision(p perception.Percept, parent *ulid.ULID, at time.Time) *decision.Decision {
	return &decision.Decision{ID: ulid.Make(), ParentID: parent, At: at.UTC(), PerceptID: p.ID, Gate: decision.GateAuto}
}

// recall brings the conversation so far into working memory. Without it, a
// follow-up like "I am in Ikoyi" makes no sense.
func (m *Mind) recall(ctx context.Context, p perception.Percept) []memory.Exchange {
	if m.Working.Turns <= 0 || p.Thread == "" {
		return nil
	}
	exchanges, err := m.Store.RecentExchanges(ctx, p.Source, p.Thread,
		p.At.Add(-m.Working.Window), p.ID, m.Working.Turns)
	if err != nil {
		m.Log.Warn("recall failed; deciding without conversation context", "percept", p.ID.String(), "err", err)
		return nil
	}
	return exchanges
}

// decideIncoming asks System 1 about a text message and routes on its
// answers. It fills in d and returns either a reply, or (when System 2
// should answer) the reason for handing over, plus how the message was
// filed by life area (nil if it was not).
func (m *Mind) decideIncoming(ctx context.Context, p perception.Percept, d *decision.Decision,
	recent []memory.Exchange) (reply, toSystem2 string, labels *lifeLabels) {
	d.Tier, d.SetID = decision.TierSystem1, m.Incoming.Ref()
	n := m.namesFor(p)

	// Besides the intent read, up to two more System 1 calls run at the same
	// time, each with its own state, since extra state upsets the intent read:
	// is the person answering what the agent just said, and which part of
	// whose life is the message about?
	var (
		follow, life       system1.Response
		followErr, lifeErr error
		lifeLabelsFound    lifeLabels
		wg                 sync.WaitGroup
	)
	previous := lastReply(recent)
	checkFollowUp := previous != "" && m.FollowUp.ID != ""
	if checkFollowUp {
		d.SetID += "," + m.FollowUp.Ref()
		wg.Add(1)
		go func() {
			defer wg.Done()
			follow, followErr = m.System1.Decide(ctx, system1.Request{
				State:     map[string]any{"message": p.Text, "previous_reply": previous},
				Questions: m.FollowUp.Questions,
			})
		}()
	}
	checkLife := m.LifeArea.ID != ""
	if checkLife {
		d.SetID += "," + m.LifeArea.Ref()
		wg.Add(1)
		go func() {
			defer wg.Done()
			lifeLabelsFound, life, lifeErr = m.labelText(ctx, n.message(p.Text), n)
		}()
	}

	// Send only what the questions need: an unrelated timestamp in the state
	// moved intent confidence by up to 0.2 in testing.
	resp, err := m.System1.Decide(ctx, system1.Request{
		State:     map[string]any{"message": p.Text},
		Questions: m.Incoming.Questions,
	})
	wg.Wait()
	if err != nil {
		m.Log.Error("system 1 failed", "percept", p.ID.String(), "err", err)
		d.Action = "error"
		return "Sorry, I couldn't think that through just now.", "", nil
	}
	d.Provider, d.Answers, d.CostUSD = resp.Provider, maps.Clone(resp.Answers), resp.CostUSD

	continues := false
	if checkFollowUp {
		if followErr != nil {
			m.Log.Warn("follow-up check failed; routing on intent alone", "percept", p.ID.String(), "err", followErr)
		} else {
			maps.Copy(d.Answers, follow.Answers)
			d.CostUSD += follow.CostUSD
			continues = holds(m.FollowUp, follow.Answers)
		}
	}
	if checkLife {
		if lifeErr != nil {
			m.Log.Warn("life-area labelling failed; searching all of memory", "percept", p.ID.String(), "err", lifeErr)
		} else {
			maps.Copy(d.Answers, life.Answers)
			d.CostUSD += life.CostUSD
			labels = &lifeLabelsFound
		}
	}

	// Every message gets a reply in the agent's own words, from System 2; what
	// a person shares is remembered by System 2 too. The read decides what
	// System 2 is told about the message, and is recorded as the action.
	r := routeFor(m.Incoming, resp.Answers)
	switch {
	case continues:
		d.Action = "continue"
		return "", n.speaker + " is answering what you just asked", labels
	case r.Kind == routeAct:
		d.Action = r.Choice // share, ask_about_reader, ask_about_writer, ask_for_help, remind, small_talk, other
		return "", "it needs a reply", labels
	case r.Kind == routeAsk:
		d.Action = string(routeAsk)
		return "", "the first read was only fairly sure", labels
	default:
		d.Action = string(routeEscalate)
		return "", "the first read was unsure", labels
	}
}

// readAs reports whether System 1's intent read of d's message was choice.
func (m *Mind) readAs(d *decision.Decision, choice string) bool {
	a, ok := d.Answers[m.Incoming.Primary]
	return ok && !a.Refused && a.Choice == choice
}

// lastReply is the agent's most recent reply in the conversation, if it was
// the last thing said; "" otherwise.
func lastReply(recent []memory.Exchange) string {
	if len(recent) == 0 {
		return ""
	}
	return recent[len(recent)-1].Reply
}

// recallFacts picks the remembered facts that would help reply to p. Only the
// agent's own facts and those about the person speaking are candidates. It
// uses how the message was filed by life area (labels; nil if it was not):
//   - not about the agent's or the speaker's own life ("capital of France?"): memory is not searched;
//   - about one of them in general ("Tell me about yourself"): every candidate goes through;
//   - otherwise System 1 answers one yes/no relevance question per candidate,
//     all in one call, with facts from the message's life areas considered first.
//
// The step is recorded as a child decision of root; nil when there was nothing to do.
func (m *Mind) recallFacts(ctx context.Context, p perception.Percept, root *decision.Decision,
	labels *lifeLabels) ([]memory.Fact, *decision.Decision) {
	if m.FactRelevance.ID == "" || m.RecallCandidates <= 0 {
		return nil, nil
	}
	if labels != nil && !labels.Personal && !labels.Broad {
		return nil, nil
	}
	var areas []string
	if labels != nil {
		areas = labels.Areas
	}
	subjects := []string{memory.SubjectSelf}
	if p.Speaker != "" {
		subjects = append(subjects, p.Speaker)
	}
	candidates, err := m.Store.RecallCandidates(ctx, subjects, areas, m.RecallCandidates)
	if err != nil {
		m.Log.Warn("loading facts failed; replying without them", "percept", p.ID.String(), "err", err)
		return nil, nil
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	start := time.Now()
	d := newDecision(p, &root.ID, start)
	if labels != nil && labels.Broad {
		// A rule on System 1's labels, not a model call: everything known is relevant.
		d.Tier, d.SetID, d.Action = decision.TierReflex, "reflex.recall_all@1", "recall_all"
		slices.Reverse(candidates) // oldest first, the order they were told
		d.Outcome = &decision.Outcome{Status: decision.OutcomeOK,
			Detail: fmt.Sprintf("broad question: passed all %d facts", len(candidates)), At: time.Now().UTC()}
		d.Latency = time.Since(start)
		return candidates, d
	}
	return m.checkFacts(ctx, p, d, m.FactRelevance, "recall", candidates, true), d
}

// openThreadWindow is how far back "what's going on with you" reaches: a
// friend asks about last week's interview, not about a cold from June.
const openThreadWindow = 30 * 24 * time.Hour

// recallOpenThreads is recall for small talk: when someone just says hi, a
// friend thinks of what's going on in their life. It checks the speaker's
// facts from the last openThreadWindow with the open_threads question set.
func (m *Mind) recallOpenThreads(ctx context.Context, p perception.Percept, root *decision.Decision) ([]memory.Fact, *decision.Decision) {
	if m.OpenThreads.ID == "" || p.Speaker == "" || m.RecallCandidates <= 0 {
		return nil, nil
	}
	facts, err := m.Store.RecallCandidates(ctx, []string{p.Speaker}, nil, m.RecallCandidates)
	if err != nil {
		m.Log.Warn("loading facts failed; replying without them", "percept", p.ID.String(), "err", err)
		return nil, nil
	}
	since := p.At.Add(-openThreadWindow)
	var recent []memory.Fact
	for _, f := range facts {
		if !f.At.Before(since) {
			recent = append(recent, f)
		}
	}
	if len(recent) == 0 {
		return nil, nil
	}
	d := newDecision(p, &root.ID, time.Now())
	return m.checkFacts(ctx, p, d, m.OpenThreads, "recall_open", recent, false), d
}

// checkFacts has System 1 answer set's one-question template once per
// candidate ({fact} becomes a path to it), all in one call, and returns the
// facts that pass, oldest first. withMessage adds the message to the state.
// The step is recorded on d.
func (m *Mind) checkFacts(ctx context.Context, p perception.Percept, d *decision.Decision, set questions.Set,
	action string, candidates []memory.Fact, withMessage bool) []memory.Fact {
	start := time.Now()
	d.Tier, d.SetID, d.Action = decision.TierSystem1, set.Ref(), action

	// Message and facts name people, so "your church" and "my church" are
	// never confused with each other.
	n := m.namesFor(p)
	template := set.Questions[0]
	texts := make([]string, len(candidates))
	qs := make([]system1.Question, len(candidates))
	for i, f := range candidates {
		texts[i] = n.fact(f)
		q := template
		q.Name = f.ID.String() // answers come back keyed by fact ID
		q.Instructions = n.fill(strings.ReplaceAll(template.Instructions, "{fact}", fmt.Sprintf("`facts[%d]`", i)))
		qs[i] = q
	}
	state := map[string]any{"facts": texts}
	if withMessage {
		state["message"] = n.message(p.Text)
	}
	resp, err := m.System1.Decide(ctx, system1.Request{State: state, Questions: qs})
	if err != nil {
		m.Log.Warn("fact check failed; replying without facts", "percept", p.ID.String(), "set", set.Ref(), "err", err)
		d.Action = "error"
		d.Outcome = &decision.Outcome{Status: decision.OutcomeFailed, Detail: err.Error(), At: time.Now().UTC()}
		d.Latency = time.Since(start)
		return nil
	}
	d.Provider, d.Answers, d.CostUSD = resp.Provider, resp.Answers, resp.CostUSD

	var kept []memory.Fact
	for _, f := range candidates {
		if a := resp.Answers[f.ID.String()]; !a.Refused && a.Probability >= set.Thresholds.Act {
			kept = append(kept, f)
		}
	}
	slices.SortStableFunc(kept, func(a, b memory.Fact) int { return a.At.Compare(b.At) }) // oldest first
	d.Outcome = &decision.Outcome{Status: decision.OutcomeOK,
		Detail: fmt.Sprintf("kept %d of %d facts", len(kept), len(candidates)), At: time.Now().UTC()}
	d.Latency = time.Since(start)
	return kept
}

// reason has System 2 reply as the agent itself, recording it as child decision d.
func (m *Mind) reason(ctx context.Context, p perception.Percept, person memory.Person, parent, d *decision.Decision,
	recent []memory.Exchange, facts []memory.Fact, why string) (string, []*decision.Decision) {
	d.Tier, d.SetID, d.Action = decision.TierSystem2, m.ReplyPrompt.Ref, "reply"
	n := m.namesFor(p)
	note, refs := m.system2Note(parent, why, facts, n)

	resp, err := m.System2.Reason(ctx, system2.Request{
		Instructions: m.instructions(ctx, person, n),
		History:      turns(recent),
		Note:         m.now(p.At) + "\n\n" + note + m.promisesNote(ctx, p, person, n) + m.permissionsNote(p),
		Message:      p.Text,
		CollectFacts: true,
	})
	if err != nil {
		m.Log.Error("system 2 failed", "percept", p.ID.String(), "err", err)
		d.Action = "error"
		return "Sorry, I couldn't think that through just now.", nil
	}
	d.Provider, d.CostUSD = resp.Provider, resp.CostUSD
	m.Log.Debug("system 2 usage", "input_tokens", resp.Usage.InputTokens,
		"output_tokens", resp.Usage.OutputTokens, "reasoning_tokens", resp.Usage.ReasoningTokens)

	// System 2 understood the conversation, so it may have heard a lasting fact
	// about the speaker that System 1 could not, like "It is 2027" after a
	// passport question, or decided something about itself.
	for _, mem := range resp.Remember {
		subject := memory.SubjectSelf
		if mem.About == system2.AboutSpeaker {
			if p.Speaker == "" {
				continue // no one to file it under
			}
			subject = p.Speaker
		}
		cost, err := m.saveFact(ctx, p, subject, mem.Text, n)
		d.CostUSD += cost
		if err != nil {
			m.Log.Error("save fact from system 2", "percept", p.ID.String(), "err", err)
		}
	}
	// It may also have learned who the speaker is to it ("I'm your cousin").
	d.CostUSD += m.learnRelation(ctx, p, person, resp.WhoTheyAre)
	m.saveReminders(ctx, p, resp.Reminders)
	m.savePromises(ctx, p, person, resp.Promises)
	// What the speaker corrected: facts, the agent's permissions, its read of
	// their last message. Each is a decision of its own, after the reply.
	var more []*decision.Decision
	more = append(more, m.correctFacts(ctx, p, d, refs, resp.Corrections, n)...)
	more = append(more, m.changePermissions(ctx, p, d, resp.Permissions)...)
	more = append(more, m.labelMisread(ctx, p, d, resp.Misread)...)
	more = append(more, m.takeFeedback(ctx, p, d, resp.Feedback)...)
	more = append(more, m.takeLessons(ctx, p, d, resp.Lessons)...)
	return resp.Text, more
}

// canPromise reports whether the agent may promise person things: not a
// stranger, whose requests could be anything.
func canPromise(p perception.Percept, person memory.Person) bool {
	return p.Speaker != "" && person.Circle != memory.CircleStranger
}

// promisesNote tells System 2 what it already promised the speaker and hasn't
// sent yet, or that it can't promise them anything, or that it must check
// with its owner first (the autonomy gate).
func (m *Mind) promisesNote(ctx context.Context, p perception.Percept, person memory.Person, n names) string {
	if !canPromise(p, person) {
		return fmt.Sprintf("\n\nYou don't know %s well yet: don't promise to make them anything; leave promises empty.", n.speaker)
	}
	var gate string
	switch m.Autonomy.Check(action.Commit, p.Speaker) {
	case action.Forbid:
		return fmt.Sprintf("\n\nYou can't promise to make %s anything; leave promises empty.", n.speaker)
	case action.Ask:
		owner := orDefault(m.person(ctx, m.Autonomy.Owner).Name, "someone")
		gate = fmt.Sprintf("\n\nBefore you promise to make anything for %s, you check with %s. If %s asks and you'd like to, "+
			"don't say yes yet: say you'll check and get back to them, and still put it in promises.", n.speaker, owner, n.speaker)
	}
	open, err := m.Store.OpenIntentions(ctx, p.Speaker, memory.IntentionTask)
	if err != nil || len(open) == 0 {
		return gate
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n\nWhat you've promised %s and not sent yet (at most %d at a time). If they add to one, just say you'll work it in; "+
		"don't make it now:", n.speaker, maxOpenPromises)
	for _, i := range open {
		fmt.Fprintf(&b, "\n- by %s: %s", i.DueAt.In(m.Zone).Format("Mon 2 Jan 15:04"), i.Text)
	}
	return gate + b.String()
}

// savePromises keeps what System 2 promised to make for the speaker, as tasks
// due when it said it would send them. One in the past, with a bad time, past
// maxOpenPromises, or to a stranger is dropped; an ability it doesn't have is
// left out. The autonomy gate may forbid promising (dropped) or say to ask
// the owner first: the task then waits for their approval.
func (m *Mind) savePromises(ctx context.Context, p perception.Percept, person memory.Person, ps []system2.Promise) {
	if len(ps) == 0 {
		return
	}
	log := m.Log.With("percept", p.ID.String(), "person", p.Speaker)
	if !canPromise(p, person) {
		log.Warn("dropped promises to a stranger", "promises", len(ps))
		return
	}
	verdict := m.Autonomy.Check(action.Commit, p.Speaker)
	if verdict == action.Forbid {
		log.Warn("dropped promises: not allowed", "promises", len(ps))
		return
	}
	open, err := m.Store.OpenIntentions(ctx, p.Speaker, memory.IntentionTask)
	if err != nil {
		log.Error("loading open promises failed; dropped", "err", err)
		return
	}
	count := len(open)
	for _, pr := range ps {
		due, err := time.ParseInLocation("2006-01-02T15:04", pr.By, m.Zone)
		if err != nil || !due.After(p.At) || count >= maxOpenPromises {
			log.Warn("dropped a promise", "by", pr.By, "what", pr.What, "open", count, "err", err)
			continue
		}
		i := memory.Intention{ID: ulid.Make(), Subject: p.Speaker, Kind: memory.IntentionTask, PerceptID: p.ID, Text: pr.What,
			DueAt: due.UTC(), Status: memory.IntentionPending, CreatedAt: time.Now().UTC()}
		if ab, ok := m.Abilities.Get(pr.Ability); ok && ab.Describe() != "" && m.allowed(ab) {
			i.Ability = ab.Name()
			if pr.Query != "" {
				i.Args = map[string]string{"query": pr.Query}
			}
		}
		if verdict == action.Ask {
			i.Status = memory.IntentionAwaiting
		}
		if err := m.Store.SaveIntention(ctx, i); err != nil {
			log.Error("save promise", "err", err)
			continue
		}
		count++
		if verdict == action.Ask {
			what := fmt.Sprintf("%s, by %s", pr.What, due.Format("Mon 2 Jan 15:04"))
			if err := m.ask(ctx, memory.Approval{Class: string(action.Commit), Person: p.Speaker, What: what, Ref: i.ID}); err != nil {
				log.Error("asking the owner failed", "err", err)
			}
			continue
		}
		log.Info("promised", "due", due.Format(time.RFC3339), "what", pr.What, "ability", i.Ability)
	}
}

// saveReminders keeps the reminders System 2 agreed to set, in the agent's
// time zone. One in the past or with a bad time is dropped.
func (m *Mind) saveReminders(ctx context.Context, p perception.Percept, rs []system2.Reminder) {
	for _, r := range rs {
		due, err := time.ParseInLocation("2006-01-02T15:04", r.When, m.Zone)
		if err != nil || !due.After(p.At) || p.Speaker == "" {
			m.Log.Warn("dropped a reminder", "percept", p.ID.String(), "when", r.When, "what", r.What, "err", err)
			continue
		}
		err = m.Store.SaveIntention(ctx, memory.Intention{ID: ulid.Make(), Subject: p.Speaker, Kind: memory.IntentionRemind,
			PerceptID: p.ID, Text: r.What, DueAt: due.UTC(), Status: memory.IntentionPending, CreatedAt: time.Now().UTC()})
		if err != nil {
			m.Log.Error("save reminder", "percept", p.ID.String(), "err", err)
			continue
		}
		m.Log.Info("reminder set", "person", p.Speaker, "due", due.Format(time.RFC3339), "what", r.What)
	}
}

// now tells System 2 the time, in the agent's time zone.
func (m *Mind) now(t time.Time) string {
	return fmt.Sprintf("It is now %s (%s time).", t.In(m.Zone).Format("Monday 2 January 2006, 15:04"), m.Zone)
}

// instructions renders the reply prompt for this conversation: who the agent
// is, its life story, and who it is talking with, from memory, by account
// rather than by name.
func (m *Mind) instructions(ctx context.Context, person memory.Person, n names) string {
	story := "- " + strings.Join(m.Life.Story, "\n- ")
	personality := "- Be yourself."
	if len(m.Life.Personality) > 0 {
		personality = "- " + strings.Join(m.Life.Personality, "\n- ")
	}
	return strings.NewReplacer("{name}", n.agent, "{speaker}", n.speaker, "{life_story}", story,
		"{personality}", personality, "{who_they_are}", m.whoTheyAre(ctx, person, n),
		"{makes}", m.makesList(), "{abilities}", m.abilitiesList(), "{intents}", strings.Join(m.intents(), ", "),
		"{correct_self}", m.correctSelf(person), "{permissions_help}", m.permissionsHelp(person, n),
		"{feedback_help}", m.feedbackHelp(ctx, person)).Replace(m.ReplyPrompt.Text)
}

// turns lays recalled exchanges out as a conversation for System 2.
func turns(recent []memory.Exchange) []system2.Turn {
	var ts []system2.Turn
	for _, e := range recent {
		if e.Message != "" { // "" when the agent spoke first
			ts = append(ts, system2.Turn{Role: system2.RolePerson, Text: e.Message})
		}
		if e.Reply != "" {
			ts = append(ts, system2.Turn{Role: system2.RoleAgent, Text: e.Reply})
		}
	}
	return ts
}

// system2Note tells System 2 why it is answering, what System 1 made of the
// message, and what it remembers that could help, about itself and about the
// speaker, with names in place of pronouns.
func (m *Mind) system2Note(s1 *decision.Decision, why string, facts []memory.Fact, n names) (string, map[string]memory.Fact) {
	refs := map[string]memory.Fact{}
	var b strings.Builder
	fmt.Fprintf(&b, "You are answering %s's latest message because %s.", n.speaker, why)
	if a, ok := s1.Answers[m.Incoming.Primary]; ok && !a.Refused {
		read := fmt.Sprintf("%q", a.Choice)
		if phrase, ok := intentPhrases[a.Choice]; ok {
			read = n.speaker + " " + phrase
		}
		fmt.Fprintf(&b, " A quick first read, without the conversation, took it as %s (%s sure).", read, percent(a.Confidence))
	}
	var mine, theirs []memory.Fact
	for _, f := range facts {
		if f.Subject == memory.SubjectSelf {
			mine = append(mine, f)
		} else {
			theirs = append(theirs, f)
		}
	}
	if len(mine) == 0 && len(theirs) == 0 {
		fmt.Fprintf(&b, "\n\nBeyond your life story, nothing you remember about yourself or %s is relevant here.", n.speaker)
		return b.String(), refs
	}
	b.WriteString("\n\nWhat you remember that may help, with when you learned it (and a ref, for corrections):")
	writeFacts := func(title string, fs []memory.Fact) {
		if len(fs) == 0 {
			return
		}
		fmt.Fprintf(&b, "\n%s:", title)
		for _, f := range fs {
			ref := fmt.Sprintf("f%d", len(refs)+1)
			refs[ref] = f
			fmt.Fprintf(&b, "\n- [%s] %s: %s", ref, f.At.Format("2 Jan 2006"), n.fact(f))
		}
	}
	writeFacts("About yourself", mine)
	writeFacts("About "+n.speaker, theirs)
	if len(theirs) == 0 {
		fmt.Fprintf(&b, "\nNothing you remember about %s is relevant here.", n.speaker)
	}
	return b.String(), refs
}

// intentPhrases puts the intent read in the agent's own terms for System 2:
// the question set speaks of "writer" and "reader", and the agent is the reader.
var intentPhrases = map[string]string{
	"share":                "sharing something about their own life",
	"ask_about_reader":     "asking about you",
	"ask_reader_abilities": "asking what you can do or plan to do",
	"ask_about_writer":     "asking what you know about them",
	"ask_for_help":         "asking for help, advice or information",
	"remind":               "asking to be reminded of something",
	"small_talk":           "making small talk",
}

func percent(f float64) string { return fmt.Sprintf("%.0f%%", f*100) }
