package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/Grandbusta/minime/internal/decision"
	"github.com/Grandbusta/minime/internal/memory"
	"github.com/Grandbusta/minime/internal/perception"
)

// openTestStore runs only when MINIME_TEST_DATABASE_URL points at a disposable database.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	url := os.Getenv("MINIME_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("MINIME_TEST_DATABASE_URL not set")
	}
	s, err := Open(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestPerceptAndDecisionRoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	p := perception.Percept{
		ID: ulid.Make(), Source: "test", ExternalID: ulid.Make().String(), Thread: "t1", Kind: "message",
		Text: "hello", Payload: []byte(`{"chat_id":1}`), At: time.Now().UTC(),
	}
	if isNew, err := s.SavePercept(ctx, p); err != nil || !isNew {
		t.Fatalf("first save: new=%v err=%v", isNew, err)
	}
	dup := p
	dup.ID = ulid.Make()
	if isNew, err := s.SavePercept(ctx, dup); err != nil || isNew {
		t.Fatalf("duplicate save: new=%v err=%v", isNew, err)
	}

	d := decision.Decision{
		ID: ulid.Make(), At: time.Now().UTC().Truncate(time.Microsecond), PerceptID: p.ID,
		SetID: "test@1", Tier: decision.TierSystem1, Provider: "fake/1",
		Answers: map[string]decision.Answer{"intent": {Type: decision.QuestionChoice, Choice: "remember", Confidence: 0.9}},
		Action:  "remember", Reply: "Noted.", Gate: decision.GateAuto, Latency: 120 * time.Millisecond, CostUSD: 0.0000126,
		Outcome: &decision.Outcome{Status: decision.OutcomeOK, At: time.Now().UTC()},
	}
	if err := s.SaveDecision(ctx, d); err != nil {
		t.Fatal(err)
	}

	got, err := s.RecentDecisions(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d decisions", len(got))
	}
	g := got[0]
	if g.ID != d.ID || g.PerceptID != p.ID || g.Tier != d.Tier || g.Answers["intent"].Choice != "remember" ||
		g.Reply != "Noted." || g.Latency != d.Latency || g.CostUSD != d.CostUSD ||
		g.Outcome == nil || g.Outcome.Status != decision.OutcomeOK || g.Affect != nil || g.ParentID != nil {
		t.Fatalf("round trip mismatch:\n got  %+v\n want %+v", g, d)
	}
}

func TestFactAreasAndRecallOrder(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	// Far in the future so these are the newest facts in the table.
	base := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Microsecond)

	sam, other := "test:"+ulid.Make().String(), "test:"+ulid.Make().String()
	save := func(i int, subject, text string, areas []string) memory.Fact {
		t.Helper()
		p := perception.Percept{ID: ulid.Make(), Source: "test", ExternalID: ulid.Make().String(), Speaker: sam,
			Kind: "message", Text: text, At: base.Add(time.Duration(i) * time.Minute)}
		if _, err := s.SavePercept(ctx, p); err != nil {
			t.Fatal(err)
		}
		f := memory.Fact{ID: ulid.Make(), PerceptID: p.ID, Subject: subject, Text: text, At: p.At, Areas: areas}
		if err := s.SaveFact(ctx, f); err != nil {
			t.Fatal(err)
		}
		return f
	}
	church := save(0, sam, "I go to Grace Chapel", []string{"religion"})
	save(1, sam, "I live with my parents", []string{"home", "family"})
	unlabelled := save(2, sam, "I like jollof", nil)
	save(3, other, "I go to St. Mary's", []string{"religion"}) // someone else: never recalled for Sam

	got, err := s.RecallCandidates(ctx, []string{sam}, []string{"religion"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0].ID != church.ID || got[0].Subject != sam || !slices.Equal(got[0].Areas, []string{"religion"}) {
		t.Errorf("Sam's same-area fact should come first, and only Sam's facts: %+v", got)
	}
	if got[1].ID != unlabelled.ID {
		t.Errorf("then the rest, newest first: %+v", got)
	}
}

func TestPeople(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond)
	dad := memory.Person{Key: "test:" + ulid.Make().String(), Relation: "Busta is Daniel's father.", Circle: "family",
		Source: memory.KnownFromBirth, FirstSeen: at, UpdatedAt: at}

	if added, err := s.AddPersonIfNew(ctx, dad); err != nil || !added {
		t.Fatalf("first add: %v %v", added, err)
	}
	changed := dad
	changed.Relation = "Busta is Daniel's uncle."
	if added, err := s.AddPersonIfNew(ctx, changed); err != nil || added {
		t.Fatalf("second add must not overwrite memory: %v %v", added, err)
	}

	got, ok, err := s.Person(ctx, dad.Key)
	if err != nil || !ok || got.Relation != dad.Relation || got.Circle != "family" || got.Source != memory.KnownFromBirth || !got.FirstSeen.Equal(at) {
		t.Fatalf("person = %+v, %v, %v", got, ok, err)
	}
}

