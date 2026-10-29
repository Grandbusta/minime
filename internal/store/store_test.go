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
