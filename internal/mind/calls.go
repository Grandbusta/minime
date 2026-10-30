package mind

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/Grandbusta/minime/internal/action"
	"github.com/Grandbusta/minime/internal/decision"
	"github.com/Grandbusta/minime/internal/memory"
	"github.com/Grandbusta/minime/internal/perception"
)

// Calls: learning its owner's judgment on its own decisions. When it decides
// something that is its owner's kind of call (what to focus on today,
// starting something new, setting or dropping a goal), it names the options,
// its pick, why, and how sure it is they'd agree. Some it asks on Telegram,
// with a button per option and one for something else; the rest the owner
// can grade in the console. A different pick gets a "what tipped it?", and
// the owner's next message is their reason. Every answer becomes one of the
// owner's decisions, approved (it's theirs), so their judgment grows; how
// often they agree, by type, is how it will earn a type of decision.
//
// After-calls (the day plan) it acts on and asks at most AsksPerDay of a
// day, the ones it's least sure of. Before-calls (goals, in the weekly
// review) wait for the answer; unanswered in time, it goes with its pick,
// and that isn't counted. Approvals it asked count too: what it wanted is
// its pick, and the owner's yes or no says whether they agree.

// Calls is how it learns its owner's judgment from its own decisions.
type Calls struct {
	On         bool
	AsksPerDay int // after-calls asked on Telegram a day; the rest wait for the console
	// ReplyWindow is how long after a different pick (or "something else")
	// the owner's next message is taken as their reason (or answer).
	ReplyWindow time.Duration
}

// Chooser asks the owner which option they'd choose (telegram.Client).
type Chooser interface {
	// Choose sends text with a button for each of n options, pick starred,
	// and one for something else, for call id, to the chat p is in, and
	// returns where it was sent.
	Choose(ctx context.Context, p perception.Percept, text, id string, n, pick int) (json.RawMessage, error)
}

const (
	callsPerTick = 10
	maxOptions   = 4
)

// planCallTypes are the calls a day plan makes.
var planCallTypes = []string{memory.CallFocus, memory.CallStart}

// planCall is a call as System 2 gives it, in a day plan.
type planCall struct {
	Type       string   `json:"type"`
	Situation  string   `json:"situation"`
	Options    []string `json:"options"`
	Pick       int      `json:"pick"`
	Why        string   `json:"why"`
	Confidence float64  `json:"confidence"`
	Stakes     string   `json:"stakes"`
	Reversible bool     `json:"reversible"`
}

// callsSchema is the shape of a day plan's calls.
var callsSchema = map[string]any{"type": "array", "items": map[string]any{
	"type": "object", "additionalProperties": false,
	"required": []string{"type", "situation", "options", "pick", "why", "confidence", "stakes", "reversible"},
	"properties": map[string]any{
		"type":       map[string]any{"type": "string", "enum": planCallTypes},
		"situation":  map[string]any{"type": "string", "description": "what you're deciding, in a sentence"},
		"options":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "two to four real options"},
		"pick":       map[string]any{"type": "integer", "description": "the option you chose, by its index from 0"},
		"why":        map[string]any{"type": "string"},
		"confidence": map[string]any{"type": "number", "description": "how sure you are your owner would choose the same, 0 to 1"},
		"stakes": map[string]any{"type": "string", "enum": memory.StakesLevels,
			"description": "what's lost if it's the wrong choice: low (an afternoon, a draft), medium (days of work, a goal, someone's expectations), high (money, reputation, a relationship, something public)"},
		"reversible": map[string]any{"type": "boolean", "description": "whether you could easily undo it if it's wrong"},
	},
}}

