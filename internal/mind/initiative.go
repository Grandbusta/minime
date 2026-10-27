package mind

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"minime/internal/decision"
	"minime/internal/memory"
	"minime/internal/perception"
	"minime/internal/providers/system2"
)

// Initiative: the agent speaks first, the way a person does. A clock ticks
// into the mind's loop; on each tick it sends the reminders that are due,
// and at the check-in times of day it considers checking in on the people
// it talks with, about what is going on in their lives.

// CheckIns is when the agent considers checking in on people.
type CheckIns struct {
	// Times of day, from midnight in the agent's time zone, e.g. 11:00 and
	// 18:00. Empty: it never checks in unprompted.
	Times []time.Duration
	// Quiet is how long a conversation must have been quiet first, so it
	// never interrupts.
	Quiet time.Duration
}

const (
	remindersPerTick = 10
	// reminderGiveUp is how late a reminder may still be sent, e.g. after
	// the agent was down; later, it is marked failed.
	reminderGiveUp = 24 * time.Hour
)

func (m *Mind) tick(ctx context.Context, now time.Time) {
	m.sendDueReminders(ctx, now)
	if m.checkInTime(now) {
		m.checkIn(ctx, now)
	}
	m.lastTick = now
}

// checkInTime reports whether a check-in time passed since the last tick.
// The first tick after starting never counts, so a restart sends nothing.
func (m *Mind) checkInTime(now time.Time) bool {
	if m.lastTick.IsZero() {
		return false
	}
	local := now.In(m.Zone)
	midnight := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, m.Zone)
	for _, at := range m.CheckIns.Times {
		if t := midnight.Add(at); t.After(m.lastTick) && !t.After(now) {
			return true
		}
	}
	return false
}

func (m *Mind) sendDueReminders(ctx context.Context, now time.Time) {
	due, err := m.Store.DueReminders(ctx, now, remindersPerTick)
	if err != nil {
		m.Log.Warn("loading due reminders failed", "err", err)
		return
	}
	for _, r := range due {
		conv, ok := m.conversationWith(ctx, r.Person)
		if !ok || now.Sub(r.DueAt) > reminderGiveUp {
			m.Log.Warn("reminder could not be sent", "reminder", r.ID.String(), "person", r.Person, "due", r.DueAt)
			m.setReminderStatus(ctx, r, memory.ReminderFailed)
			continue
		}
		person := m.person(ctx, r.Person)
		p := outgoing(conv, person, perception.KindReminder, now)
		n := m.namesFor(p)
		note := fmt.Sprintf("You are messaging %s first: it's time for the reminder %s asked you for on %s: %s. "+
			"Remind them now, in your own words, briefly, like a friend would.",
			n.speaker, n.speaker, r.CreatedAt.In(m.Zone).Format("Monday 2 January at 15:04"), n.fill(r.Text))
		if m.speakFirst(ctx, p, person, nil, "remind", note) {
			m.setReminderStatus(ctx, r, memory.ReminderSent)
		}
		// Not sent: still pending, so the next tick tries again.
	}
}

func (m *Mind) setReminderStatus(ctx context.Context, r memory.Reminder, status string) {
	if err := m.Store.SetReminderStatus(ctx, r.ID, status); err != nil {
		m.Log.Error("set reminder status", "reminder", r.ID.String(), "err", err)
	}
}

// checkIn considers each person the agent talks with: someone it knows, in
// a quiet conversation, not checked in on today, with something recent going
// on in their life that it has not asked about yet. System 1 picks what is
// worth asking about (open_threads); System 2 writes the message.
func (m *Mind) checkIn(ctx context.Context, now time.Time) {
	if m.OpenThreads.ID == "" || m.RecallCandidates <= 0 {
		return
	}
	convs, err := m.Store.Conversations(ctx)
	if err != nil {
		m.Log.Warn("loading conversations failed; no check-ins", "err", err)
		return
	}
	for _, conv := range convs {
		person, known, err := m.Store.Person(ctx, conv.Person)
		if err != nil || !known || person.Circle == memory.CircleStranger || now.Sub(conv.LastActivity) < m.CheckIns.Quiet {
			continue
		}
		last, ok, err := m.Store.LastSpokeFirst(ctx, conv.Person, perception.KindCheckIn)
		if err != nil || (ok && sameDay(last, now, m.Zone)) {
			continue
		}
		facts, err := m.Store.UnaskedFacts(ctx, conv.Person, now.Add(-openThreadWindow), m.RecallCandidates)
		if err != nil || len(facts) == 0 {
			continue
		}

		p := outgoing(conv, person, perception.KindCheckIn, now)
		root := newDecision(p, nil, now)
		kept := m.checkFacts(ctx, p, root, m.OpenThreads, "check_in", facts, false)
		if len(kept) == 0 {
			// Nothing worth asking about: recorded, but no message, so no percept.
			root.PerceptID = ulid.ULID{}
			if root.Action != "error" {
				root.Action = "no_check_in"
			}
			if err := m.Store.SaveDecision(ctx, *root); err != nil {
				m.Log.Error("save decision", "err", err)
			}
			continue
		}

		n := m.namesFor(p)
		var b strings.Builder
		fmt.Fprintf(&b, "You are messaging %s first, unprompted, to check in; you haven't talked for a while. "+
			"Things %s told you that may still be going on, with when they told you:", n.speaker, n.speaker)
		for _, f := range kept {
			fmt.Fprintf(&b, "\n- %s: %s", f.At.Format("2 Jan 2006"), n.fact(f))
		}
		b.WriteString("\nAsk how one of them is going, the way a friend texts out of the blue: short and warm, one question at most.")
		if !m.speakFirst(ctx, p, person, root, "check_in", b.String()) {
			continue
		}
		ids := make([]ulid.ULID, len(kept))
		for i, f := range kept {
			ids[i] = f.ID
		}
		if err := m.Store.MarkFactsAsked(ctx, ids, now.UTC()); err != nil {
			m.Log.Error("mark facts asked", "err", err)
		}
	}
}