func TestRecentExchanges(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	thread := ulid.Make().String()
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)

	// say saves a message at base+offset with a decision chain like the mind's: a
	// System 1 root and, if reply is set, a System 2 child that said it.
	say := func(offset time.Duration, text, reply string) perception.Percept {
		t.Helper()
		p := perception.Percept{ID: ulid.Make(), Source: "test", ExternalID: ulid.Make().String(),
			Thread: thread, Kind: "message", Text: text, At: base.Add(offset)}
		if _, err := s.SavePercept(ctx, p); err != nil {
			t.Fatal(err)
		}
		root := decision.Decision{ID: ulid.Make(), At: p.At, PerceptID: p.ID, SetID: "s@1",
			Tier: decision.TierSystem1, Action: "escalate", Gate: decision.GateAuto}
		if err := s.SaveDecision(ctx, root); err != nil {
			t.Fatal(err)
		}
		if reply != "" {
			child := decision.Decision{ID: ulid.Make(), ParentID: &root.ID, At: p.At.Add(time.Second), PerceptID: p.ID,
				SetID: "reply@x", Tier: decision.TierSystem2, Action: "reply", Reply: reply, Gate: decision.GateAuto}
			if err := s.SaveDecision(ctx, child); err != nil {
				t.Fatal(err)
			}
		}
		return p
	}
	say(0, "too old", "old reply")
	say(40*time.Minute, "I want help finding a dentist", "Which city are you in?")
	say(41*time.Minute, "no reply recorded", "")
	say(42*time.Minute, "", "") // no text: left out
	current := say(43*time.Minute, "Let's say Lagos", "")

	got, err := s.RecentExchanges(ctx, "test", thread, base.Add(30*time.Minute), current.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d exchanges: %+v", len(got), got)
	}
	if got[0].Message != "I want help finding a dentist" || got[0].Reply != "Which city are you in?" ||
		got[1].Message != "no reply recorded" || got[1].Reply != "" {
		t.Errorf("exchanges = %+v", got)
	}
}

