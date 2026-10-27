package store

import (
	"context"
	"os"
	"slices"
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
