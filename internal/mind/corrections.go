package mind

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/Grandbusta/minime/internal/action"
	"github.com/Grandbusta/minime/internal/decision"
	"github.com/Grandbusta/minime/internal/memory"
	"github.com/Grandbusta/minime/internal/perception"
	"github.com/Grandbusta/minime/internal/providers/system2"
)

// Corrections: what a person tells the agent it got wrong, returned by
// System 2 with its reply. Each is recorded as a decision after the reply.

// correctFacts: a person may correct what the agent knows about them; the
// owner may also correct what it knows about itself. Facts are referred to by
// their ref in the note (f1, f2, ...), so only facts it was shown can be
// corrected.
func (m *Mind) correctFacts(ctx context.Context, p perception.Percept, reply *decision.Decision,
	refs map[string]memory.Fact, cs []system2.Correction, n names) []*decision.Decision {
	var out []*decision.Decision
	for _, c := range cs {
		f, ok := refs[c.Fact]
		allowed := ok && (f.Subject == p.Speaker || (f.Subject == memory.SubjectSelf && p.Speaker == m.policy().Owner && p.Speaker != ""))
		if !allowed {
			m.Log.Warn("dropped a correction", "percept", p.ID.String(), "fact", c.Fact, "known", ok)
			continue
		}
		d := m.child(p, reply, "forget_fact")
		if _, err := m.Store.DeleteFact(ctx, f.ID); err != nil {
			m.Log.Error("delete fact", "err", err)
			continue
		}
		about := "itself"
		if f.Subject != memory.SubjectSelf {
			about = n.speaker
		}
		d.Outcome.Detail = "forgot a fact about " + about // not what it said: it was forgotten
		if !c.Forget {
			d.Action = "correct_fact"
			cost, err := m.saveFact(ctx, p, f.Subject, c.Text, n)
			d.CostUSD += cost
			if err != nil {
				m.Log.Error("save corrected fact", "err", err)
			}
			d.Outcome.Detail = "replaced a fact about " + about + " with: " + c.Text
		}
		m.Log.Info("corrected memory", "percept", p.ID.String(), "action", d.Action, "subject", f.Subject)
		out = append(out, d)
	}
	return out
}

func (m *Mind) changePermissions(ctx context.Context, p perception.Percept, reply *decision.Decision,
	ps []system2.Permission) []*decision.Decision {
	if len(ps) == 0 {
		return nil
	}
	if p.Speaker == "" || p.Speaker != m.policy().Owner {
		m.Log.Warn("dropped permission changes from someone who isn't the owner", "speaker", p.Speaker)
		return nil
	}
	var out []*decision.Decision
	for _, change := range ps {
		class, verdict := action.Class(change.Class), action.Verdict(change.Verdict)
		if !action.ByChat(class) || action.Validate(map[action.Class]action.Verdict{class: verdict}) != nil {
			m.Log.Warn("dropped a permission change", "class", class, "verdict", verdict)
			continue
		}
		before := m.policy().Check(class, "")
		if before == verdict {
			continue
		}
		if err := m.Store.SaveAutonomyRule(ctx, string(class), string(verdict), p.Speaker, p.ID, time.Now().UTC()); err != nil {
			m.Log.Error("save autonomy rule", "err", err)
			continue
		}
		m.setPolicy(m.policy().With(map[action.Class]action.Verdict{class: verdict}))
		d := m.child(p, reply, "permission")
		d.Outcome.Detail = fmt.Sprintf("%s: %s → %s", class, before, verdict)
		m.Log.Info("permission changed by the owner", "class", class, "from", before, "to", verdict)
		out = append(out, d)
	}
	return out
}