// speakFirst has System 2 write what the agent says first to person, as a
// child of root (nil: none), and sends it in the conversation p places it in.
// The note says why it is speaking. It reports whether the message was sent.
func (m *Mind) speakFirst(ctx context.Context, p perception.Percept, person memory.Person,
	root *decision.Decision, action, note string) bool {
	log := m.Log.With("percept", p.ID.String(), "person", p.Speaker, "action", action)
	if _, err := m.Store.SavePercept(ctx, p); err != nil {
		log.Error("save percept", "err", err)
		return false
	}
	var parent *ulid.ULID
	chain := []*decision.Decision{}
	if root != nil {
		parent = &root.ID
		chain = append(chain, root)
	}
	d := newDecision(p, parent, time.Now())
	d.Tier, d.SetID, d.Action = decision.TierSystem2, m.ReplyPrompt.Ref, action
	chain = append(chain, d)

	n := m.namesFor(p)
	resp, err := m.System2.Reason(ctx, system2.Request{
		Instructions: m.instructions(ctx, person, n),
		History:      turns(m.recall(ctx, p)),
		Note:         m.now(p.At) + "\n\n" + note,
		CollectFacts: true, // the format the reply prompt describes; nothing new was said, so nothing is kept
	})
	sent := false
	switch {
	case err != nil:
		log.Error("system 2 failed", "err", err)
		d.Action = "error"
		d.Outcome = &decision.Outcome{Status: decision.OutcomeFailed, Detail: err.Error(), At: time.Now().UTC()}
	default:
		d.Provider, d.CostUSD, d.Reply = resp.Provider, resp.CostUSD, resp.Text
		d.Outcome = &decision.Outcome{Status: decision.OutcomeOK, At: time.Now().UTC()}
		if err := m.Replier.Reply(ctx, p, resp.Text); err != nil {
			log.Warn("sending failed", "err", err)
			d.Outcome = &decision.Outcome{Status: decision.OutcomeFailed, Detail: err.Error(), At: time.Now().UTC()}
		} else {
			sent = true
		}
	}
	d.Latency = time.Since(d.At)
	for _, dd := range chain {
		if dd.Outcome == nil {
			dd.Outcome = &decision.Outcome{Status: decision.OutcomeOK, At: time.Now().UTC()}
		}
		if err := m.Store.SaveDecision(ctx, *dd); err != nil {
			log.Error("save decision", "err", err)
		}
	}
	if sent {
		log.Info("spoke first", "decision", d.ID.String())
	}
	return sent
}

// outgoing is the percept that places what the agent says first in a
// conversation: the person's thread, addressed like their last message.
func outgoing(conv memory.Conversation, person memory.Person, kind string, now time.Time) perception.Percept {
	id := ulid.Make()
	return perception.Percept{ID: id, Source: conv.Source, ExternalID: kind + ":" + id.String(), Thread: conv.Thread,
		Speaker: conv.Person, SpeakerName: person.Name, Kind: kind, Payload: conv.Payload, At: now.UTC()}
}

func (m *Mind) conversationWith(ctx context.Context, key string) (memory.Conversation, bool) {
	convs, err := m.Store.Conversations(ctx)
	if err != nil {
		m.Log.Warn("loading conversations failed", "err", err)
		return memory.Conversation{}, false
	}
	for _, c := range convs {
		if c.Person == key {
			return c, true
		}
	}
	return memory.Conversation{}, false
}

// person is who key is, from memory; just the key if unknown.
func (m *Mind) person(ctx context.Context, key string) memory.Person {
	p, _, err := m.Store.Person(ctx, key)
	if err != nil {
		m.Log.Warn("loading person failed", "person", key, "err", err)
	}
	p.Key = key
	return p
}

func sameDay(a, b time.Time, zone *time.Location) bool {
	ay, am, ad := a.In(zone).Date()
	by, bm, bd := b.In(zone).Date()
	return ay == by && am == bm && ad == bd
}