// newCall makes a call from what System 2 gave, or false if it isn't one.
func newCall(typ, mode, situation string, options []string, pick int, why string, confidence float64, now time.Time) (memory.Call, bool) {
	var opts []string
	for _, o := range options {
		if o = strings.TrimSpace(o); o != "" && !slices.Contains(opts, o) {
			opts = append(opts, o)
		}
	}
	situation = strings.TrimSpace(situation)
	if situation == "" || len(opts) < 2 || len(opts) > maxOptions || pick < 0 || pick >= len(opts) {
		return memory.Call{}, false
	}
	if math.IsNaN(confidence) {
		confidence = 0
	}
	return memory.Call{ID: ulid.Make(), Type: typ, Mode: mode, Situation: situation, Options: opts, Pick: pick,
		Why: strings.TrimSpace(why), Confidence: math.Max(0, math.Min(1, confidence)), Reversible: true, Status: memory.CallLogged,
		CreatedAt: now.UTC()}, true
}

// logPlanCalls keeps the calls its day plan made, to ask or be graded.
func (m *Mind) logPlanCalls(ctx context.Context, calls []planCall, now time.Time) int {
	if !m.Calls.On {
		return 0
	}
	n := 0
	for _, pc := range calls {
		if !slices.Contains(planCallTypes, pc.Type) {
			continue
		}
		c, ok := newCall(pc.Type, memory.CallAfter, pc.Situation, pc.Options, pc.Pick, pc.Why, pc.Confidence, now)
		if !ok {
			m.Log.Warn("dropped a call that isn't one", "type", pc.Type, "situation", pc.Situation, "options", len(pc.Options))
			continue
		}
		if slices.Contains(memory.StakesLevels, pc.Stakes) {
			c.Stakes = pc.Stakes
		}
		c.Reversible = pc.Reversible
		if err := m.Store.SaveCall(ctx, c); err != nil {
			m.Log.Error("save call", "err", err)
			continue
		}
		n++
	}
	return n
}

// sendCalls asks its owner what they'd choose: every before-call, and the
// after-calls it made today, up to AsksPerDay: first what puts the most at
// stake and it's least sure of.
func (m *Mind) sendCalls(ctx context.Context, now time.Time) {
	chooser, ok := m.Asker.(Chooser)
	if !m.Calls.On || !ok || m.Autonomy.Owner == "" {
		return
	}
	logged, err := m.Store.RecentCalls(ctx, memory.CallLogged, 50)
	if err != nil || len(logged) == 0 {
		if err != nil {
			m.Log.Warn("loading calls to ask failed", "err", err)
		}
		return
	}
	midnight := m.startOfDay(now)
	asked, err := m.Store.CallsAskedSince(ctx, memory.CallAfter, midnight)
	if err != nil {
		return
	}
	var ask []memory.Call
	var after []memory.Call
	for _, c := range logged {
		switch {
		case c.Mode == memory.CallBefore:
			ask = append(ask, c)
		case !c.CreatedAt.Before(midnight):
			after = append(after, c)
		}
	}
	worth := func(c memory.Call) float64 { return c.AtStake() * (1 - c.Confidence) }
	slices.SortStableFunc(after, func(a, b memory.Call) int { return cmp.Compare(worth(b), worth(a)) })
	for _, c := range after {
		if asked >= m.Calls.AsksPerDay {
			break
		}
		ask, asked = append(ask, c), asked+1
	}
	if len(ask) == 0 {
		return
	}
	conv, ok := m.conversationWith(ctx, m.Autonomy.Owner)
	if !ok {
		return
	}
	p := outgoing(conv, m.person(ctx, m.Autonomy.Owner), perception.KindCall, now) // only says where; not kept
	for i, c := range ask {
		if i >= callsPerTick {
			break
		}
		where, err := chooser.Choose(ctx, p, m.callText(c, now), c.ID.String(), len(c.Options), c.Pick)
		if err != nil {
			m.Log.Warn("asking the owner's call failed; trying again next tick", "call", c.ID.String(), "err", err)
			return
		}
		if err := m.Store.MarkCallAsked(ctx, c.ID, where, now.UTC()); err != nil {
			m.Log.Error("mark call asked", "err", err)
		}
		m.Log.Info("asked the owner's call", "call", c.ID.String(), "type", c.Type, "confidence", c.Confidence)
	}
}

