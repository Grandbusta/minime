package store

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"minime/internal/decision"
	"minime/internal/memory"
	"minime/internal/perception"
	"minime/internal/search"
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

func TestFacts(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(time.Hour).Truncate(time.Microsecond) // newer than anything else in the table

	var want []string
	for i, text := range []string{"my car is a blue Golf", "my passport expires in March 2027", "I live in Ikoyi"} {
		p := perception.Percept{ID: ulid.Make(), Source: "test", ExternalID: ulid.Make().String(), Kind: "message",
			Text: text, At: base.Add(time.Duration(i) * time.Minute)}
		if _, err := s.SavePercept(ctx, p); err != nil {
			t.Fatal(err)
		}
		f := memory.Fact{ID: ulid.Make(), PerceptID: p.ID, Text: text, At: p.At}
		if err := s.SaveFact(ctx, f); err != nil {
			t.Fatal(err)
		}
		want = append([]string{text}, want...) // newest first
	}

	got, err := s.RecentFacts(ctx, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Text != want[0] || got[1].Text != want[1] || got[0].PerceptID.IsZero() {
		t.Errorf("facts = %+v, want the newest two of %v", got, want)
	}
}

func TestFactAreasAndRecallOrder(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	// Far in the future so these are the newest facts in the table.
	base := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Microsecond)

	sam, other := "test:"+ulid.Make().String(), "test:"+ulid.Make().String() // fresh people per run
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

	todo, err := s.UnlabelledFacts(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(todo, func(f memory.Fact) bool { return f.ID == unlabelled.ID && f.Subject == sam }) {
		t.Fatalf("unlabelled fact missing: %+v", todo)
	}
	if err := s.SetFactAreas(ctx, unlabelled.ID, []string{"preferences"}); err != nil {
		t.Fatal(err)
	}
	todo, _ = s.UnlabelledFacts(ctx, 100)
	if slices.ContainsFunc(todo, func(f memory.Fact) bool { return f.ID == unlabelled.ID }) {
		t.Error("fact still unlabelled after SetFactAreas")
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

	got.Name, got.UpdatedAt = "Busta", at.Add(time.Minute)
	if err := s.SavePerson(ctx, got); err != nil {
		t.Fatal(err)
	}
	if again, _, _ := s.Person(ctx, dad.Key); again.Name != "Busta" || !again.FirstSeen.Equal(at) {
		t.Errorf("after save = %+v", again)
	}

	if _, ok, err := s.Person(ctx, "test:nobody"); err != nil || ok {
		t.Errorf("unknown person: %v %v", ok, err)
	}
	known, err := s.KnownPeople(ctx, 1000)
	if err != nil || !slices.ContainsFunc(known, func(p memory.Person) bool { return p.Key == dad.Key }) {
		t.Errorf("known people = %+v, %v", known, err)
	}
}

func TestRecentExchanges(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	thread := ulid.Make().String() // a fresh conversation per run
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)

	// say saves a message at base+offset, plus a decision chain like the mind's:
	// a System 1 root and, if reply is set, a System 2 child that said it.
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

	limited, err := s.RecentExchanges(ctx, "test", thread, base, current.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 1 || limited[0].Message != "no reply recorded" {
		t.Errorf("limit keeps the newest: %+v", limited)
	}
}

func TestPublicFactsAndProfile(t *testing.T) {
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

	// A decision the agent makes on its own has no percept.
	d := decision.Decision{ID: ulid.Make(), At: base, SetID: "public_profile@1", Tier: decision.TierSystem1,
		Action: "decide_public", Gate: decision.GateAuto}
	if err := s.SaveDecision(ctx, d); err != nil {
		t.Fatal(err)
	}
	ds, err := s.RecentDecisions(ctx, 1)
	if err != nil || len(ds) != 1 || ds[0].ID != d.ID || !ds[0].PerceptID.IsZero() {
		t.Fatalf("decisions = %+v, err = %v", ds, err)
	}

	for _, inputs := range []string{"a", "b"} {
		if err := s.SaveProfile(ctx, []byte(`{"about":["`+inputs+`"]}`), inputs, base); err != nil {
			t.Fatal(err)
		}
	}
	page, inputs, at, ok, err := s.Profile(ctx)
	if err != nil || !ok || inputs != "b" || !at.Equal(base) || !strings.Contains(string(page), `"b"`) {
		t.Errorf("profile = %s, %q, %v, %v, %v", page, inputs, at, ok, err)
	}
}

func TestRemindersConversationsAndCheckIns(t *testing.T) {
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

	// The agent speaks first in the same thread.
	checkIn := perception.Percept{ID: ulid.Make(), Source: "test", ExternalID: "check_in:" + ulid.Make().String(),
		Thread: msg.Thread, Speaker: person, Kind: perception.KindCheckIn, At: base.Add(2 * time.Hour)}
	if _, err := s.SavePercept(ctx, checkIn); err != nil {
		t.Fatal(err)
	}
	d := decision.Decision{ID: ulid.Make(), At: checkIn.At, PerceptID: checkIn.ID, SetID: "reply@x", Tier: decision.TierSystem2,
		Action: "check_in", Reply: "How did the interview go?", Gate: decision.GateAuto}
	if err := s.SaveDecision(ctx, d); err != nil {
		t.Fatal(err)
	}

	convs, err := s.Conversations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var conv *memory.Conversation
	for i := range convs {
		if convs[i].Person == person {
			conv = &convs[i]
		}
	}
	if conv == nil || conv.Thread != msg.Thread || !conv.LastActivity.Equal(checkIn.At) || !strings.Contains(string(conv.Payload), `"chat_id"`) {
		t.Fatalf("conversation = %+v", conv)
	}
	last, ok, err := s.LastSpokeFirst(ctx, person, perception.KindCheckIn)
	if err != nil || !ok || !last.Equal(checkIn.At) {
		t.Errorf("last check-in = %v, %v, %v", last, ok, err)
	}
	if _, ok, _ := s.LastSpokeFirst(ctx, person, perception.KindReminder); ok {
		t.Error("found a reminder that was never sent")
	}

	exchanges, err := s.RecentExchanges(ctx, "test", msg.Thread, base.Add(-time.Minute), ulid.Make(), 10)
	if err != nil || len(exchanges) != 2 || exchanges[1].Message != "" || exchanges[1].Reply != "How did the interview go?" {
		t.Errorf("exchanges = %+v, %v; want the message and then the check-in", exchanges, err)
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

func TestProjectsWorksAndOwnIntentions(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	base := time.Now().UTC().Add(4 * time.Hour).Truncate(time.Microsecond)

	song := memory.Project{ID: ulid.Make(), Title: "Lagos Rain " + ulid.Make().String(), Kind: "music", Status: memory.ProjectActive,
		CreatedAt: base, UpdatedAt: base}
	if err := s.SaveProject(ctx, song); err != nil {
		t.Fatal(err)
	}
	song.Notes, song.UpdatedAt = "verse 1 done", base.Add(time.Minute)
	if err := s.SaveProject(ctx, song); err != nil {
		t.Fatal(err)
	}
	if p, ok, err := s.Project(ctx, song.ID); err != nil || !ok || p.Notes != "verse 1 done" {
		t.Fatalf("project = %+v, %v, %v", p, ok, err)
	}
	active, err := s.ActiveProjects(ctx, 100)
	if err != nil || !slices.ContainsFunc(active, func(p memory.Project) bool { return p.ID == song.ID }) {
		t.Errorf("active = %+v, %v", active, err)
	}

	// An activity of its own: no percept, part of the project.
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
	if ws, err := s.WorksBetween(ctx, base, base.Add(2*time.Minute)); err != nil || len(ws) != 2 || ws[0].Title != "verse 1" {
		t.Errorf("works between = %+v, %v", ws, err)
	}
	if j, ok, err := s.LatestWork(ctx, "journal"); err != nil || !ok || j.ID != journal.ID || !j.ProjectID.IsZero() {
		t.Errorf("latest journal = %+v, %v, %v", j, ok, err)
	}
	if w, ok, err := s.IntentionWork(ctx, own.ID); err != nil || !ok || w.Title != "verse 2" {
		t.Errorf("intention work = %+v, %v, %v", w, ok, err)
	}
	if _, ok, err := s.IntentionWork(ctx, ulid.Make()); err != nil || ok {
		t.Errorf("work for an intention without one: %v, %v", ok, err)
	}

	// Promises to a person: only the pending tasks for them.
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

func TestSourcesAndReadingProgress(t *testing.T) {
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
	if text, err := s.SourceText(ctx, src.ID); err != nil || text != src.Text {
		t.Errorf("text = %q, %v", text, err)
	}

	p := memory.Project{ID: ulid.Make(), Title: "Pride and Prejudice", Kind: "reading", Status: memory.ProjectActive,
		SourceID: src.ID, Position: 12, CreatedAt: now, UpdatedAt: now}
	if err := s.SaveProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Project(ctx, p.ID)
	if err != nil || !ok || got.SourceID != src.ID || got.Position != 12 || got.Source == nil ||
		got.Source.Title != "Pride and Prejudice" || got.Source.Chars != len(src.Text) {
		t.Errorf("project = %+v (%+v), %v", got, got.Source, err)
	}
	if r, ok, err := s.ReadingProject(ctx, src.ID); err != nil || !ok || r.ID != p.ID || r.Position != 12 {
		t.Errorf("reading project = %+v, %v, %v", r, ok, err)
	}
	if _, ok, _ := s.ReadingProject(ctx, ulid.Make()); ok {
		t.Error("found a project for a source nobody reads")
	}

	i := memory.Intention{ID: ulid.Make(), Subject: memory.SubjectSelf, Kind: memory.IntentionActivity, Text: "learn",
		Ability: "read_article", Args: map[string]string{"query": "polyrhythm"}, DueAt: now.AddDate(0, 0, -50), Status: memory.IntentionPending, CreatedAt: now}
	if err := s.SaveIntention(ctx, i); err != nil {
		t.Fatal(err)
	}
	is, err := s.IntentionsDue(ctx, i.DueAt, i.DueAt.Add(time.Second))
	if err != nil || len(is) != 1 || is[0].Ability != "read_article" || is[0].Args["query"] != "polyrhythm" {
		t.Errorf("intention = %+v, %v", is, err)
	}
	if err := s.FinishIntention(ctx, i.ID, memory.IntentionDone, "", now); err != nil {
		t.Fatal(err)
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
	// The owner hides the notes; the daily check can't bring them back.
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
	if err := s.ResumeIntention(ctx, promise.ID, now.Add(3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if is, _ := s.OpenIntentions(ctx, person, memory.IntentionTask); len(is) != 1 || !is[0].DueAt.Equal(now.Add(3*time.Hour)) {
		t.Errorf("resumed = %+v", is)
	}
	if err := s.AwaitIntention(ctx, promise.ID); err != nil {
		t.Fatal(err)
	}
	if is, _ := s.OpenIntentions(ctx, person, memory.IntentionTask); len(is) != 0 {
		t.Errorf("awaiting again, but open: %+v", is)
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
	if a, ok, err := s.Approval(ctx, fresh.ID); err != nil || !ok || a.SentAt.IsZero() || string(a.Message) != `{"chat_id": 1, "message_id": 2}` {
		t.Errorf("sent approval = %+v (%s), %v", a, a.Message, err)
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

	// Permission changes: every one kept, the newest per class wins.
	if err := s.SaveAutonomyRule(ctx, "reach_out", "auto", "test:owner", ulid.ULID{}, base); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveAutonomyRule(ctx, "reach_out", "ask", "test:owner", ulid.ULID{}, base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if rules, err := s.AutonomyRules(ctx); err != nil || rules["reach_out"] != "ask" {
		t.Errorf("rules = %v, %v", rules, err)
	}

	// Forgetting a fact.
	fp := perception.Percept{ID: ulid.Make(), Source: "test", ExternalID: ulid.Make().String(), Thread: thread, Kind: "message", At: base}
	if _, err := s.SavePercept(ctx, fp); err != nil {
		t.Fatal(err)
	}
	f := memory.Fact{ID: ulid.Make(), PerceptID: fp.ID, Subject: "test:x", Text: "x lives in Ikoyi", At: base}
	if err := s.SaveFact(ctx, f); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.DeleteFact(ctx, f.ID); err != nil || !ok {
		t.Errorf("delete: %v, %v", ok, err)
	}
	if ok, _ := s.DeleteFact(ctx, f.ID); ok {
		t.Error("deleted twice")
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

func TestGrowth(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	dream := memory.Dream{ID: ulid.Make(), Text: "be a producer", Status: memory.DreamActive, Source: memory.DreamFromLife, Since: now}
	old := memory.Dream{ID: ulid.Make(), Text: "be a DJ", Status: memory.DreamActive, Source: memory.DreamFromSelf, Since: now}
	for _, d := range []memory.Dream{dream, old} {
		if err := s.SaveDream(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	old.Status, old.EndedAt = memory.DreamDropped, now
	if err := s.SaveDream(ctx, old); err != nil {
		t.Fatal(err)
	}
	active, err := s.Dreams(ctx, true)
	if err != nil || !slices.ContainsFunc(active, func(d memory.Dream) bool { return d.ID == dream.ID }) ||
		slices.ContainsFunc(active, func(d memory.Dream) bool { return d.ID == old.ID }) {
		t.Errorf("active dreams = %+v, %v", active, err)
	}

	g := memory.Goal{ID: ulid.Make(), DreamID: dream.ID, Text: "five beat ideas", DoneWhen: "five in the EP", Due: now.AddDate(0, 1, 0),
		Status: memory.GoalActive, CreatedAt: now, UpdatedAt: now}
	if err := s.SaveGoal(ctx, g); err != nil {
		t.Fatal(err)
	}
	g.Progress = "two done"
	if err := s.SaveGoal(ctx, g); err != nil {
		t.Fatal(err)
	}
	goals, err := s.Goals(ctx, true)
	if err != nil || !slices.ContainsFunc(goals, func(x memory.Goal) bool { return x.ID == g.ID && x.Progress == "two done" && x.DreamID == dream.ID }) {
		t.Errorf("goals = %+v, %v", goals, err)
	}
	p := memory.Project{ID: ulid.Make(), Title: "The EP", Kind: "music", Status: memory.ProjectActive, GoalID: g.ID, CreatedAt: now, UpdatedAt: now}
	if err := s.SaveProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := s.Project(ctx, p.ID); err != nil || !ok || got.GoalID != g.ID {
		t.Errorf("project = %+v, %v", got, err)
	}

	name := "skill-" + ulid.Make().String()
	if err := s.AddSkill(ctx, memory.Skill{Name: name, Level: 3, ShownBy: []string{"beat_idea"}, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddSkill(ctx, memory.Skill{Name: name, Level: 9, UpdatedAt: now}); err != nil { // already has it: kept
		t.Fatal(err)
	}
	if err := s.SetSkillLevel(ctx, name, 4.6, now); err != nil {
		t.Fatal(err)
	}
	skills, err := s.Skills(ctx)
	if err != nil || !slices.ContainsFunc(skills, func(sk memory.Skill) bool { return sk.Name == name && sk.Level == 4.6 && sk.ShownBy[0] == "beat_idea" }) {
		t.Errorf("skills = %+v, %v", skills, err)
	}
	for i, score := range []float64{5, 6.5} {
		if err := s.SaveEvidence(ctx, memory.Evidence{ID: ulid.Make(), Skill: name, Score: score, Source: memory.EvidenceSystem1,
			At: now.Add(time.Duration(i) * time.Minute)}); err != nil {
			t.Fatal(err)
		}
	}
	if ev, err := s.RecentEvidence(ctx, name, 1); err != nil || len(ev) != 1 || ev[0].Score != 6.5 {
		t.Errorf("evidence = %+v, %v", ev, err)
	}
}

func TestLessons(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	skill := "skill-" + ulid.Make().String()
	if err := s.AddSkill(ctx, memory.Skill{Name: skill, Level: 3, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	w := memory.Work{ID: ulid.Make(), Kind: "lyrics", Title: "Hook", Text: "...", CreatedAt: now}
	if err := s.SaveWork(ctx, w); err != nil {
		t.Fatal(err)
	}
	a := memory.Lesson{ID: ulid.Make(), Skill: skill, Text: "Leave space after the drop", Source: memory.LessonFromOwner, WorkID: w.ID, At: now}
	b := memory.Lesson{ID: ulid.Make(), Skill: skill, Text: "Start on the downbeat", Source: memory.LessonFromSelf, At: now.Add(time.Minute)}
	for _, l := range []memory.Lesson{a, b} {
		if err := s.SaveLesson(ctx, l); err != nil {
			t.Fatal(err)
		}
	}
	if ls, err := s.Lessons(ctx, skill, 10); err != nil || len(ls) != 2 || ls[0].ID != b.ID || ls[1].WorkID != w.ID {
		t.Errorf("lessons = %+v, %v", ls, err)
	}
	if err := s.RetireLesson(ctx, a.ID, now); err != nil {
		t.Fatal(err)
	}
	if ls, _ := s.Lessons(ctx, skill, 10); len(ls) != 1 || ls[0].ID != b.ID {
		t.Errorf("after retiring = %+v", ls)
	}

	if err := s.MarkReviewAsked(ctx, w.ID, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got, ok, err := s.AskedForReview(ctx, now.Add(30*time.Minute)); err != nil || !ok || got.ID != w.ID {
		t.Errorf("asked for review = %+v, %v, %v", got, ok, err)
	}
}

func TestLearningPlans(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	dream := memory.Dream{ID: ulid.Make(), Text: "write songs", Status: memory.DreamActive, Source: memory.DreamFromLife, Since: now}
	if err := s.SaveDream(ctx, dream); err != nil {
		t.Fatal(err)
	}
	goal := memory.Goal{ID: ulid.Make(), DreamID: dream.ID, Text: "hooks", Skill: "lyric writing", Due: now.AddDate(0, 1, 0),
		Status: memory.GoalActive, CreatedAt: now, UpdatedAt: now}
	if err := s.SaveGoal(ctx, goal); err != nil {
		t.Fatal(err)
	}
	if gs, _ := s.Goals(ctx, true); !slices.ContainsFunc(gs, func(g memory.Goal) bool { return g.ID == goal.ID && g.Skill == "lyric writing" }) {
		t.Errorf("goals = %+v", gs)
	}
	plan := memory.LearningPlan{ID: ulid.Make(), GoalID: goal.ID, Skill: "lyric writing", Status: memory.PlanActive, CreatedAt: now, UpdatedAt: now,
		Stages: []memory.Stage{{N: 0, Learn: "hooks", Practice: "a hook", DoneWhen: "two good ones"}, {N: 1, Learn: "verses", Practice: "a verse"}}}
	if err := s.SavePlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	r := memory.Resource{ID: ulid.Make(), PlanID: plan.ID, Stage: 0, Ability: "read_article", Query: "hook (music)", Why: "basics",
		Status: memory.ResourceTodo, AddedAt: now}
	if err := s.SaveResource(ctx, r); err != nil {
		t.Fatal(err)
	}
	w := memory.Work{ID: ulid.Make(), Kind: "reading_notes", Title: "Hooks", Text: "...", CreatedAt: now}
	if err := s.SaveWork(ctx, w); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishResource(ctx, r.ID, w.ID, "Hook (music)", now); err != nil {
		t.Fatal(err)
	}
	if rs, err := s.Resources(ctx, plan.ID); err != nil || len(rs) != 1 || rs[0].Status != memory.ResourceDone || rs[0].WorkID != w.ID {
		t.Errorf("resources = %+v, %v", rs, err)
	}

	mine := func() memory.LearningPlan {
		ps, err := s.Plans(ctx, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range ps {
			if p.ID == plan.ID {
				return p
			}
		}
		t.Fatal("plan not found")
		return memory.LearningPlan{}
	}
	if p := mine(); len(p.Stages) != 2 || p.Stages[0].DoneWhen != "two good ones" || p.Stage != 0 {
		t.Errorf("plan = %+v", p)
	}
	if err := s.AdvancePlan(ctx, plan.ID, now); err != nil {
		t.Fatal(err)
	}
	if p := mine(); p.Stage != 1 || p.Stages[0].DoneAt.IsZero() || p.Status != memory.PlanActive {
		t.Errorf("after one stage = %+v", p)
	}
	if err := s.AdvancePlan(ctx, plan.ID, now); err != nil {
		t.Fatal(err)
	}
	if p := mine(); p.Status != memory.PlanDone {
		t.Errorf("after the last stage = %+v", p)
	}
}

func TestWeb(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond).AddDate(1, 0, 0) // after other tests' searches
	url := "https://example.com/" + ulid.Make().String()
	if err := s.SaveFound(ctx, "hooks", []search.Result{{Title: "Hooks", URL: url, Snippet: "a guide"}}, now); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveFound(ctx, "again", []search.Result{{Title: "Renamed", URL: url}}, now); err != nil { // kept as first found
		t.Fatal(err)
	}
	if r, ok, err := s.Found(ctx, url); err != nil || !ok || r.Title != "Hooks" || r.Snippet != "a guide" {
		t.Errorf("found = %+v, %v, %v", r, ok, err)
	}
	if _, ok, _ := s.Found(ctx, "https://never.example/"); ok {
		t.Error("found a page no search found")
	}
	if rs, err := s.RecentFound(ctx, 1); err != nil || len(rs) != 1 || rs[0].URL != url {
		t.Errorf("recent = %+v, %v", rs, err)
	}
	for _, kind := range []string{"search", "page", "page"} {
		if err := s.LogWeb(ctx, kind, "x", 0.001, now); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := s.WebSince(ctx, "page", now.Add(-time.Minute)); err != nil || n != 2 {
		t.Errorf("pages = %d, %v", n, err)
	}
}
