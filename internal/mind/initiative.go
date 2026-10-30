package mind

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"minime/internal/action"
	"minime/internal/decision"
	"minime/internal/memory"
	"minime/internal/perception"
	"minime/internal/providers/system2"
)

// Initiative: the agent speaks first, the way a person does. A clock ticks
// into the mind's loop; on each tick it sends the reminders that are due,
// keeps the promises that are due (makes what it promised and sends it), and
// at the check-in times of day it considers checking in on the people it
// talks with, about what is going on in their lives.

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
	intentionsPerTick = 10
	// reminderGiveUp is how late a reminder may still be sent, e.g. after
	// the agent was down; later, it is marked missed.
	reminderGiveUp = 24 * time.Hour
	// promiseGiveUp is how late a promise may still be kept.
	promiseGiveUp = 24 * time.Hour
	// maxOpenPromises is how many promises it keeps open with one person.
	maxOpenPromises = 3
	// What was said around a promise, for making it: from promiseTalkBefore
	// it was made to promiseTalkAfter, at most promiseTalkTurns exchanges.
	promiseTalkBefore = 10 * time.Minute
	promiseTalkAfter  = 30 * time.Minute
	promiseTalkTurns  = 12
	// A promise is made ahead and sent when due: within promiseLead of
	// its time, but not before promiseSettle after it was made, so details
	// added in the next breath reach it, unless it is due within
	// promiseMakeAhead, the time making it may take.
	promiseLead      = 30 * time.Minute
	promiseSettle    = 5 * time.Minute
	promiseMakeAhead = time.Minute
)

func (m *Mind) tick(ctx context.Context, now time.Time) {
	m.planDay(ctx, now)
	m.expireApprovals(ctx, now)
	m.sendApprovals(ctx)
	m.expireCalls(ctx, now)
	m.sendCalls(ctx, now)
	m.doDueIntentions(ctx, now)
	m.preparePromises(ctx, now)
	m.weeklyReview(ctx, now)
	if m.checkInTime(now) {
		m.checkIn(ctx, now)
	}
	m.lastTick = now
}