// callText is how it asks its owner what they'd choose, in its own voice.
func (m *Mind) callText(c memory.Call, now time.Time) string {
	var b strings.Builder
	b.WriteString(strings.TrimSuffix(c.Situation, ".") + ".\n")
	for i, o := range c.Options {
		fmt.Fprintf(&b, "\n%d. %s", i+1, o)
		if i == c.Pick {
			b.WriteString(" ★")
		}
	}
	verb := "I went with"
	if c.Mode == memory.CallBefore {
		verb = "I'd go with"
	}
	fmt.Fprintf(&b, "\n\n%s %d", verb, c.Pick+1)
	if c.Why != "" {
		fmt.Fprintf(&b, " because %s", lowerFirst(strings.TrimSuffix(c.Why, ".")))
	}
	if c.Confidence > 0 {
		fmt.Fprintf(&b, " (%d%% sure you'd pick it too)", int(math.Round(c.Confidence*100)))
	}
	b.WriteString(".")
	if c.Stakes != "" {
		at := map[string]string{memory.StakesLow: "Not much rides on it", memory.StakesMedium: "A fair bit rides on it",
			memory.StakesHigh: "A lot rides on it"}[c.Stakes]
		if c.Reversible {
			at += ", and I can undo it."
		} else {
			at += ", and I can't undo it."
		}
		b.WriteString(" " + at)
	}
	b.WriteString(" What would you do?")
	if c.Mode == memory.CallBefore && m.Autonomy.AskExpires > 0 {
		fmt.Fprintf(&b, "\n\n(If I don't hear back by %s, I'll go with mine.)", now.Add(m.Autonomy.AskExpires).In(m.Zone).Format("Mon 2 Jan 15:04"))
	}
	return b.String()
}

