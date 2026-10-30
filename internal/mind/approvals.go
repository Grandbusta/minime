package mind

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/Grandbusta/minime/internal/action"
	"github.com/Grandbusta/minime/internal/decision"
	"github.com/Grandbusta/minime/internal/memory"
	"github.com/Grandbusta/minime/internal/perception"
)

// Approvals: what the autonomy gate makes the agent ask its owner first. An
// approval is saved, then sent to the owner (now, or on the next tick if it
// can't be yet) with Approve and Deny buttons. The owner's tap comes back as
// a percept; unanswered, it is denied when it expires. Either way the agent
// carries on: sends the message it wrote, keeps or drops a promise, publishes
// a work or not.

// Asker asks the owner with Approve and Deny buttons (telegram.Client).
type Asker interface {
	// Ask sends text with buttons for approval id to the chat p is in, and
	// returns where it was sent.
	Ask(ctx context.Context, p perception.Percept, text, id string) (json.RawMessage, error)
	// Settle replaces a request sent by Ask with text, removing its buttons.
	Settle(ctx context.Context, where json.RawMessage, text string) error
}

const approvalsPerTick = 10

// ask saves an approval for the owner and asks them right away if it can.
func (m *Mind) ask(ctx context.Context, a memory.Approval) error {
	a.ID, a.Status, a.AskedAt = ulid.Make(), memory.ApprovalPending, time.Now().UTC()
	if err := m.Store.SaveApproval(ctx, a); err != nil {
		return err
	}
	m.Log.Info("will ask the owner", "approval", a.ID.String(), "class", a.Class, "person", a.Person)
	m.sendApprovals(ctx)
	return nil
}

// sendApprovals asks the owner what they haven't been asked yet, in their
// own conversation with the agent. Saved by anything (the profile writer
// too), an approval is sent here.
func (m *Mind) sendApprovals(ctx context.Context) {
	if m.Asker == nil || m.Autonomy.Owner == "" {
		return
	}
	as, err := m.Store.UnsentApprovals(ctx, approvalsPerTick)
	if err != nil || len(as) == 0 {
		if err != nil {
			m.Log.Warn("loading approvals to send failed", "err", err)
		}
		return
	}
	conv, ok := m.conversationWith(ctx, m.Autonomy.Owner)
	if !ok {
		m.Log.Warn("can't ask the owner yet: they haven't messaged the agent", "owner", m.Autonomy.Owner, "waiting", len(as))
		return
	}
	owner := m.person(ctx, m.Autonomy.Owner)
	p := outgoing(conv, owner, perception.KindApproval, time.Now()) // only says where; not kept
	for _, a := range as {
		where, err := m.Asker.Ask(ctx, p, m.request(ctx, a), a.ID.String())
		if err != nil {
			m.Log.Warn("asking the owner failed; trying again next tick", "approval", a.ID.String(), "err", err)
			return
		}
		if err := m.Store.MarkApprovalSent(ctx, a.ID, where, time.Now().UTC()); err != nil {
			m.Log.Error("mark approval sent", "err", err)
		}
	}
}

// request is how the agent asks its owner, in its own voice.
func (m *Mind) request(ctx context.Context, a memory.Approval) string {
	who := orDefault(m.person(ctx, a.Person).Name, fallbackName)
	var ask string
	switch action.Class(a.Class) {
	case action.ReachOut:
		ask = fmt.Sprintf("Can I message %s? It'd be this:\n\n%s", who, a.Text)
	case action.Commit:
		ask = fmt.Sprintf("%s asked me to make something: %s. Can I say yes?", who, a.What)
	case action.Publish:
		ask = "Can I put this on my public page?\n\n" + a.What
	case action.Dream:
		ask = "Can I " + a.What + "?"
	default:
		ask = "Can I " + a.What + "?"
	}
	if m.Autonomy.AskExpires > 0 {
		ask += fmt.Sprintf("\n\n(If I don't hear back by %s, I won't.)",
			a.AskedAt.Add(m.Autonomy.AskExpires).In(m.Zone).Format("Mon 2 Jan 15:04"))
	}
	return ask
}