func (m *Mind) labelMisread(ctx context.Context, p perception.Percept, reply *decision.Decision, choice string) []*decision.Decision {
	if choice == "" || p.Speaker != m.policy().Owner || p.Speaker == "" || m.Incoming.ID == "" || !slices.Contains(m.intents(), choice) {
		return nil
	}
	read, ok, err := m.Store.LastRead(ctx, m.Incoming.ID, p.Source, p.Thread, p.At)
	if err != nil || !ok {
		m.Log.Warn("no read of the previous message to label", "err", err)
		return nil
	}
	if err := m.Store.SaveLabel(ctx, memory.Label{DecisionID: read, Question: m.Incoming.Primary, Value: choice,
		Source: memory.LabelFromChat, At: time.Now().UTC()}); err != nil {
		m.Log.Error("save label", "err", err)
		return nil
	}
	d := m.child(p, reply, "label")
	d.Outcome.Detail = fmt.Sprintf("%s of %s should have been %s", m.Incoming.Primary, read, choice)
	m.Log.Info("labelled a misread", "decision", read.String(), "value", choice)
	return []*decision.Decision{d}
}

func (m *Mind) intents() []string {
	var out []string
	for _, q := range m.Incoming.Questions {
		if q.Name == m.Incoming.Primary {
			for _, c := range q.Choices {
				out = append(out, c.Value)
			}
		}
	}
	return out
}

func (m *Mind) child(p perception.Percept, reply *decision.Decision, act string) *decision.Decision {
	now := time.Now().UTC()
	return &decision.Decision{ID: ulid.Make(), ParentID: &reply.ID, At: now, PerceptID: p.ID, Tier: decision.TierReflex,
		SetID: "correction", Action: act, Gate: decision.GateAuto, Outcome: &decision.Outcome{Status: decision.OutcomeOK, At: now}}
}

func (m *Mind) permissionsNote(p perception.Percept) string {
	if p.Speaker == "" || p.Speaker != m.policy().Owner || m.policy().Classes == nil {
		return ""
	}
	var lines []string
	for c := range m.policy().Classes {
		if action.ByChat(c) {
			lines = append(lines, fmt.Sprintf("%s: %s", c, m.policy().Check(c, "")))
		}
	}
	sort.Strings(lines)
	return "\n\nWhat you may do on your own now, with anyone but the person you're talking to (auto: on your own; ask: " +
		"you ask them first; forbid: never): " + strings.Join(lines, ", ") + "."
}

// loadPermissions applies the owner's changes made by chat over the config's
// policy, so they last across restarts.
func (m *Mind) loadPermissions(ctx context.Context) {
	if m.policy().Classes == nil {
		return
	}
	rules, err := m.Store.AutonomyRules(ctx)
	if err != nil {
		m.Log.Warn("loading the owner's permission changes failed; using the config's", "err", err)
		return
	}
	if len(rules) > 0 {
		m.setPolicy(m.policy().WithChatRules(rules))
		m.Log.Info("the owner's permission changes apply", "changes", len(rules))
	}
}

func (m *Mind) correctSelf(person memory.Person) string {
	if person.Key == "" || person.Key != m.policy().Owner {
		return ""
	}
	return ", or about yourself"
}

func (m *Mind) permissionsHelp(person memory.Person, n names) string {
	if person.Key == "" || person.Key != m.policy().Owner || m.policy().Classes == nil {
		return ""
	}
	return "\nWhat you may do on your own:\n" +
		"- " + n.speaker + " decides what you may do without asking them first: reach_out (messaging someone first: reminders, check-ins, " +
		"sending what you promised), commit (promising to make something for someone), publish (putting a work on your public page), " +
		"read_web (reading books and articles). Each is auto (on your own), ask (you ask " + n.speaker + " first) or forbid; read_web " +
		"can't be ask. The note says what they are now.\n" +
		"- If " + n.speaker + " clearly tells you to change one (\"you can send reminders without asking\", \"ask me before you publish anything\"), " +
		"put it in `permissions` and confirm it plainly. Money and posting outside this chat can't be changed by chat: say they're set in your config.\n"
}
