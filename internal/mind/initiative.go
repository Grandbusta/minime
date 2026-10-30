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

// Initiative: the agent speaks first, the way a person does: reminders,
// promises and checking in on people.

type CheckIns struct {
	// Times of day, from midnight in its time zone. Empty: it never checks in unprompted.
	Times []time.Duration
	Quiet time.Duration
}

const (
	intentionsPerTick = 10
	// reminderGiveUp: a reminder later than this, e.g. after downtime, is missed.
	reminderGiveUp  = 24 * time.Hour
	promiseGiveUp   = 24 * time.Hour
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

// tick does first what someone is waiting for, then its own day, the slow part.
func (m *Mind) tick(ctx context.Context, now time.Time) {
	m.expireApprovals(ctx, now)
	m.sendApprovals(ctx)
	m.expireCalls(ctx, now)
	m.sendCalls(ctx, now)
	m.doDueIntentions(ctx, now, false)
	if m.checkInTime(now) {
		m.checkIn(ctx, now)
	}
	m.planDay(ctx, now)
	m.doDueIntentions(ctx, now, true)
	m.preparePromises(ctx, now)
	m.weeklyReview(ctx, now)
	if m.toPlan.Swap(false) { // a goal taken on in a chat: its learning plan, now the reply is sent
		m.EnsurePlans(ctx, now)
	}
	m.lastTick = now
}

func (m *Mind) doDueIntentions(ctx context.Context, now time.Time, ownDay bool) {
	due, err := m.Store.DueIntentions(ctx, now, intentionsPerTick)
	if err != nil {
		m.Log.Warn("loading due intentions failed", "err", err)
		return
	}
	for _, i := range due {
		if (i.Kind == memory.IntentionActivity) != ownDay {
			continue
		}
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

// keepPromise isn't put off by a conversation or the day's budget: a promise
// is kept.
func (m *Mind) keepPromise(ctx context.Context, i memory.Intention, now time.Time) {
	log := m.Log.With("intention", i.ID.String(), "person", i.Subject)
	conv, ok := m.conversationWith(ctx, i.Subject)
	if !ok || now.Sub(i.DueAt) > promiseGiveUp {
		log.Warn("promise could not be kept", "due", i.DueAt)
		m.finish(ctx, i, memory.IntentionMissed, "too late", now)
		return
	}
	person := m.person(ctx, i.Subject)
	w, made, err := m.promisedWork(ctx, i)
	if err != nil {
		log.Error("loading the work failed", "err", err)
		return
	}
	if !made {
		if w, made = m.makePromise(ctx, i, conv, person, now); !made {
			return
		}
	}
	promised := i.CreatedAt.In(m.Zone).Format("Monday 2 January at 15:04")
	p := outgoing(conv, person, perception.KindDelivery, now)
	n := m.namesFor(p)
	s := speech{action: "deliver", attachment: titled(w), trim: firstParagraph, ref: i.ID}
	goes := "it goes right after your message, word for word"
	f, err := m.fileFor(i, w)
	if err != nil {
		log.Error("making the file failed; it goes as a message", "err", err)
	}
	if f != nil {
		s.attachment, s.file = "", f
		goes = "it goes with your message as a PDF, " + f.name
	}
	s.note = fmt.Sprintf("You are messaging %s first: on %s you promised to make this for them: %s. You made it; this is "+
		"it, and %s:\n\n%s\n\nWrite only the one or two short sentences "+
		"you send with it, the way a friend would (\"here's that verse I promised\"). Don't write anything of your own "+
		"in its place, and don't repeat it.", n.speaker, promised, i.Text, goes, titled(w))
	got := m.speakFirst(ctx, p, person, nil, s)
	m.settleSpoken(ctx, i, got, w.Title, now) // not sent: the work is kept, so the next tick sends it
}

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
			continue
		}
		if _, made, err := m.promisedWork(ctx, i); err != nil || made {
			continue
		}
		conv, ok := m.conversationWith(ctx, i.Subject)
		if !ok {
			continue
		}
		m.makePromise(ctx, i, conv, m.person(ctx, i.Subject), now)
	}
}

// promisedWork is what promise i sends: the work it already made that i
// names, or else what it made for i, if anything yet.
func (m *Mind) promisedWork(ctx context.Context, i memory.Intention) (memory.Work, bool, error) {
	if id, err := ulid.Parse(i.Args["work"]); err == nil {
		if w, ok, err := m.Store.Work(ctx, id); err != nil || ok {
			return w, ok, err
		}
	}
	return m.Store.IntentionWork(ctx, i.ID)
}

func (m *Mind) makePromise(ctx context.Context, i memory.Intention, conv memory.Conversation, person memory.Person,
	now time.Time) (memory.Work, bool) {
	who := orDefault(person.Name, fallbackName)
	what := fmt.Sprintf("You promised %s, on %s, to make this for them by %s: %s. Make it now, in full; you'll send it to them then.",
		who, i.CreatedAt.In(m.Zone).Format("Monday 2 January at 15:04"), i.DueAt.In(m.Zone).Format("Monday 15:04"), i.Text)
	talk := m.promiseTalk(ctx, conv, i, who)
	works, _, _ := m.worksNote(ctx, i.Subject, i.Text+talk)
	if works != "" {
		works += "\n\nIf what you promised is something you already made (above), send that: keep it as it is, only " +
			"laid out for how it goes, and don't write a new one. Either way, add nothing about yourself or your work that isn't true."
	}
	return m.makeWork(ctx, i, "task", what+fileNote(i)+works+talk, now)
}

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

func titled(w memory.Work) string {
	text := strings.TrimSpace(w.Text)
	if first, _, _ := strings.Cut(text, "\n"); strings.EqualFold(strings.Trim(first, " *#\"'"), strings.TrimSpace(w.Title)) {
		return text
	}
	return w.Title + "\n\n" + text
}

// firstParagraph guards against System 2 writing its own version of what is
// attached.
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

type speech struct {
	action     string
	note       string
	attachment string              // sent after what System 2 wrote, e.g. what it promised; "" if none
	file       *file               // sent with what System 2 wrote, as its caption; nil if none
	trim       func(string) string // shortens what System 2 wrote; nil: kept as is
	ref        ulid.ULID
	approved   bool // the owner already approved it: a follow-up to an approval
}

type spoken int

const (
	failed spoken = iota // not sent; worth trying again
	sent
	asked
	forbidden // not allowed: not even written
)

// speakFirst goes through the autonomy gate unless already approved: when
// asked, the owner is shown exactly what would be sent. root may be nil.
func (m *Mind) speakFirst(ctx context.Context, p perception.Percept, person memory.Person,
	root *decision.Decision, s speech) spoken {
	log := m.Log.With("percept", p.ID.String(), "person", p.Speaker, "action", s.action)
	verdict := action.Auto
	if !s.approved {
		verdict = m.policy().Check(action.ReachOut, p.Speaker)
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
		text := d.Reply
		if s.file != nil {
			d.Reply += attachedLine(s.file.name)
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
		if err := m.send(ctx, p, text, s.file); err != nil {
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