// answer acts on the owner's tap on an approval's button. Only the owner's
// answer counts, and only the first: an approval is decided once.
func (m *Mind) answer(ctx context.Context, p perception.Percept) {
	log := m.Log.With("percept", p.ID.String())
	if p.Speaker != m.Autonomy.Owner {
		log.Warn("an approval answered by someone who isn't the owner; ignored", "speaker", p.Speaker)
		return
	}
	var ans perception.Answer
	if err := json.Unmarshal(p.Payload, &ans); err != nil {
		log.Warn("bad approval answer", "err", err)
		return
	}
	id, err := ulid.Parse(ans.Approval)
	if err != nil {
		log.Warn("bad approval id", "approval", ans.Approval)
		return
	}
	a, ok, err := m.Store.Approval(ctx, id)
	if err != nil || !ok {
		log.Warn("no such approval", "approval", ans.Approval, "err", err)
		return
	}
	status := memory.ApprovalDenied
	if ans.Approve {
		status = memory.ApprovalApproved
	}
	m.decide(ctx, a, status, p.ID)
}

// expireApprovals denies what the owner didn't answer in time.
func (m *Mind) expireApprovals(ctx context.Context, now time.Time) {
	if m.Autonomy.AskExpires <= 0 {
		return
	}
	stale, err := m.Store.StaleApprovals(ctx, now.Add(-m.Autonomy.AskExpires), approvalsPerTick)
	if err != nil {
		m.Log.Warn("loading expired approvals failed", "err", err)
		return
	}
	for _, a := range stale {
		m.decide(ctx, a, memory.ApprovalExpired, ulid.ULID{})
	}
}

// decide settles an approval (approved, denied or expired), carries on
// accordingly, updates the request the owner saw, and records the decision.
func (m *Mind) decide(ctx context.Context, a memory.Approval, status string, percept ulid.ULID) {
	now := time.Now()
	ok, err := m.Store.DecideApproval(ctx, a.ID, status, now.UTC())
	if err != nil || !ok {
		if err != nil {
			m.Log.Error("decide approval", "approval", a.ID.String(), "err", err)
		}
		return // already decided: a second tap, or it expired first
	}
	a.Status = status
	detail := m.carryOut(ctx, a, now)
	m.approvalCall(ctx, a, now)

	if m.Asker != nil && len(a.Message) > 0 {
		ending := map[string]string{memory.ApprovalApproved: "✅ You said yes.", memory.ApprovalDenied: "❌ You said no.",
			memory.ApprovalExpired: "⌛ No answer, so I didn't."}[status]
		if err := m.Asker.Settle(ctx, a.Message, m.request(ctx, a)+"\n\n"+ending); err != nil {
			m.Log.Warn("updating the request failed", "approval", a.ID.String(), "err", err)
		}
	}
	gate := decision.GateAsked
	if status != memory.ApprovalApproved {
		gate = decision.GateDenied
	}
	d := decision.Decision{ID: ulid.Make(), At: now.UTC(), PerceptID: percept, Tier: decision.TierReflex,
		SetID: "gate:" + a.Class, Action: status, Gate: gate, Latency: time.Since(now),
		Outcome: &decision.Outcome{Status: decision.OutcomeOK, Detail: detail, At: time.Now().UTC()}}
	if err := m.Store.SaveDecision(ctx, d); err != nil {
		m.Log.Error("save decision", "err", err)
	}
	m.Log.Info("approval decided", "approval", a.ID.String(), "class", a.Class, "status", status, "detail", detail)
}