func TestPublicFacts(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Microsecond)

	p := perception.Percept{ID: ulid.Make(), Source: "test", ExternalID: ulid.Make().String(), Kind: "message", At: base}
	if _, err := s.SavePercept(ctx, p); err != nil {
		t.Fatal(err)
	}
	mine := map[ulid.ULID]string{}
	var colour, pin memory.Fact
	for i, f := range []memory.Fact{
		{Subject: memory.SubjectSelf, Text: "Tobi's favourite colour is green"},
		{Subject: memory.SubjectSelf, Text: "Tobi's PIN is 4821"},
		{Subject: "test:someone", Text: "Sam likes jollof"},
	} {
		f.ID, f.PerceptID, f.At = ulid.Make(), p.ID, base.Add(time.Duration(i)*time.Minute)
		if err := s.SaveFact(ctx, f); err != nil {
			t.Fatal(err)
		}
		mine[f.ID] = f.Text
		switch i {
		case 0:
			colour = f
		case 1:
			pin = f
		}
	}
	ours := func(fs []memory.Fact) []string {
		var out []string
		for _, f := range fs {
			if _, ok := mine[f.ID]; ok {
				out = append(out, f.Text)
			}
		}
		return out
	}

	undecided, err := s.UndecidedPublicFacts(ctx, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if got := ours(undecided); !slices.Equal(got, []string{colour.Text, pin.Text}) {
		t.Errorf("undecided = %v, want the agent's two facts, oldest first", got)
	}
	if err := s.SetFactPublic(ctx, colour.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFactPublic(ctx, pin.ID, false); err != nil {
		t.Fatal(err)
	}
	undecided, _ = s.UndecidedPublicFacts(ctx, 1000)
	public, err := s.PublicFacts(ctx, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if got := ours(undecided); len(got) != 0 {
		t.Errorf("still undecided: %v", got)
	}
	if got := ours(public); !slices.Equal(got, []string{colour.Text}) {
		t.Errorf("public = %v, want only the colour", got)
	}
}

func TestDueIntentionsAndUnaskedFacts(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(3 * time.Hour).Truncate(time.Microsecond)
	person := "test:" + ulid.Make().String()

	msg := perception.Percept{ID: ulid.Make(), Source: "test", ExternalID: ulid.Make().String(), Thread: "chat-" + person,
		Speaker: person, Kind: "message", Text: "remind me", Payload: []byte(`{"chat_id":7}`), At: base}
	if _, err := s.SavePercept(ctx, msg); err != nil {
		t.Fatal(err)
	}
	due := memory.Intention{ID: ulid.Make(), Subject: person, Kind: memory.IntentionRemind, PerceptID: msg.ID, Text: "call mum",
		DueAt: base.Add(time.Hour), Status: memory.IntentionPending, CreatedAt: base}
	if err := s.SaveIntention(ctx, due); err != nil {
		t.Fatal(err)
	}
	ours := func(is []memory.Intention) (out []memory.Intention) {
		for _, i := range is {
			if i.Subject == person {
				out = append(out, i)
			}
		}
		return out
	}
	if is, err := s.DueIntentions(ctx, base, 100); err != nil || len(ours(is)) != 0 {
		t.Fatalf("due before its time: %+v, %v", is, err)
	}
	is, err := s.DueIntentions(ctx, base.Add(time.Hour), 100)
	if got := ours(is); err != nil || len(got) != 1 || got[0].Text != "call mum" || !got[0].DueAt.Equal(due.DueAt) ||
		got[0].PerceptID != msg.ID || !got[0].ProjectID.IsZero() || !got[0].DoneAt.IsZero() {
		t.Fatalf("due = %+v, %v", got, err)
	}
	if err := s.PostponeIntention(ctx, due.ID, base.Add(90*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if is, _ := s.DueIntentions(ctx, base.Add(time.Hour), 100); len(ours(is)) != 0 {
		t.Error("a postponed intention is still due")
	}
	if err := s.FinishIntention(ctx, due.ID, memory.IntentionDone, "", base.Add(90*time.Minute)); err != nil {
		t.Fatal(err)
	}
	is, err = s.IntentionsDue(ctx, base, base.Add(2*time.Hour))
	if got := ours(is); err != nil || len(got) != 1 || got[0].Status != memory.IntentionDone || got[0].DoneAt.IsZero() {
		t.Errorf("intentions due = %+v, %v", got, err)
	}

	fact := memory.Fact{ID: ulid.Make(), PerceptID: msg.ID, Subject: person, Text: "I have an interview", At: base}
	if err := s.SaveFact(ctx, fact); err != nil {
		t.Fatal(err)
	}
	if fs, err := s.UnaskedFacts(ctx, person, base.Add(-time.Hour), 10); err != nil || len(fs) != 1 {
		t.Fatalf("unasked = %+v, %v", fs, err)
	}
	if err := s.MarkFactsAsked(ctx, []ulid.ULID{fact.ID}, base); err != nil {
		t.Fatal(err)
	}
	if fs, _ := s.UnaskedFacts(ctx, person, base.Add(-time.Hour), 10); len(fs) != 0 {
		t.Errorf("still unasked: %+v", fs)
	}
}

func TestProjectWorksAndOwnIntentions(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(4 * time.Hour).Truncate(time.Microsecond)

	song := memory.Project{ID: ulid.Make(), Title: "Lagos Rain " + ulid.Make().String(), Kind: "music", Status: memory.ProjectActive,
		CreatedAt: base, UpdatedAt: base}
	if err := s.SaveProject(ctx, song); err != nil {
		t.Fatal(err)
	}

	own := memory.Intention{ID: ulid.Make(), Subject: memory.SubjectSelf, Kind: memory.IntentionActivity, ProjectID: song.ID,
		Text: "write verse 2", DueAt: base, Status: memory.IntentionPending, CreatedAt: base}
	if err := s.SaveIntention(ctx, own); err != nil {
		t.Fatal(err)
	}
	is, err := s.IntentionsDue(ctx, base, base.Add(time.Second))
	if err != nil || !slices.ContainsFunc(is, func(i memory.Intention) bool { return i.ID == own.ID && i.ProjectID == song.ID && i.PerceptID.IsZero() }) {
		t.Errorf("own intentions = %+v, %v", is, err)
	}

	for n, title := range []string{"verse 1", "verse 2"} {
		w := memory.Work{ID: ulid.Make(), IntentionID: own.ID, ProjectID: song.ID, Kind: "lyrics", Title: title, Text: "...",
			CreatedAt: base.Add(time.Duration(n) * time.Minute)}
		if err := s.SaveWork(ctx, w); err != nil {
			t.Fatal(err)
		}
	}
	journal := memory.Work{ID: ulid.Make(), Kind: "journal", Title: "today", Text: "good day", CreatedAt: base.Add(time.Hour)}
	if err := s.SaveWork(ctx, journal); err != nil {
		t.Fatal(err)
	}
	ws, err := s.ProjectWorks(ctx, song.ID, 1)
	if err != nil || len(ws) != 1 || ws[0].Title != "verse 2" || ws[0].IntentionID != own.ID {
		t.Errorf("project works = %+v, %v", ws, err)
	}
	if j, ok, err := s.LatestWork(ctx, "journal"); err != nil || !ok || j.ID != journal.ID || !j.ProjectID.IsZero() {
		t.Errorf("latest journal = %+v, %v, %v", j, ok, err)
	}

	person := "test:" + ulid.Make().String()
	task := func(text, status string) memory.Intention {
		return memory.Intention{ID: ulid.Make(), Subject: person, Kind: memory.IntentionTask, Text: text,
			Ability: "read_article", Args: map[string]string{"query": "afrobeat"}, DueAt: base.Add(time.Hour), Status: status, CreatedAt: base}
	}
	for _, i := range []memory.Intention{task("open", memory.IntentionPending), task("kept", memory.IntentionDone),
		{ID: ulid.Make(), Subject: person, Kind: memory.IntentionRemind, Text: "a reminder", DueAt: base, Status: memory.IntentionPending, CreatedAt: base}} {
		if err := s.SaveIntention(ctx, i); err != nil {
			t.Fatal(err)
		}
	}
	if open, err := s.OpenIntentions(ctx, person, memory.IntentionTask); err != nil || len(open) != 1 || open[0].Text != "open" ||
		open[0].Args["query"] != "afrobeat" {
		t.Errorf("open promises = %+v, %v", open, err)
	}

	d := decision.Decision{ID: ulid.Make(), At: base, SetID: "activity@1", Tier: decision.TierSystem2, Action: "activity",
		Gate: decision.GateAuto, CostUSD: 0.0021}
	if err := s.SaveDecision(ctx, d); err != nil {
		t.Fatal(err)
	}
	if cost, err := s.CostSince(ctx, base, []string{"activity", "plan_day"}); err != nil || cost < 0.0021 {
		t.Errorf("cost = %v, %v", cost, err)
	}
	if cost, _ := s.CostSince(ctx, base, []string{"reply"}); cost != 0 {
		t.Errorf("cost of other actions = %v, want 0", cost)
	}
}

func TestSourcesAndReadingProjects(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	url := "https://example.org/book/" + ulid.Make().String()

	src := memory.Source{ID: ulid.Make(), Reading: memory.Reading{Kind: memory.ReadBook, Title: "Pride and Prejudice",
		Author: "Jane Austen"}, URL: url, Text: "It is a truth universally acknowledged.", FetchedAt: now}
	id, err := s.SaveSource(ctx, src)
	if err != nil || id != src.ID {
		t.Fatalf("save = %v, %v", id, err)
	}
	again := src
	again.ID = ulid.Make()
	if id, err := s.SaveSource(ctx, again); err != nil || id != src.ID {
		t.Errorf("same URL again = %v, %v; want the first ID", id, err)
	}

	p := memory.Project{ID: ulid.Make(), Title: "Pride and Prejudice", Kind: "reading", Status: memory.ProjectActive,
		SourceID: src.ID, Position: 12, CreatedAt: now, UpdatedAt: now}
	if err := s.SaveProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	if r, ok, err := s.ReadingProject(ctx, src.ID); err != nil || !ok || r.ID != p.ID || r.Position != 12 {
		t.Errorf("reading project = %+v, %v, %v", r, ok, err)
	}
	if _, ok, _ := s.ReadingProject(ctx, ulid.Make()); ok {
		t.Error("found a project for a source nobody reads")
	}
}

func TestPublicWorks(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Microsecond).AddDate(0, 0, -400) // older than the other tests' works

	own := memory.Intention{ID: ulid.Make(), Subject: memory.SubjectSelf, Kind: memory.IntentionActivity, Text: "write",
		DueAt: base, Status: memory.IntentionDone, CreatedAt: base}
	promise := memory.Intention{ID: ulid.Make(), Subject: "test:" + ulid.Make().String(), Kind: memory.IntentionTask, Text: "a poem for them",
		DueAt: base, Status: memory.IntentionDone, CreatedAt: base}
	for _, i := range []memory.Intention{own, promise} {
		if err := s.SaveIntention(ctx, i); err != nil {
			t.Fatal(err)
		}
	}
	work := func(kind, title string, intention ulid.ULID, n int) memory.Work {
		w := memory.Work{ID: ulid.Make(), IntentionID: intention, Kind: kind, Title: title, Text: "...", CreatedAt: base.Add(time.Duration(n) * time.Minute)}
		if err := s.SaveWork(ctx, w); err != nil {
			t.Fatal(err)
		}
		return w
	}
	lyrics := work("lyrics", "Lagos Rain", own.ID, 0)
	waiting := work("lyrics", "Waiting", own.ID, 4)
	if err := s.AwaitWorkPublic(ctx, waiting.ID); err != nil {
		t.Fatal(err)
	}
	notes := work("notes", "On rhythm", ulid.ULID{}, 1)
	journal := work("journal", "29 Oct", own.ID, 2)
	forThem := work("writing", "NEPA Took the Light", promise.ID, 3)

	undecided, err := s.UndecidedPublicWorks(ctx, 1000)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[ulid.ULID]bool{}
	for _, w := range undecided {
		ids[w.ID] = true
	}
	if !ids[lyrics.ID] || !ids[notes.ID] || ids[journal.ID] || ids[forThem.ID] || ids[waiting.ID] {
		t.Errorf("undecided = %v; want only its own works, not the journal or the promise", ids)
	}

	set := func(id ulid.ULID, public bool, by string) bool {
		ok, err := s.SetWorkPublic(ctx, id, public, by)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if !set(lyrics.ID, true, memory.PublicBySystem1) || !set(notes.ID, true, memory.PublicBySystem1) {
		t.Fatal("couldn't make its own works public")
	}
	if set(journal.ID, true, memory.PublicByOwner) || set(forThem.ID, true, memory.PublicByOwner) {
		t.Error("made the journal or a promise public")
	}
	if !set(notes.ID, false, memory.PublicByOwner) || set(notes.ID, true, memory.PublicBySystem1) {
		t.Error("the owner's choice was overridden")
	}

	ws, err := s.PublicWorks(ctx, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var mine []memory.Work
	for _, w := range ws {
		if w.ID == lyrics.ID || w.ID == notes.ID || w.ID == journal.ID || w.ID == forThem.ID {
			mine = append(mine, w)
		}
	}
	if len(mine) != 1 || mine[0].ID != lyrics.ID || mine[0].Public == nil || !*mine[0].Public || mine[0].PublicBy != memory.PublicBySystem1 {
		t.Errorf("public works = %+v", mine)
	}
	if _, ok, err := s.PublicWork(ctx, lyrics.ID); err != nil || !ok {
		t.Errorf("public work: %v, %v", ok, err)
	}
	for _, id := range []ulid.ULID{notes.ID, journal.ID, forThem.ID, ulid.Make()} {
		if _, ok, err := s.PublicWork(ctx, id); err != nil || ok {
			t.Errorf("%s is public: %v, %v", id, ok, err)
		}
	}
	if w, ok, _ := s.Work(ctx, notes.ID); !ok || w.Public == nil || *w.Public || w.PublicBy != memory.PublicByOwner {
		t.Errorf("hidden work = %+v", w)
	}
}

func TestApprovals(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	person := "test:" + ulid.Make().String()

	promise := memory.Intention{ID: ulid.Make(), Subject: person, Kind: memory.IntentionTask, Text: "a verse",
		DueAt: now.Add(time.Hour), Status: memory.IntentionAwaiting, CreatedAt: now}
	if err := s.SaveIntention(ctx, promise); err != nil {
		t.Fatal(err)
	}
	if due, err := s.DueIntentions(ctx, now.Add(2*time.Hour), 1000); err != nil || slices.ContainsFunc(due, func(i memory.Intention) bool { return i.ID == promise.ID }) {
		t.Errorf("an awaiting intention is due: %v", err)
	}

	old := memory.Approval{ID: ulid.Make(), Class: "commit", Person: person, What: "a verse by 20:00", Ref: promise.ID,
		Status: memory.ApprovalPending, AskedAt: now.Add(-25 * time.Hour)}
	fresh := memory.Approval{ID: ulid.Make(), Class: "reach_out", Person: person, What: "message them", Text: "Hi!", Kind: "reminder",
		Status: memory.ApprovalPending, AskedAt: now}
	for _, a := range []memory.Approval{old, fresh} {
		if err := s.SaveApproval(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	unsent, err := s.UnsentApprovals(ctx, 1000)
	if err != nil || !slices.ContainsFunc(unsent, func(a memory.Approval) bool { return a.ID == fresh.ID && a.Text == "Hi!" }) {
		t.Fatalf("unsent = %+v, %v", unsent, err)
	}
	if err := s.MarkApprovalSent(ctx, fresh.ID, []byte(`{"chat_id":1,"message_id":2}`), now); err != nil {
		t.Fatal(err)
	}
	if a, ok, err := s.Approval(ctx, fresh.ID); err != nil || !ok || a.SentAt.IsZero() {
		t.Errorf("sent approval = %+v, %v", a, err)
	}
	stale, err := s.StaleApprovals(ctx, now.Add(-24*time.Hour), 1000)
	if err != nil || !slices.ContainsFunc(stale, func(a memory.Approval) bool { return a.ID == old.ID && a.Ref == promise.ID }) ||
		slices.ContainsFunc(stale, func(a memory.Approval) bool { return a.ID == fresh.ID }) {
		t.Errorf("stale = %+v, %v", stale, err)
	}
	if ok, err := s.DecideApproval(ctx, fresh.ID, memory.ApprovalApproved, now); err != nil || !ok {
		t.Fatalf("decide: %v, %v", ok, err)
	}
	if ok, _ := s.DecideApproval(ctx, fresh.ID, memory.ApprovalDenied, now); ok {
		t.Error("decided twice")
	}
	recent, err := s.RecentApprovals(ctx, 1000)
	if err != nil || len(recent) < 2 || recent[0].Status != memory.ApprovalPending {
		t.Errorf("recent = %+v, %v", recent, err)
	}
}

func TestLearning(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Truncate(time.Microsecond).AddDate(0, 0, -600) // older than other tests' data
	thread := "learn-" + ulid.Make().String()

	if err := s.SaveAutonomyRule(ctx, "reach_out", "auto", "test:owner", ulid.ULID{}, base); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAutonomyRule(ctx, "reach_out", "ask", "test:owner", ulid.ULID{}, base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if rules, err := s.AutonomyRules(ctx); err != nil || rules["reach_out"] != "ask" {
		t.Errorf("rules = %v, %v", rules, err)
	}

	// Two messages and their reads; the first is labelled, twice.
	msg := func(text string, at time.Time, set string, choice string) decision.Decision {
		p := perception.Percept{ID: ulid.Make(), Source: "test", ExternalID: ulid.Make().String(), Thread: thread, Kind: "message", Text: text, At: at}
		if _, err := s.SavePercept(ctx, p); err != nil {
			t.Fatal(err)
		}
		d := decision.Decision{ID: ulid.Make(), At: at, PerceptID: p.ID, SetID: set, Tier: decision.TierSystem1, Action: choice, Gate: decision.GateAuto,
			Answers: map[string]decision.Answer{"intent": {Type: decision.QuestionChoice, Choice: choice, Confidence: 0.6}}}
		if err := s.SaveDecision(ctx, d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	first := msg("I need a dentist", base, "incoming_message@1,life_area@1", "share")
	second := msg("No, I was asking for help", base.Add(time.Minute), "incoming_message@1", "other")

	if id, ok, err := s.LastRead(ctx, "incoming_message", "test", thread, base.Add(time.Minute)); err != nil || !ok || id != first.ID {
		t.Errorf("last read = %s, %v, %v; want the first message's", id, ok, err)
	}
	for _, v := range []string{"small_talk", "ask_for_help"} {
		if err := s.SaveLabel(ctx, memory.Label{DecisionID: first.ID, Question: "intent", Value: v, Source: memory.LabelFromChat, At: base}); err != nil {
			t.Fatal(err)
		}
	}
	labels, err := s.Labels(ctx, []ulid.ULID{first.ID, second.ID})
	if err != nil || len(labels) != 1 || labels[0].Value != "ask_for_help" {
		t.Errorf("labels = %+v, %v", labels, err)
	}
	examples, err := s.LabelledExamples(ctx, "incoming_message", "intent", 1000)
	if err != nil {
		t.Fatal(err)
	}
	var mine []Example
	for _, e := range examples {
		if e.DecisionID == first.ID {
			mine = append(mine, e)
		}
	}
	if len(mine) != 1 || mine[0].Text != "I need a dentist" || mine[0].SetID != "incoming_message@1" || mine[0].Label != "ask_for_help" ||
		mine[0].Answer.Choice != "share" || mine[0].Answer.Confidence != 0.6 {
		t.Errorf("examples = %+v", mine)
	}
}

func TestJudgment(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	tag := ulid.Make().String()
	cases := []memory.JudgmentCase{
		{ID: ulid.Make(), Key: "tiny-" + tag, Type: "general", Situation: "What to build first.", Options: []string{"all", "tiny"},
			Choice: "tiny", Reason: "build on it", Status: memory.JudgmentProposed, Source: memory.JudgmentFromSeed, At: now},
		{ID: ulid.Make(), Key: "ask-" + tag, Type: "autonomy", Situation: "Who approves.", Choice: "the owner",
			Status: memory.JudgmentProposed, Source: memory.JudgmentFromSeed, At: now.Add(time.Second)},
	}
	p := memory.Principle{ID: ulid.Make(), Text: "Start small " + tag, Seed: "Start small " + tag, Types: []string{"general"},
		Cases: []string{"tiny-" + tag}, Status: memory.JudgmentProposed, Source: memory.JudgmentFromSeed, CreatedAt: now}
	if nc, np, err := s.SeedJudgment(ctx, cases, []memory.Principle{p}); err != nil || nc != 2 || np != 1 {
		t.Fatalf("seed = %d cases, %d principles, %v", nc, np, err)
	}
	if ok, err := s.ReviewPrinciple(ctx, p.ID, memory.JudgmentApproved, "Start small, then grow "+tag, now); err != nil || !ok {
		t.Fatalf("review = %v, %v", ok, err)
	}
	again := p
	again.ID = ulid.Make()
	cases[0].ID, cases[1].ID = ulid.Make(), ulid.Make()
	if nc, np, err := s.SeedJudgment(ctx, cases, []memory.Principle{again}); err != nil || nc != 0 || np != 0 {
		t.Fatalf("seed again = %d cases, %d principles, %v", nc, np, err)
	}
	ps, err := s.Principles(ctx, memory.JudgmentApproved)
	if err != nil {
		t.Fatal(err)
	}
	var got *memory.Principle
	for i := range ps {
		if ps[i].ID == p.ID {
			got = &ps[i]
		}
	}
	if got == nil || got.Text != "Start small, then grow "+tag || len(got.Cases) != 1 || got.Cases[0] != "tiny-"+tag {
		t.Errorf("principle = %+v", got)
	}
	cs, err := s.JudgmentCases(ctx, memory.JudgmentProposed, []string{"autonomy"})
	if err != nil {
		t.Fatal(err)
	}
	var ask *memory.JudgmentCase
	for i := range cs {
		if cs[i].Key == "ask-"+tag {
			ask = &cs[i]
		}
	}
	if ask == nil || ask.Reason != "" || len(ask.Options) != 0 {
		t.Fatalf("ask case = %+v", ask)
	}
}

func TestCalls(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond).AddDate(2, 0, 0) // after other tests' calls
	kind := "focus_" + ulid.Make().String()[:8]
	c := memory.Call{ID: ulid.Make(), Type: kind, Mode: memory.CallAfter, Situation: "What to focus on today.",
		Options: []string{"the motif", "Equiano"}, Pick: 0, Why: "the stage is due", Confidence: 0.7, Status: memory.CallLogged, CreatedAt: now}
	if err := s.SaveCall(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkCallAsked(ctx, c.ID, json.RawMessage(`{"chat_id":1,"message_id":2}`), now); err != nil {
		t.Fatal(err)
	}
	if n, err := s.CallsAskedSince(ctx, memory.CallAfter, now.Add(-time.Minute)); err != nil || n != 1 {
		t.Errorf("asked since = %d, %v", n, err)
	}
	if stale, err := s.StaleCalls(ctx, now.Add(time.Minute), 100); err != nil || !slices.ContainsFunc(stale, func(x memory.Call) bool { return x.ID == c.ID }) {
		t.Errorf("stale = %v, %v", stale, err)
	}
	c.OwnerPick, c.AnsweredBy, c.AnsweredAt, c.Waiting = 1, memory.AnsweredOnTelegram, now.Add(time.Minute), memory.CallWaitingReason
	if ok, err := s.AnswerCall(ctx, c, true); err != nil || !ok {
		t.Fatalf("answer = %v, %v", ok, err)
	}
	if ok, _ := s.AnswerCall(ctx, c, true); ok {
		t.Error("answered twice")
	}
	w, ok, err := s.WaitingCall(ctx, now)
	if err != nil || !ok || w.ID != c.ID || w.Waiting != memory.CallWaitingReason {
		t.Fatalf("waiting = %+v, %v, %v", w, ok, err)
	}
	if err := s.GiveCallReason(ctx, c.ID, "reading feeds the music", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Call(ctx, c.ID)
	if err != nil || !ok || got.Status != memory.CallAnswered || got.Agreed == nil || *got.Agreed || got.OwnerReason != "reading feeds the music" ||
		got.Waiting != "" || got.CaseID.IsZero() || len(got.Message) == 0 {
		t.Fatalf("call = %+v, %v, %v", got, ok, err)
	}
	cases, _ := s.JudgmentCases(ctx, memory.JudgmentApproved, []string{"life"})
	if !slices.ContainsFunc(cases, func(x memory.JudgmentCase) bool {
		return x.ID == got.CaseID && x.Choice == "Equiano" && x.Reason == "reading feeds the music" && x.Source == memory.AnsweredOnTelegram
	}) {
		t.Error("the answer didn't become an approved case with its reason")
	}
	agree := memory.Call{ID: ulid.Make(), Type: kind, Mode: memory.CallAfter, Situation: "s", Options: []string{"a", "b"}, Status: memory.CallLogged,
		CreatedAt: now}
	if err := s.SaveCall(ctx, agree); err != nil {
		t.Fatal(err)
	}
	agree.OwnerPick, agree.AnsweredBy, agree.AnsweredAt = 0, memory.AnsweredInConsole, now.Add(3*time.Minute)
	if ok, err := s.AnswerCall(ctx, agree, false); err != nil || !ok {
		t.Fatalf("answer = %v, %v", ok, err)
	}
	as, err := s.Agreement(ctx, 20)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(as, func(a memory.Agreement) bool { return a.Type == kind && a.Answered == 2 && a.Agreed == 1 }) {
		t.Errorf("agreement = %+v", as)
	}
}

func TestRankPriority(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	tag := ulid.Make().String()
	var ps []memory.Principle
	for i, text := range []string{"health", "honesty", "music"} {
		ps = append(ps, memory.Principle{ID: ulid.Make(), Text: text + tag, Seed: text + tag, Kind: memory.PrincipleRank, Rank: i + 1,
			Status: memory.JudgmentProposed, Source: memory.JudgmentFromSeed, CreatedAt: now})
	}
	if _, _, err := s.SeedJudgment(ctx, nil, ps); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.RankPriority(ctx, ps[2].ID, 1, now); err != nil || !ok {
		t.Fatalf("rank = %v, %v", ok, err)
	}
	got, _ := s.Principles(ctx, memory.JudgmentProposed)
	var order []string
	for _, p := range got {
		if strings.HasSuffix(p.Text, tag) {
			order = append(order, strings.TrimSuffix(p.Text, tag))
		}
	}
	if strings.Join(order, ",") != "music,health,honesty" {
		t.Errorf("order = %v", order)
	}
	if ok, _ := s.RankPriority(ctx, ulid.Make(), 1, now); ok {
		t.Error("ranked a priority that doesn't exist")
	}
}

func TestOutcomes(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond).AddDate(3, 0, 0) // after other tests' calls
	kind := "focus_" + ulid.Make().String()[:8]
	c := memory.Call{ID: ulid.Make(), Type: kind, Mode: memory.CallAfter, Situation: "s", Options: []string{"a", "b"}, Status: memory.CallLogged,
		CreatedAt: now.Add(-3 * 24 * time.Hour)}
	if err := s.SaveCall(ctx, c); err != nil {
		t.Fatal(err)
	}
	c.OwnerPick, c.AnsweredBy, c.AnsweredAt = 1, memory.AnsweredInConsole, now
	if _, err := s.AnswerCall(ctx, c, true); err != nil {
		t.Fatal(err)
	}
	cs, err := s.CallsToLookBack(ctx, []string{kind}, now.Add(-14*24*time.Hour), now.Add(-2*24*time.Hour), 10)
	if err != nil || len(cs) != 1 || cs[0].ID != c.ID {
		t.Fatalf("to look back on = %+v, %v", cs, err)
	}
	if ok, err := s.SetOutcome(ctx, c.ID, memory.OutcomeBadly, memory.VerdictUnlucky, "it rained", memory.OutcomeBySelf, now); err != nil || !ok {
		t.Fatalf("set = %v, %v", ok, err)
	}
	if ok, _ := s.SetOutcome(ctx, c.ID, memory.OutcomeBadly, memory.VerdictBad, "", memory.OutcomeByOwner, now); !ok {
		t.Fatal("the owner couldn't correct it")
	}
	if ok, _ := s.SetOutcome(ctx, c.ID, memory.OutcomeWell, memory.VerdictGood, "x", memory.OutcomeBySelf, now); ok {
		t.Error("its review overrode the owner")
	}
	got, _, _ := s.Call(ctx, c.ID)
	if got.Outcome != memory.OutcomeBadly || got.Verdict != memory.VerdictBad || got.OutcomeNote != "it rained" || got.OutcomeBy != memory.OutcomeByOwner {
		t.Errorf("call = %+v", got)
	}
	cases, _ := s.JudgmentCases(ctx, memory.JudgmentApproved, nil)
	if !slices.ContainsFunc(cases, func(x memory.JudgmentCase) bool {
		return x.ID == got.CaseID && x.Outcome == "went badly, bad call: it rained"
	}) {
		t.Error("the decision doesn't say how it turned out")
	}
	if cs, _ := s.CallsToLookBack(ctx, []string{kind}, now.Add(-14*24*time.Hour), now, 10); len(cs) != 0 {
		t.Error("looked back on it twice")
	}
}

func TestTastes(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond).AddDate(5, 0, 0) // newest of all tests' tastes
	a := memory.Taste{ID: ulid.Make(), Kind: "lyrics", Likes: true, Text: "one repeated word", Source: memory.TasteFromTelegram, At: now}
	b := memory.Taste{ID: ulid.Make(), Likes: false, Text: "long intros", Source: memory.TasteFromConsole, At: now.Add(time.Second)}
	for _, x := range []memory.Taste{a, b} {
		if err := s.SaveTaste(ctx, x); err != nil {
			t.Fatal(err)
		}
	}
	ts, err := s.Tastes(ctx, 2)
	if err != nil || len(ts) != 2 || ts[0].ID != b.ID || ts[1].Kind != "lyrics" || !ts[1].Likes {
		t.Fatalf("tastes = %+v, %v", ts, err)
	}
	if ok, err := s.RetireTaste(ctx, b.ID, now); err != nil || !ok {
		t.Fatalf("retire = %v, %v", ok, err)
	}
	if ok, _ := s.RetireTaste(ctx, b.ID, now); ok {
		t.Error("retired twice")
	}
	if ts, _ := s.Tastes(ctx, 1); len(ts) != 1 || ts[0].ID != a.ID {
		t.Errorf("after retiring = %+v", ts)
	}
}

func TestWhose(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	name, ok, err := s.Whose(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !ok { // a fresh test database: the first claim makes it theirs
		if err := s.Claim(ctx, "Tobi", time.Now()); err != nil {
			t.Fatal(err)
		}
		name = "Tobi"
	}
	if err := s.Claim(ctx, name, time.Now()); err != nil {
		t.Errorf("claiming it again: %v", err)
	}
	err = s.Claim(ctx, name+"-someone-else", time.Now())
	if !errors.Is(err, ErrNotTheirs) || !strings.Contains(err.Error(), name+"'s") {
		t.Errorf("someone else's claim = %v", err)
	}
}

func TestWorksFor(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond).AddDate(6, 0, 0) // newest of all tests' works
	sam, ada := "test:sam-"+ulid.Make().String(), "test:ada-"+ulid.Make().String()
	work := func(title, kind, subject string, n int) memory.Work {
		w := memory.Work{ID: ulid.Make(), Kind: kind, Title: title, Text: "x", CreatedAt: at.Add(time.Duration(n) * time.Second)}
		if subject != "" {
			i := memory.Intention{ID: ulid.Make(), Subject: subject, Kind: memory.IntentionTask, Text: title, DueAt: at,
				Status: memory.IntentionDone, CreatedAt: at}
			if err := s.SaveIntention(ctx, i); err != nil {
				t.Fatal(err)
			}
			w.IntentionID = i.ID
		}
		if err := s.SaveWork(ctx, w); err != nil {
			t.Fatal(err)
		}
		return w
	}
	work("beat", "beat_idea", memory.SubjectSelf, 1)
	work("for sam", "writing", sam, 2)
	work("for ada", "writing", ada, 3)
	work("diary", "journal", "", 4)
	work("notes", "notes", "", 5)
	ws, err := s.WorksFor(ctx, sam, 3)
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, w := range ws {
		titles = append(titles, w.Title)
	}
	if strings.Join(titles, ",") != "notes,for sam,beat" {
		t.Fatalf("works = %s", strings.Join(titles, ","))
	}
	if ws[0].MadeFor != "" || ws[1].MadeFor != sam || ws[2].MadeFor != memory.SubjectSelf {
		t.Errorf("made for: %q, %q, %q", ws[0].MadeFor, ws[1].MadeFor, ws[2].MadeFor)
	}
}

func TestWorkData(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond)
	beat := memory.Work{ID: ulid.Make(), Kind: "beat_idea", Title: "beat", Text: "x", CreatedAt: at,
		Data: json.RawMessage(`{"bpm": 82}`), DataKind: "score"}
	old := memory.Work{ID: ulid.Make(), Kind: "beat_idea", Title: "old", Text: "x", CreatedAt: at}
	for _, w := range []memory.Work{beat, old} {
		if err := s.SaveWork(ctx, w); err != nil {
			t.Fatal(err)
		}
	}
	if w, _, err := s.Work(ctx, beat.ID); err != nil || string(w.Data) != `{"bpm": 82}` || !w.Carries("score") {
		t.Errorf("data = %s %q, %v", w.Data, w.DataKind, err)
	}
	if w, _, _ := s.Work(ctx, old.ID); w.Data != nil || w.Carries("score") {
		t.Errorf("no data = %s %q", w.Data, w.DataKind)
	}
	if ws, err := s.WorksMissingData(ctx, []string{"beat_idea"}, 50); err != nil || !slices.ContainsFunc(ws, func(w memory.Work) bool { return w.ID == old.ID }) ||
		slices.ContainsFunc(ws, func(w memory.Work) bool { return w.ID == beat.ID }) {
		t.Errorf("missing data = %d works, %v", len(ws), err)
	}
	if err := s.SetWorkData(ctx, old.ID, "score", json.RawMessage(`{"bpm":90}`)); err != nil {
		t.Fatal(err)
	}
	if w, _, _ := s.Work(ctx, old.ID); string(w.Data) != `{"bpm": 90}` || w.DataKind != "score" {
		t.Errorf("set data = %s %q", w.Data, w.DataKind)
	}
}

func TestActions(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond)
	a := memory.Action{ID: ulid.Make(), Tool: "notes.write_file", Class: "write", Args: `{"path":"plan.md"}`, Why: "asked",
		Status: memory.ActionProposed, ProposedAt: at}
	if err := s.SaveAction(ctx, a); err != nil {
		t.Fatal(err)
	}
	if n, err := s.ActionsSince(ctx, at); err != nil || n < 1 {
		t.Errorf("since = %d, %v", n, err)
	}
	if moved, err := s.SetAction(ctx, a.ID, []string{memory.ActionProposed}, memory.ActionApproved, "", at); err != nil || !moved {
		t.Fatalf("approve = %v, %v", moved, err)
	}
	if moved, _ := s.SetAction(ctx, a.ID, []string{memory.ActionProposed}, memory.ActionDenied, "late", at); moved {
		t.Error("an approved action was denied after")
	}
	run, err := s.Actions(ctx, []string{memory.ActionApproved}, 50)
	if err != nil || !slices.ContainsFunc(run, func(x memory.Action) bool { return x.ID == a.ID && x.Args == a.Args }) {
		t.Fatalf("to run = %+v, %v", run, err)
	}
	if moved, _ := s.SetAction(ctx, a.ID, []string{memory.ActionApproved}, memory.ActionDone, "wrote plan.md", at); !moved {
		t.Fatal("not done")
	}
	recent, _ := s.RecentActions(ctx, 50)
	if i := slices.IndexFunc(recent, func(x memory.Action) bool { return x.ID == a.ID }); i < 0 || recent[i].Status != memory.ActionDone ||
		recent[i].Result != "wrote plan.md" || !recent[i].DoneAt.Equal(at) {
		t.Errorf("recent = %+v", recent)
	}
}