func lowerFirst(s string) string {
	if s == "" || strings.HasPrefix(s, "I ") || strings.HasPrefix(s, "I'") {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// pickAnswered acts on the owner's tap on one of a call's options.
func (m *Mind) pickAnswered(ctx context.Context, p perception.Percept) {
	log := m.Log.With("percept", p.ID.String())
	if p.Speaker == "" || p.Speaker != m.Autonomy.Owner {
		log.Warn("a call answered by someone who isn't the owner; ignored", "speaker", p.Speaker)
		return
	}
	var pick perception.Pick
	if err := json.Unmarshal(p.Payload, &pick); err != nil {
		log.Warn("bad call answer", "err", err)
		return
	}
	id, err := ulid.Parse(pick.Call)
	if err != nil {
		return
	}
	c, ok, err := m.Store.Call(ctx, id)
	if err != nil || !ok || c.Status != memory.CallAsked || pick.Option >= len(c.Options) {
		log.Info("a call that can't be answered now", "call", pick.Call, "err", err)
		return
	}
	now := time.Now().UTC()
	if pick.Option < 0 { // something else: their next message says what
		if err := m.Store.WaitForOwner(ctx, c.ID, memory.CallWaitingAnswer, now); err != nil {
			m.Log.Error("wait for owner", "err", err)
			return
		}
		m.settleCall(ctx, c, "Something else. Tell me what you'd do, and why.")
		return
	}
	c.OwnerPick, c.AnsweredBy, c.AnsweredAt = pick.Option, memory.AnsweredOnTelegram, now
	ending := fmt.Sprintf("✅ You'd pick %d too.", pick.Option+1)
	if pick.Option != c.Pick {
		c.Waiting = memory.CallWaitingReason
		ending = fmt.Sprintf("You'd pick %d. What tipped it?", pick.Option+1)
	}
	if ok, err := m.Store.AnswerCall(ctx, c, true); err != nil || !ok {
		if err != nil {
			m.Log.Error("answer call", "err", err)
		}
		return
	}
	m.settleCall(ctx, c, ending)
	m.callDecided(ctx, c, p.ID, now)
}

// ownerReplied takes the owner's message as what a call was waiting for:
// their reason for a different pick, or their own answer. It returns
// whether it did, having thanked them.
func (m *Mind) ownerReplied(ctx context.Context, p perception.Percept) bool {
	if !m.Calls.On || p.Speaker == "" || p.Speaker != m.Autonomy.Owner || strings.TrimSpace(p.Text) == "" || p.Kind != "message" {
		return false
	}
	c, ok, err := m.Store.WaitingCall(ctx, time.Now().Add(-m.Calls.ReplyWindow).UTC())
	if err != nil || !ok {
		return false
	}
	now, text := time.Now().UTC(), strings.TrimSpace(p.Text)
	reply := "Thanks, that helps. I'll remember it."
	switch c.Waiting {
	case memory.CallWaitingReason:
		if err := m.Store.GiveCallReason(ctx, c.ID, text, now); err != nil {
			m.Log.Error("call reason", "err", err)
			return false
		}
	case memory.CallWaitingAnswer:
		c.OwnerPick, c.OwnerChoice, c.OwnerReason, c.AnsweredBy, c.AnsweredAt, c.Waiting = -1, text, text, memory.AnsweredOnTelegram, now, ""
		if ok, err := m.Store.AnswerCall(ctx, c, true); err != nil || !ok {
			return false
		}
		reply = "Got it, thanks. I'll keep that in mind."
		m.callDecided(ctx, c, p.ID, now)
	default:
		return false
	}
	d := decision.Decision{ID: ulid.Make(), At: now, PerceptID: p.ID, Tier: decision.TierReflex, SetID: "judgment:" + c.Type,
		Action: "take_" + c.Waiting, Reply: reply, Gate: decision.GateAuto}
	err = m.Replier.Reply(ctx, p, reply)
	d.Outcome = &decision.Outcome{Status: decision.OutcomeOK, Detail: "the owner's " + c.Waiting + " for a call", At: time.Now().UTC()}
	if err != nil {
		d.Outcome.Status, d.Outcome.Detail = decision.OutcomeFailed, err.Error()
	}
	d.Latency = time.Since(now)
	if err := m.Store.SaveDecision(ctx, d); err != nil {
		m.Log.Error("save decision", "err", err)
	}
	m.Log.Info("took the owner's "+c.Waiting+" for a call", "call", c.ID.String(), "type", c.Type)
	return true
}

// expireCalls closes calls the owner didn't answer in time: a before-call
// goes with its pick (not counted), an after-call waits for the console.
func (m *Mind) expireCalls(ctx context.Context, now time.Time) {
	if !m.Calls.On || m.Autonomy.AskExpires <= 0 {
		return
	}
	stale, err := m.Store.StaleCalls(ctx, now.Add(-m.Autonomy.AskExpires).UTC(), callsPerTick)
	if err != nil {
		m.Log.Warn("loading expired calls failed", "err", err)
		return
	}
	for _, c := range stale {
		if ok, err := m.Store.ExpireCall(ctx, c.ID); err != nil || !ok {
			continue
		}
		ending := "⌛ No answer."
		if c.Mode == memory.CallBefore {
			ending = "⌛ No answer, so I went with mine."
			detail := m.carryOutCall(ctx, c, c.Pick, now)
			m.Log.Info("a call went unanswered; went with its pick", "call", c.ID.String(), "type", c.Type, "detail", detail)
		}
		m.settleCall(ctx, c, ending)
	}
}

// callDecided carries out a before-call as the owner chose, and records
// the decision.
func (m *Mind) callDecided(ctx context.Context, c memory.Call, percept ulid.ULID, now time.Time) {
	detail := "graded"
	if c.Mode == memory.CallBefore {
		detail = m.carryOutCall(ctx, c, c.OwnerPick, now)
	}
	action := "disagreed"
	if c.OwnerPick == c.Pick {
		action = "agreed"
	}
	d := decision.Decision{ID: ulid.Make(), At: now.UTC(), PerceptID: percept, Tier: decision.TierReflex, SetID: "judgment:" + c.Type,
		Action: action, Gate: decision.GateAuto, Outcome: &decision.Outcome{Status: decision.OutcomeOK, Detail: detail, At: time.Now().UTC()}}
	if err := m.Store.SaveDecision(ctx, d); err != nil {
		m.Log.Error("save decision", "err", err)
	}
	m.Log.Info("the owner answered a call", "call", c.ID.String(), "type", c.Type, "agreed", c.OwnerPick == c.Pick, "detail", detail)
}

// carryOutCall does what a before-call's chosen option says. Option 0 is
// always doing what it wanted (setting or dropping the goal); anything else
// is not. It returns what it did, for the log.
func (m *Mind) carryOutCall(ctx context.Context, c memory.Call, option int, now time.Time) string {
	goals, err := m.Store.Goals(ctx, false)
	if err != nil {
		return "loading goals failed: " + err.Error()
	}
	var g *memory.Goal
	for i := range goals {
		if goals[i].ID == c.Ref {
			g = &goals[i]
		}
	}
	if g == nil {
		return "the goal is gone"
	}
	g.UpdatedAt = now.UTC()
	switch c.Type {
	case memory.CallNewGoal:
		if g.Status != memory.GoalProposed {
			return "the goal was already settled"
		}
		g.Status = memory.GoalDropped
		if option == 0 {
			g.Status = memory.GoalActive
		}
	case memory.CallDropGoal:
		if option != 0 || g.Status != memory.GoalActive {
			return "kept the goal"
		}
		g.Status = memory.GoalDropped
		if plans, err := m.Store.Plans(ctx, true); err == nil {
			for _, p := range plans {
				if p.GoalID == g.ID {
					if err := m.Store.EndPlan(ctx, p.ID, memory.PlanDropped, now.UTC()); err != nil {
						m.Log.Error("end plan", "err", err)
					}
				}
			}
		}
	default:
		return "nothing to do"
	}
	if err := m.Store.SaveGoal(ctx, *g); err != nil {
		return "saving the goal failed: " + err.Error()
	}
	if g.Status == memory.GoalActive {
		m.EnsurePlans(ctx, now) // a learning plan for a new skill goal
		return "set the goal: " + g.Text
	}
	return "goal " + g.Status + ": " + g.Text
}

// settleCall replaces the question the owner saw with how it ended.
func (m *Mind) settleCall(ctx context.Context, c memory.Call, ending string) {
	if m.Asker == nil || len(c.Message) == 0 {
		return
	}
	if err := m.Asker.Settle(ctx, c.Message, m.callText(c, c.AskedAt)+"\n\n"+ending); err != nil {
		m.Log.Warn("updating the call failed", "call", c.ID.String(), "err", err)
	}
}

// approvalCall counts an approval the owner answered as one of its calls:
// what it wanted was its pick, and their yes or no says whether they agree.
func (m *Mind) approvalCall(ctx context.Context, a memory.Approval, now time.Time) {
	if !m.Calls.On || (a.Status != memory.ApprovalApproved && a.Status != memory.ApprovalDenied) {
		return
	}
	situation, _, _ := strings.Cut(m.request(ctx, a), "\n\n(If I don't")
	c := memory.Call{ID: ulid.Make(), Type: a.Class, Mode: memory.CallBefore, Situation: situation, Options: []string{"yes", "no"},
		Ref: a.ID, Status: memory.CallLogged, CreatedAt: a.AskedAt}
	c.Stakes, c.Reversible = approvalStakes(action.Class(a.Class))
	if err := m.Store.SaveCall(ctx, c); err != nil {
		m.Log.Error("save call", "err", err)
		return
	}
	c.OwnerPick, c.AnsweredBy, c.AnsweredAt = 1, memory.AnsweredByApproval, now.UTC()
	if a.Status == memory.ApprovalApproved {
		c.OwnerPick = 0
	}
	if _, err := m.Store.AnswerCall(ctx, c, false); err != nil {
		m.Log.Error("answer call", "err", err)
	}
}

// goalCall asks the owner before setting or dropping a goal, in the weekly
// review. It returns false if calls are off, so the review does it itself.
func (m *Mind) goalCall(ctx context.Context, typ string, g memory.Goal, why string, confidence float64, now time.Time) bool {
	if !m.Calls.On {
		return false
	}
	var (
		situation string
		options   []string
	)
	switch typ {
	case memory.CallNewGoal:
		situation = fmt.Sprintf("In my weekly review I want to set a new goal: %s (done when: %s; due %s)", g.Text,
			orDefault(g.DoneWhen, "not said"), g.Due.In(m.Zone).Format("2 Jan"))
		options = []string{"Set it", "Not now"}
	case memory.CallDropGoal:
		situation = fmt.Sprintf("In my weekly review I want to drop my goal: %s", g.Text)
		options = []string{"Drop it", "Keep going"}
	default:
		return false
	}
	c, ok := newCall(typ, memory.CallBefore, situation, options, 0, why, confidence, now)
	if !ok {
		return false
	}
	c.Ref, c.Stakes, c.Reversible = g.ID, memory.StakesMedium, typ == memory.CallNewGoal // a set goal can be dropped; a dropped one's plan is gone
	if err := m.Store.SaveCall(ctx, c); err != nil {
		m.Log.Error("save call", "err", err)
		return false
	}
	return true
}

// disagreementsNote is where its owner chose differently this week, for the
// weekly review to draw principles from.
func (m *Mind) disagreementsNote(ctx context.Context, now time.Time) (string, map[string]string) {
	calls, err := m.Store.RecentCalls(ctx, memory.CallAnswered, 100)
	if err != nil {
		return "", nil
	}
	owner := m.ownerName(ctx)
	cases := map[string]string{} // call id: its case's key
	var b strings.Builder
	for _, c := range calls {
		if c.Agreed == nil || *c.Agreed || c.AnsweredAt.Before(now.Add(-reviewWindow)) || c.AnsweredBy == memory.AnsweredByApproval {
			continue
		}
		theirs := c.OwnerChoice
		if c.OwnerPick >= 0 && c.OwnerPick < len(c.Options) {
			theirs = c.Options[c.OwnerPick]
		}
		fmt.Fprintf(&b, "\n- [call %s] %s You picked %q; %s picked %q.", c.ID, strings.TrimSuffix(c.Situation, ".")+".", c.Options[c.Pick], owner, theirs)
		if c.OwnerReason != "" && c.OwnerReason != theirs {
			fmt.Fprintf(&b, " Why: %s", c.OwnerReason)
		}
		if !c.CaseID.IsZero() {
			cases[c.ID.String()] = c.CaseID.String()
		}
	}
	if b.Len() == 0 {
		return "", nil
	}
	return fmt.Sprintf("\n\nWhere %s chose differently from you this week (by call id):%s", owner, b.String()), cases
}

// ownerName is what it calls its owner: their name, or "your owner".
func (m *Mind) ownerName(ctx context.Context) string {
	if m.Autonomy.Owner != "" {
		if n := m.person(ctx, m.Autonomy.Owner).Name; n != "" {
			return n
		}
	}
	return "your owner"
}

const (
	// lookBackAfter is how long after a call its review looks at how it turned out.
	lookBackAfter = 2 * 24 * time.Hour
	// lookBackPerReview is the most calls one review looks back on.
	lookBackPerReview = 10
)

// lookBackTypes are the calls worth looking back on: its own decisions, not
// approvals (what happened after those is the owner's doing).
var lookBackTypes = []string{memory.CallFocus, memory.CallStart, memory.CallNewGoal, memory.CallDropGoal}

// outcomesFormat is the shape of a review's look back at its calls.
var outcomesFormat = map[string]any{"type": "array", "items": map[string]any{
	"type": "object", "additionalProperties": false, "required": []string{"call_id", "outcome", "verdict", "note"},
	"properties": map[string]any{
		"call_id": map[string]any{"type": "string"},
		"outcome": map[string]any{"type": "string", "enum": memory.Outcomes},
		"verdict": map[string]any{"type": "string", "enum": memory.Verdicts,
			"description": "good_call: a good decision that went well; lucky: went well despite the decision; unlucky: a good decision that went badly anyway; bad_call: went badly because of the decision"},
		"note": map[string]any{"type": "string", "description": "what actually happened, in a sentence, from what you made and lived"},
	},
}}

// done is what a call led it to do: its pick, or for a before-call the
// owner's choice when they gave one.
func done(c memory.Call) string {
	if c.Mode == memory.CallBefore && c.Agreed != nil {
		if c.OwnerPick >= 0 && c.OwnerPick < len(c.Options) {
			return c.Options[c.OwnerPick]
		}
		return c.OwnerChoice
	}
	return c.Options[c.Pick]
}

// lookBackNote is the calls its weekly review looks back on, by id: what it
// decided, what it did, and what its owner would have chosen.
func (m *Mind) lookBackNote(ctx context.Context, now time.Time) (string, []memory.Call) {
	if !m.Calls.On {
		return "", nil
	}
	calls, err := m.Store.CallsToLookBack(ctx, lookBackTypes, now.Add(-2*reviewWindow), now.Add(-max(3*time.Hour, m.paced(lookBackAfter))),
		lookBackPerReview)
	if err != nil || len(calls) == 0 {
		return "", nil
	}
	owner := m.ownerName(ctx)
	var b strings.Builder
	b.WriteString("\n\nCalls of yours to look back on (by call id): how did each turn out, from what you made and lived since? Judge the decision apart from the luck.")
	for _, c := range calls {
		fmt.Fprintf(&b, "\n- [call %s] %s, %s: you did %q.", c.ID, c.CreatedAt.In(m.Zone).Format("Mon 2 Jan"),
			strings.TrimSuffix(c.Situation, "."), done(c))
		if c.Agreed != nil && !*c.Agreed {
			theirs := c.OwnerChoice
			if c.OwnerPick >= 0 && c.OwnerPick < len(c.Options) {
				theirs = c.Options[c.OwnerPick]
			}
			fmt.Fprintf(&b, " %s would have chosen %q.", owner, theirs)
		}
	}
	return b.String(), calls
}

// lookBack records how the calls its review looked back on turned out. It
// returns what it recorded, for the log.
func (m *Mind) lookBack(ctx context.Context, calls []memory.Call, outcomes []callOutcome, now time.Time) []string {
	byID := map[string]memory.Call{}
	for _, c := range calls {
		byID[c.ID.String()] = c
	}
	var changes []string
	for _, o := range outcomes {
		c, ok := byID[strings.TrimSpace(o.CallID)]
		if !ok || !slices.Contains(memory.Outcomes, o.Outcome) || !slices.Contains(memory.Verdicts, o.Verdict) {
			continue
		}
		if ok, err := m.Store.SetOutcome(ctx, c.ID, o.Outcome, o.Verdict, strings.TrimSpace(o.Note), memory.OutcomeBySelf, now.UTC()); err != nil || !ok {
			continue
		}
		changes = append(changes, fmt.Sprintf("%s: %s (%s)", c.Type, strings.ReplaceAll(o.Outcome, "_", " "), strings.ReplaceAll(o.Verdict, "_", " ")))
	}
	return changes
}

// callOutcome is how a call turned out, as its review judges it.
type callOutcome struct {
	CallID  string `json:"call_id"`
	Outcome string `json:"outcome"`
	Verdict string `json:"verdict"`
	Note    string `json:"note"`
}

// approvalStakes is what an approval puts at stake, by what it asks to do: a
// message sent can't be unsent, a promise is hard to take back, a work can
// come off the page, a dream can change again.
func approvalStakes(class action.Class) (string, bool) {
	switch class {
	case action.ReachOut:
		return memory.StakesMedium, false
	case action.Commit:
		return memory.StakesMedium, false
	case action.Publish:
		return memory.StakesMedium, true
	case action.Dream:
		return memory.StakesHigh, true
	case action.Spend, action.ExternalMessage:
		return memory.StakesHigh, false
	}
	return memory.StakesLow, true
}