// carryOut does what an approval said: approved, what was asked; otherwise
// not. It returns what it did, for the log.
func (m *Mind) carryOut(ctx context.Context, a memory.Approval, now time.Time) string {
	yes := a.Status == memory.ApprovalApproved
	why := map[string]string{memory.ApprovalDenied: "the owner said no", memory.ApprovalExpired: "no answer from the owner"}[a.Status]
	switch action.Class(a.Class) {
	case action.Dream:
		if !yes {
			return "kept its dreams as they are"
		}
		return m.changeDream(ctx, a, now)

	case action.Publish:
		public, by := yes, memory.PublicByOwner
		if a.Status == memory.ApprovalExpired {
			by = memory.PublicByGate
		}
		if _, err := m.Store.SetWorkPublic(ctx, a.Ref, public, by); err != nil {
			return "publishing failed: " + err.Error()
		}
		return map[bool]string{true: "published the work", false: "kept the work off the page"}[yes]

	case action.ReachOut:
		if !yes {
			m.finishRef(ctx, a.Ref, memory.IntentionMissed, why, now)
			return "didn't send the message"
		}
		if err := m.sendApproved(ctx, a, now); err != nil {
			m.finishRef(ctx, a.Ref, memory.IntentionMissed, "sending failed", now)
			return "sending failed: " + err.Error()
		}
		m.finishRef(ctx, a.Ref, memory.IntentionDone, "", now)
		return "sent the message"

	case action.Commit:
		person := m.person(ctx, a.Person)
		conv, ok := m.conversationWith(ctx, a.Person)
		if !yes {
			m.finishRef(ctx, a.Ref, memory.IntentionCancelled, why, now)
			if ok {
				p := outgoing(conv, person, perception.KindReminder, now)
				m.speakFirst(ctx, p, person, nil, speech{action: "follow_up", approved: true, note: fmt.Sprintf(
					"You told %s you'd check whether you could make this for them: %s. You can't this time. Tell them, "+
						"kindly and briefly, like a friend would; don't make excuses or blame anyone.", m.namesFor(p).speaker, a.What)})
			}
			return "dropped the promise"
		}
		if err := m.Store.ResumeIntention(ctx, a.Ref, now.Add(time.Hour).UTC()); err != nil {
			return "keeping the promise failed: " + err.Error()
		}
		if ok {
			due := a.What
			if is, err := m.Store.OpenIntentions(ctx, a.Person, memory.IntentionTask); err == nil {
				for _, i := range is {
					if i.ID == a.Ref {
						due = fmt.Sprintf("%s, by %s", i.Text, i.DueAt.In(m.Zone).Format("Monday 15:04"))
					}
				}
			}
			p := outgoing(conv, person, perception.KindReminder, now)
			m.speakFirst(ctx, p, person, nil, speech{action: "follow_up", approved: true, note: fmt.Sprintf(
				"You told %s you'd check whether you could make this for them. You can: %s. Tell them it's on and when "+
					"you'll send it, briefly, like a friend would.", m.namesFor(p).speaker, due)})
		}
		return "kept the promise"
	}
	return "nothing to do"
}

// sendApproved sends a message the owner approved, word for word, as the
// agent speaking first in that person's conversation.
func (m *Mind) sendApproved(ctx context.Context, a memory.Approval, now time.Time) error {
	conv, ok := m.conversationWith(ctx, a.Person)
	if !ok {
		return fmt.Errorf("no conversation with %s", a.Person)
	}
	p := outgoing(conv, m.person(ctx, a.Person), orDefault(a.Kind, perception.KindReminder), now)
	if _, err := m.Store.SavePercept(ctx, p); err != nil {
		return err
	}
	d := decision.Decision{ID: ulid.Make(), At: now.UTC(), PerceptID: p.ID, Tier: decision.TierReflex,
		SetID: "gate:" + a.Class, Action: "send", Reply: a.Text, Gate: decision.GateAsked}
	err := m.Replier.Reply(ctx, p, a.Text)
	d.Outcome = &decision.Outcome{Status: decision.OutcomeOK, Detail: "approved by the owner", At: time.Now().UTC()}
	if err != nil {
		d.Outcome = &decision.Outcome{Status: decision.OutcomeFailed, Detail: err.Error(), At: time.Now().UTC()}
	}
	d.Latency = time.Since(now)
	if serr := m.Store.SaveDecision(ctx, d); serr != nil {
		m.Log.Error("save decision", "err", serr)
	}
	return err
}

// finishRef finishes the intention an approval was about, if any.
func (m *Mind) finishRef(ctx context.Context, id ulid.ULID, status, result string, now time.Time) {
	if !id.IsZero() {
		m.finish(ctx, memory.Intention{ID: id}, status, result, now)
	}
}
