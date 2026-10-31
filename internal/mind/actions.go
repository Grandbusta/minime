package mind

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/Grandbusta/minime/internal/action"
	"github.com/Grandbusta/minime/internal/decision"
	"github.com/Grandbusta/minime/internal/memory"
	"github.com/Grandbusta/minime/internal/perception"
	"github.com/Grandbusta/minime/internal/providers/system2"
)

// Actions: tools that change things. It proposes one (in a chat with its
// owner, or in its own day); the gate allows it, or the owner approves it;
// then the life loop runs it on the next tick, never while a reply waits,
// and tells the owner what came of it.

// Doer is a tool that changes things (tools.Action).
type Doer interface {
	Name() string
	Describe() string
	Class() action.Class
	// Destructive tools always ask, even when their class is auto.
	Destructive() bool
	Check(args string) error
	Do(ctx context.Context, args string, at time.Time) (string, error)
}

const actionsPerTick = 5

// doers is the tools that change things it may propose now: those whose
// class isn't forbidden.
func (m *Mind) doers() map[string]Doer {
	out := map[string]Doer{}
	for _, d := range m.Doers {
		if m.policy().Check(d.Class(), "") != action.Forbid {
			out[d.Name()] = d
		}
	}
	return out
}

// actionsFor is the help on tools that change things, for a reply to person:
// only its owner can have it use them.
func (m *Mind) actionsFor(person memory.Person, n names) string {
	if person.Key == "" || person.Key != m.policy().Owner {
		return ""
	}
	return m.actionsHelp(n.speaker)
}

func (m *Mind) actionsHelp(owner string) string {
	ds := m.doers()
	if len(ds) == 0 {
		return `- You have no tools that change things: leave act empty.`
	}
	var b strings.Builder
	fmt.Fprintf(&b, "- Tools that change things, which you can use for %s:", owner)
	for _, d := range m.Doers {
		if _, ok := ds[d.Name()]; ok {
			fmt.Fprintf(&b, "\n  - %s: %s", d.Name(), d.Describe())
		}
	}
	fmt.Fprintf(&b, "\n- Use one only when %[1]s asks for it, or it plainly serves what you're doing for them: put it in `act`, with "+
		"its name as tool, its arguments as a JSON object in args, and why in a few words. It runs a minute or so later, once "+
		"it's allowed (most need %[1]s's OK on Telegram first), so never say it's done: say you'll do it, and that you'll let "+
		"them know when it is.", owner)
	return b.String()
}

// propose saves what it proposes to do: checked against the tool, under the
// day's cap, and through the gate. A destructive tool always asks, and so
// does one proposed from what it read rather than its owner's own words
// (trusted false): text from outside can't make it act unasked.
func (m *Mind) propose(ctx context.Context, p perception.Percept, acts []system2.Act, now time.Time, trusted bool) {
	if len(acts) == 0 {
		return
	}
	ds := m.doers()
	l := now.In(m.Zone)
	done, err := m.Store.ActionsSince(ctx, time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, m.Zone))
	if err != nil {
		m.Log.Error("counting today's actions failed; none proposed", "err", err)
		return
	}
	for _, act := range acts {
		log := m.Log.With("percept", p.ID.String(), "tool", act.Tool)
		d, ok := ds[act.Tool]
		switch {
		case !ok:
			log.Warn("proposed a tool it can't use; dropped")
			continue
		case done >= m.ActionsPerDay:
			log.Warn("over today's actions; dropped", "today", done)
			continue
		}
		if err := d.Check(act.Args); err != nil {
			log.Warn("proposed arguments the tool doesn't take; dropped", "args", act.Args, "err", err)
			continue
		}
		verdict := m.policy().Check(d.Class(), "")
		if verdict == action.Auto && (d.Destructive() || !trusted) {
			verdict = action.Ask
		}
		a := memory.Action{ID: ulid.Make(), Tool: d.Name(), Class: string(d.Class()), Args: act.Args, Why: act.Why,
			Status: memory.ActionApproved, PerceptID: p.ID, ProposedAt: now.UTC()}
		if verdict == action.Ask {
			a.Status = memory.ActionProposed
		}
		if err := m.Store.SaveAction(ctx, a); err != nil {
			log.Error("save action", "err", err)
			continue
		}
		done++
		if verdict == action.Ask {
			if err := m.ask(ctx, memory.Approval{Class: a.Class, What: a.Tool + ": " + a.Why, Text: actionRequest(a),
				Kind: memory.KindAction, Ref: a.ID}); err != nil {
				log.Error("asking the owner failed", "err", err)
			}
			log.Info("proposed an action; asked the owner", "action", a.ID.String())
			continue
		}
		log.Info("proposed an action; it runs on the next tick", "action", a.ID.String())
	}
}