// doDueIntentions does what is due: reminders for people, activities of its own.
func (m *Mind) doDueIntentions(ctx context.Context, now time.Time) {
	due, err := m.Store.DueIntentions(ctx, now, intentionsPerTick)
	if err != nil {
		m.Log.Warn("loading due intentions failed", "err", err)
		return
	}
	for _, i := range due {
		switch i.Kind {
		case memory.IntentionRemind:
			m.remind(ctx, i, now)
		case memory.IntentionActivity:
			m.doActivity(ctx, i, now)
		case memory.IntentionTask:
			m.keepPromise(ctx, i, now)
		default:
			m.Log.Warn("intention of unknown kind; cancelled", "intention", i.ID.String(), "kind", i.Kind)
			m.finish(ctx, i, memory.IntentionCancelled, "", now)
		}
	}
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

func (m *Mind) remind(ctx context.Context, r memory.Intention, now time.Time) {
	conv, ok := m.conversationWith(ctx, r.Subject)
	if !ok || now.Sub(r.DueAt) > reminderGiveUp {
		m.Log.Warn("reminder could not be sent", "intention", r.ID.String(), "person", r.Subject, "due", r.DueAt)
		m.finish(ctx, r, memory.IntentionMissed, "", now)
		return
	}
	person := m.person(ctx, r.Subject)
	p := outgoing(conv, person, perception.KindReminder, now)
	n := m.namesFor(p)
	note := fmt.Sprintf("You are messaging %s first: it's time for the reminder %s asked you for on %s: %s. "+
		"Remind them now, in your own words, briefly, like a friend would.",
		n.speaker, n.speaker, r.CreatedAt.In(m.Zone).Format("Monday 2 January at 15:04"), n.fill(r.Text))
	m.settleSpoken(ctx, r, m.speakFirst(ctx, p, person, nil, speech{action: "remind", note: note, ref: r.ID}), "", now)
}

// keepPromise sends what it promised a person when it is due, with a few
// words of its own: made ahead by preparePromises, or now if that failed.
// It isn't put off by a conversation or the day's budget: a promise is kept.
// Made but not sent (sending failed), it is sent on the next tick without
// being made again.
func (m *Mind) keepPromise(ctx context.Context, i memory.Intention, now time.Time) {
	log := m.Log.With("intention", i.ID.String(), "person", i.Subject)
	conv, ok := m.conversationWith(ctx, i.Subject)
	if !ok || now.Sub(i.DueAt) > promiseGiveUp {
		log.Warn("promise could not be kept", "due", i.DueAt)
		m.finish(ctx, i, memory.IntentionMissed, "too late", now)
		return
	}
	person := m.person(ctx, i.Subject)
	w, made, err := m.Store.IntentionWork(ctx, i.ID)
	if err != nil {
		log.Error("loading the work failed", "err", err)
		return
	}
	if !made { // not made ahead, e.g. it failed then
		if w, made = m.makePromise(ctx, i, conv, person, now); !made {
			return // missed, or tried again next tick
		}
	}
	promised := i.CreatedAt.In(m.Zone).Format("Monday 2 January at 15:04")
	p := outgoing(conv, person, perception.KindDelivery, now)
	n := m.namesFor(p)
	note := fmt.Sprintf("You are messaging %s first: on %s you promised to make this for them: %s. You made it; this is "+
		"it, and it goes right after your message, word for word:\n\n%s\n\nWrite only the one or two short sentences "+
		"you send with it, the way a friend would (\"here's that verse I promised\"). Don't write anything of your own "+
		"in its place, and don't repeat it.", n.speaker, promised, i.Text, titled(w))
	got := m.speakFirst(ctx, p, person, nil, speech{action: "deliver", note: note, attachment: titled(w), trim: firstParagraph, ref: i.ID})
	m.settleSpoken(ctx, i, got, w.Title, now) // not sent: the work is kept, so the next tick sends it
}

// preparePromises makes what it promised ahead, so it is ready to send on
// time: promises due within promiseLead, once they have settled (see
// promiseSettle). One that fails is tried again next tick, and at the latest
// when it is due.
func (m *Mind) preparePromises(ctx context.Context, now time.Time) {
	soon, err := m.Store.DueIntentions(ctx, now.Add(promiseLead), intentionsPerTick)
	if err != nil {
		m.Log.Warn("loading promises due soon failed", "err", err)
		return
	}
	for _, i := range soon {
		if i.Kind != memory.IntentionTask || !i.DueAt.After(now) {
			continue // not a promise, or due now: kept by keepPromise
		}
		if now.Before(i.CreatedAt.Add(promiseSettle)) && now.Before(i.DueAt.Add(-promiseMakeAhead)) {
			continue // just promised: more may still be said about it
		}
		if _, made, err := m.Store.IntentionWork(ctx, i.ID); err != nil || made {
			continue
		}
		conv, ok := m.conversationWith(ctx, i.Subject)
		if !ok {
			continue // missed when due
		}
		m.makePromise(ctx, i, conv, m.person(ctx, i.Subject), now)
	}
}

// makePromise makes what it promised person, with what was said about it.
func (m *Mind) makePromise(ctx context.Context, i memory.Intention, conv memory.Conversation, person memory.Person,
	now time.Time) (memory.Work, bool) {
	who := orDefault(person.Name, fallbackName)
	what := fmt.Sprintf("You promised %s, on %s, to make this for them by %s: %s. Make it now, in full; you'll send it to them then.",
		who, i.CreatedAt.In(m.Zone).Format("Monday 2 January at 15:04"), i.DueAt.In(m.Zone).Format("Monday 15:04"), i.Text)
	return m.makeWork(ctx, i, "task", what+m.promiseTalk(ctx, conv, i, who), now)
}

// promiseTalk is what was said around a promise, from a little before it to
// a while after, so details added once it was made ("funny, please") reach
// the work.
func (m *Mind) promiseTalk(ctx context.Context, conv memory.Conversation, i memory.Intention, who string) string {
	exchanges, err := m.Store.RecentExchanges(ctx, conv.Source, conv.Thread, i.CreatedAt.Add(-promiseTalkBefore), ulid.ULID{}, promiseTalkTurns)
	if err != nil {
		m.Log.Warn("loading what was said around a promise failed", "intention", i.ID.String(), "err", err)
		return ""
	}
	var b strings.Builder
	for _, e := range exchanges {
		if e.At.After(i.CreatedAt.Add(promiseTalkAfter)) {
			break
		}
		if e.Message != "" {
			fmt.Fprintf(&b, "\n%s: %s", who, e.Message)
		}
		if e.Reply != "" {
			fmt.Fprintf(&b, "\n%s: %s", m.Life.Name, e.Reply)
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return "\n\nWhat you and " + who + " said about it (follow any details they added):" + b.String()
}

// titled is a work as it is sent: its title, then its text, unless the text
// already starts with the title.
func titled(w memory.Work) string {
	text := strings.TrimSpace(w.Text)
	if first, _, _ := strings.Cut(text, "\n"); strings.EqualFold(strings.Trim(first, " *#\"'"), strings.TrimSpace(w.Title)) {
		return text
	}
	return w.Title + "\n\n" + text
}

// firstParagraph keeps the words sent with a delivery to their first
// paragraph, in case System 2 wrote more than it was asked to (like its own
// version of what is attached).
func firstParagraph(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "\n\n"); i > 0 {
		return strings.TrimSpace(s[:i])
	}
	return s
}

// settleSpoken records what came of speaking first about intention i: done
// once sent, on hold while the owner is asked, missed if it isn't allowed;
// if sending failed it stays pending, so the next tick tries again.
func (m *Mind) settleSpoken(ctx context.Context, i memory.Intention, got spoken, result string, now time.Time) {
	switch got {
	case sent:
		m.finish(ctx, i, memory.IntentionDone, result, now)
	case asked:
		if err := m.Store.AwaitIntention(ctx, i.ID); err != nil {
			m.Log.Error("await intention", "intention", i.ID.String(), "err", err)
		}
	case forbidden:
		m.finish(ctx, i, memory.IntentionMissed, "not allowed", now)
	}
}

func (m *Mind) finish(ctx context.Context, i memory.Intention, status, result string, now time.Time) {
	if err := m.Store.FinishIntention(ctx, i.ID, status, result, now.UTC()); err != nil {
		m.Log.Error("finish intention", "intention", i.ID.String(), "err", err)
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
		if got := m.speakFirst(ctx, p, person, root, speech{action: "check_in", note: b.String()}); got != sent && got != asked {
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

// speech is what the agent says first: why (note), as which action, and
// what goes with it.
type speech struct {
	action     string
	note       string              // why it is speaking, for System 2
	attachment string              // sent after what System 2 wrote, e.g. what it promised; "" if none
	trim       func(string) string // shortens what System 2 wrote; nil: kept as is
	ref        ulid.ULID           // the intention it is about; zero if none
	approved   bool                // the owner already approved it: a follow-up to an approval
}

// spoken is what came of speaking first.
type spoken int

const (
	failed    spoken = iota // not sent; worth trying again
	sent                    // sent
	asked                   // written, and waiting for the owner's approval
	forbidden               // not allowed: not even written
)

// speakFirst has System 2 write what the agent says first to person, as a
// child of root (nil: none), and sends it in the conversation p places it in,
// followed by the attachment if any. Unless already approved, it goes through
// the autonomy gate first (reach_out): forbidden, it isn't written; asked,
// the owner is shown exactly what would be sent, and it is sent if they approve.
func (m *Mind) speakFirst(ctx context.Context, p perception.Percept, person memory.Person,
	root *decision.Decision, s speech) spoken {
	log := m.Log.With("percept", p.ID.String(), "person", p.Speaker, "action", s.action)
	verdict := action.Auto
	if !s.approved {
		verdict = m.Autonomy.Check(action.ReachOut, p.Speaker)
	}
	if verdict == action.Forbid {
		log.Info("not allowed to message them first", "class", action.ReachOut)
		return forbidden
	}
	if _, err := m.Store.SavePercept(ctx, p); err != nil {
		log.Error("save percept", "err", err)
		return failed
	}
	var parent *ulid.ULID
	chain := []*decision.Decision{}
	if root != nil {
		parent = &root.ID
		chain = append(chain, root)
	}
	d := newDecision(p, parent, time.Now())
	d.Tier, d.SetID, d.Action = decision.TierSystem2, m.ReplyPrompt.Ref, s.action
	chain = append(chain, d)

	n := m.namesFor(p)
	recent := m.recall(ctx, p)
	resp, err := m.System2.Reason(ctx, system2.Request{
		Instructions: m.instructions(ctx, person, n),
		History:      turns(recent),
		Note:         m.now(p.At) + "\n\n" + s.note + nameNote(recent, n.speaker),
		CollectFacts: true, // the format the reply prompt describes; nothing new was said, so nothing is kept
	})
	got := failed
	switch {
	case err != nil:
		log.Error("system 2 failed", "err", err)
		d.Action = "error"
		d.Outcome = &decision.Outcome{Status: decision.OutcomeFailed, Detail: err.Error(), At: time.Now().UTC()}
	default:
		d.Provider, d.CostUSD, d.Reply = resp.Provider, resp.CostUSD, resp.Text
		if s.trim != nil {
			d.Reply = s.trim(d.Reply)
		}
		if s.attachment != "" {
			d.Reply += "\n\n" + s.attachment
		}
		if verdict == action.Ask {
			d.Gate = decision.GateAsked
			what := fmt.Sprintf("message %s first (%s)", n.speaker, strings.ReplaceAll(p.Kind, "_", " "))
			if err := m.ask(ctx, memory.Approval{Class: string(action.ReachOut), Person: p.Speaker, What: what,
				Text: d.Reply, Kind: p.Kind, Ref: s.ref}); err != nil {
				log.Error("asking the owner failed", "err", err)
				d.Outcome = &decision.Outcome{Status: decision.OutcomeFailed, Detail: err.Error(), At: time.Now().UTC()}
				break
			}
			d.Outcome = &decision.Outcome{Status: decision.OutcomeOK, Detail: "written; waiting for the owner's OK", At: time.Now().UTC()}
			got = asked
			break
		}
		d.Outcome = &decision.Outcome{Status: decision.OutcomeOK, At: time.Now().UTC()}
		if err := m.Replier.Reply(ctx, p, d.Reply); err != nil {
			log.Warn("sending failed", "err", err)
			d.Outcome = &decision.Outcome{Status: decision.OutcomeFailed, Detail: err.Error(), At: time.Now().UTC()}
		} else {
			got = sent
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
	switch got {
	case sent:
		log.Info("spoke first", "decision", d.ID.String())
	case asked:
		log.Info("asked the owner before messaging them first", "decision", d.ID.String())
	}
	return got
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