// actionRequest is what the owner is shown: exactly what would be done.
func actionRequest(a memory.Action) string {
	return fmt.Sprintf("Can I use %s?\n\n%s\n\nWhy: %s", a.Tool, cutRunes(a.Args, 1500), orDefault(a.Why, "you asked"))
}

// approveAction settles an action the owner was asked about: approved, it
// runs on the next tick; otherwise it never does.
func (m *Mind) approveAction(ctx context.Context, a memory.Approval, now time.Time) string {
	status, result := memory.ActionApproved, ""
	if a.Status != memory.ApprovalApproved {
		status, result = memory.ActionDenied, map[string]string{memory.ApprovalDenied: "the owner said no"}[a.Status]
		result = orDefault(result, "no answer from the owner")
	}
	moved, err := m.Store.SetAction(ctx, a.Ref, []string{memory.ActionProposed}, status, result, now.UTC())
	switch {
	case err != nil:
		return "settling the action failed: " + err.Error()
	case !moved:
		return "the action was already settled"
	case status == memory.ActionApproved:
		return "it runs on the next tick"
	}
	return "it won't run"
}

// runActions runs what is approved, oldest first, and tells the owner what
// came of each. A tool whose class has since been forbidden doesn't run.
func (m *Mind) runActions(ctx context.Context, now time.Time) {
	as, err := m.Store.Actions(ctx, []string{memory.ActionApproved}, actionsPerTick)
	if err != nil || len(as) == 0 {
		if err != nil {
			m.Log.Warn("loading actions to run failed", "err", err)
		}
		return
	}
	ds := m.doers()
	for _, a := range as {
		log := m.Log.With("action", a.ID.String(), "tool", a.Tool)
		status, result := memory.ActionDone, ""
		start := time.Now()
		if d, ok := ds[a.Tool]; !ok {
			status, result = memory.ActionDenied, "not allowed any more"
		} else if text, err := d.Do(ctx, a.Args, now); err != nil {
			status, result = memory.ActionFailed, err.Error()
		} else {
			result = text
		}
		if moved, err := m.Store.SetAction(ctx, a.ID, []string{memory.ActionApproved}, status, cutRunes(result, 4000), now.UTC()); err != nil || !moved {
			log.Error("settling the action failed", "err", err)
			continue
		}
		log.Info("ran an action", "status", status, "took", time.Since(start).Round(time.Millisecond).String())
		m.tellDone(ctx, a, status, result, start)
	}
}

// tellDone tells the owner what came of an action.
func (m *Mind) tellDone(ctx context.Context, a memory.Action, status, result string, start time.Time) {
	text := map[string]string{
		memory.ActionDone:   fmt.Sprintf("✅ Done: %s (%s)", a.Tool, a.Why),
		memory.ActionFailed: fmt.Sprintf("⚠️ %s didn't work (%s)", a.Tool, a.Why),
		memory.ActionDenied: fmt.Sprintf("I didn't use %s: it isn't allowed any more.", a.Tool),
	}[status]
	if r := strings.TrimSpace(result); r != "" && status != memory.ActionDenied {
		text += "\n\n" + cutRunes(r, 600)
	}
	m.tellOwner(ctx, perception.KindAction, "tool:"+a.Tool, text, status == memory.ActionDone, start)
}

// tellOwner messages the owner text as is, kept as an exchange so it can
// talk about it after.
func (m *Mind) tellOwner(ctx context.Context, kind, setID, text string, ok bool, start time.Time) {
	owner := m.policy().Owner
	conv, found := m.conversationWith(ctx, owner)
	if !found {
		return
	}
	p := outgoing(conv, m.person(ctx, owner), kind, time.Now())
	if _, err := m.Store.SavePercept(ctx, p); err != nil {
		m.Log.Error("save percept", "err", err)
		return
	}
	d := decision.Decision{ID: ulid.Make(), At: start.UTC(), PerceptID: p.ID, Tier: decision.TierReflex, SetID: setID,
		Action: "tell", Reply: text, Gate: decision.GateAuto, Latency: time.Since(start),
		Outcome: &decision.Outcome{Status: decision.OutcomeOK, At: time.Now().UTC()}}
	if !ok {
		d.Outcome.Status = decision.OutcomeFailed
	}
	if err := m.Replier.Reply(ctx, p, text); err != nil {
		m.Log.Warn("telling the owner failed", "err", err)
	}
	if err := m.Store.SaveDecision(ctx, d); err != nil {
		m.Log.Error("save decision", "err", err)
	}
}
