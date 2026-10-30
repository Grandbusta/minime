package mind

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"minime/internal/ability"
	"minime/internal/ability/journal"
	"minime/internal/memory"
	"minime/internal/perception"
	"minime/internal/providers/system2"
)

func promiseMind(r *fakeReplier, s2 *fakeSystem2) (*Mind, *fakeStore) {
	m, s := initiativeMind(r, &fakeSystem1{choice: "other", confidence: 0.95}, s2)
	look := &fakeAbility{name: "look_up", describe: "look it up", found: map[string]string{"afrobeat": "Afrobeat began in Lagos."}}
	m.Abilities = ability.NewSet(journal.Journal{Store: s}, look)
	m.Day = Day{ActivityPrompt: Prompt{Ref: "activity@test", Text: "You are {name}, making something."}}
	return m, s
}

func TestRepliesKeepTheirPromises(t *testing.T) {
	s2 := &fakeSystem2{promises: []system2.Promise{
		{By: "2026-10-27T20:00", What: "Tobi writes Sam a verse about Sunday mornings"},
		{By: "2026-10-28T10:00", What: "Tobi reads up on afrobeat for Sam", Ability: "look_up", Query: "afrobeat"},
		{By: "2026-10-27T09:00", What: "in the past"},
		{By: "tonight", What: "not a time"},
		{By: "2026-10-28T12:00", What: "Tobi records a song for Sam", Ability: "record_audio"},
	}}
	m, s := promiseMind(&fakeReplier{}, s2)
	m.handle(context.Background(), percept("1", "Can you write me a verse tonight, and read up on afrobeat for me?"))

	if len(s.intentions) != 3 {
		t.Fatalf("intentions = %+v, want the three in the future", s.intentions)
	}
	verse, read, audio := s.intentions[0], s.intentions[1], s.intentions[2]
	if verse.Subject != sam || verse.Kind != memory.IntentionTask || !verse.DueAt.Equal(time.Date(2026, 10, 27, 19, 0, 0, 0, time.UTC)) ||
		verse.Ability != "" || verse.PerceptID.IsZero() {
		t.Errorf("verse = %+v", verse)
	}
	if read.Ability != "look_up" || read.Args["query"] != "afrobeat" {
		t.Errorf("reading = %+v", read)
	}
	if audio.Ability != "" {
		t.Errorf("kept an ability it doesn't have: %+v", audio)
	}

	// The next reply knows what it promised, and no more are taken on past the limit.
	s2.promises = []system2.Promise{{By: "2026-10-29T10:00", What: "a fourth"}, {By: "2026-10-29T11:00", What: "a fifth"}}
	m.handle(context.Background(), percept("2", "And a poem?"))
	if note := s2.requests[len(s2.requests)-1].Note; !strings.Contains(note, "What you've promised Sam and not sent yet") ||
		!strings.Contains(note, "by Tue 27 Oct 20:00: Tobi writes Sam a verse about Sunday mornings") {
		t.Errorf("note:\n%s", note)
	}
	if len(s.intentions) != 3 {
		t.Errorf("took on more than %d promises: %d", maxOpenPromises, len(s.intentions))
	}
}

func TestNoPromisesToStrangers(t *testing.T) {
	s2 := &fakeSystem2{promises: []system2.Promise{{By: "2026-10-27T20:00", What: "Tobi writes a verse"}}}
	m, s := promiseMind(&fakeReplier{}, s2)
	s.people[sam] = memory.Person{Key: sam, Name: "Sam", Circle: memory.CircleStranger}
	m.handle(context.Background(), percept("1", "Write me a verse"))
	if len(s.intentions) != 0 || !strings.Contains(s2.requests[0].Note, "don't promise to make them anything") {
		t.Errorf("intentions %+v; note:\n%s", s.intentions, s2.requests[0].Note)
	}
}

const verseJSON = `{"title":"Sunday Mornings","kind":"writing","text":"Bells over Yaba, bread on the stove.",
	"project_notes":"","project_done":false,"remember":["Tobi wrote Sam a verse about Sunday mornings"]}`

func promised(text, abilityName, query string, due time.Time) memory.Intention {
	i := memory.Intention{ID: ulid.Make(), Subject: sam, Kind: memory.IntentionTask, Text: text, Ability: abilityName,
		DueAt: due, Status: memory.IntentionPending, CreatedAt: now.Add(-6 * time.Hour)}
	if query != "" {
		i.Args = map[string]string{"query": query}
	}
	return i
}

func TestADuePromiseIsMadeAndSent(t *testing.T) {
	r := &fakeReplier{}
	s2 := &fakeSystem2{formats: map[string]string{"activity": verseJSON}}
	m, s := promiseMind(r, s2)
	s.convs[0].LastActivity = now.Add(-time.Minute) // mid-conversation: a promise doesn't wait
	s.spent = 1                                     // over the day's budget: a promise is still kept
	m.Day.BudgetUSD = 0.05
	i := promised("Tobi writes Sam a verse about Sunday mornings", "", "", now.Add(-time.Minute))
	s.intentions = []memory.Intention{i}
	s.plans = []memory.LearningPlan{{ID: ulid.Make(), Skill: "lyric writing", Status: memory.PlanActive,
		Stages: []memory.Stage{{Learn: "hooks", Practice: "a hook", DoneWhen: "a hook"}}}}
	s.exchanges = []memory.Exchange{
		{At: i.CreatedAt.Add(-5 * time.Second), Message: "Write me a verse about Sunday mornings tonight?", Reply: "Sure, by 8."},
		{At: i.CreatedAt.Add(time.Minute), Message: "Make it funny", Reply: "Funny it is."},
		{At: i.CreatedAt.Add(2 * time.Hour), Message: "Unrelated", Reply: "Ok."},
	}
	m.tick(context.Background(), now)

	if len(s2.requests) != 2 {
		t.Fatalf("system 2 requests = %d, want making it and the message", len(s2.requests))
	}
	if note := s2.requests[0].Note; !strings.Contains(note, "You promised Sam, on Tuesday 27 October at 08:16, to make this for them by Tuesday 14:15: Tobi writes Sam a verse") {
		t.Errorf("making note:\n%s", note)
	}
	if note := s2.requests[0].Note; !strings.Contains(note, "\nSam: Make it funny\nTobi: Funny it is.") || strings.Contains(note, "Unrelated") {
		t.Errorf("making note doesn't carry what was said about it:\n%s", note)
	}
	if strings.Contains(s2.requests[0].Note, "Where your learning plans are") {
		t.Error("what it makes for someone else was offered as practice for its own plan")
	}
	if note := s2.requests[1].Note; !strings.Contains(note, "word for word:\n\nSunday Mornings\n\nBells over Yaba") {
		t.Errorf("message note:\n%s", note)
	}
	if len(r.sent) != 1 || r.sent[0] != "a thoughtful reply\n\nSunday Mornings\n\nBells over Yaba, bread on the stove." {
		t.Errorf("sent = %q", r.sent)
	}
	w := s.works[len(s.works)-1]
	if w.IntentionID != i.ID || w.Title != "Sunday Mornings" {
		t.Errorf("work = %+v", w)
	}
	if got := s.intention(i.ID); got.Status != memory.IntentionDone || got.Result != "Sunday Mornings" {
		t.Errorf("intention = %+v", got)
	}
	if len(s.facts) != 1 || s.facts[0].Text != "Tobi wrote Sam a verse about Sunday mornings" {
		t.Errorf("facts = %+v", s.facts)
	}
	var kinds []string
	for _, p := range s.percepts {
		kinds = append(kinds, p.Kind)
	}
	if strings.Join(kinds, ",") != "task,"+perception.KindDelivery {
		t.Errorf("percepts = %v", kinds)
	}
	last := s.decisions[len(s.decisions)-1]
	if last.Action != "deliver" || !strings.Contains(last.Reply, "Bells over Yaba") {
		t.Errorf("decision = %+v", last)
	}
}

func TestAPromiseMadeButNotSentIsSentLaterWithoutRemaking(t *testing.T) {
	r := &fakeReplier{err: errors.New("telegram down")}
	s2 := &fakeSystem2{formats: map[string]string{"activity": verseJSON}}
	m, s := promiseMind(r, s2)
	i := promised("Tobi writes Sam a verse", "", "", now.Add(-time.Minute))
	s.intentions = []memory.Intention{i}
	m.tick(context.Background(), now)
	if got := s.intention(i.ID); got.Status != memory.IntentionPending || len(s.works) != 1 {
		t.Fatalf("after a failed send: %+v, %d works", got, len(s.works))
	}

	r.err = nil
	m.tick(context.Background(), now.Add(time.Minute))
	if got := s.intention(i.ID); got.Status != memory.IntentionDone || len(s.works) != 1 {
		t.Errorf("after the retry: %+v, %d works (made again?)", got, len(s.works))
	}
	if len(s2.requests) != 3 { // made once, written twice
		t.Errorf("system 2 requests = %d", len(s2.requests))
	}
}

func TestAPromiseUsesItsAbility(t *testing.T) {
	r := &fakeReplier{}
	s2 := &fakeSystem2{formats: map[string]string{"activity": verseJSON}}
	m, s := promiseMind(r, s2)
	s.intentions = []memory.Intention{promised("Tobi reads up on afrobeat for Sam", "look_up", "afrobeat", now.Add(-time.Minute))}
	m.tick(context.Background(), now)
	if note := s2.requests[0].Note; !strings.Contains(note, "Afrobeat began in Lagos.") {
		t.Errorf("making note:\n%s", note)
	}
	if len(r.sent) != 1 {
		t.Errorf("sent = %q", r.sent)
	}
}

func TestPromisesTooLateOrToNobodyAreMissed(t *testing.T) {
	r := &fakeReplier{}
	s2 := &fakeSystem2{formats: map[string]string{"activity": verseJSON}}
	m, s := promiseMind(r, s2)
	late := promised("too late", "", "", now.Add(-25*time.Hour))
	nobody := promised("to nobody", "", "", now.Add(-time.Minute))
	nobody.Subject = "telegram:999"
	s.intentions = []memory.Intention{late, nobody}
	m.tick(context.Background(), now)
	for _, i := range []memory.Intention{late, nobody} {
		if got := s.intention(i.ID); got.Status != memory.IntentionMissed {
			t.Errorf("%s: %+v", i.Text, got)
		}
	}
	if len(s2.requests) != 0 || len(r.sent) != 0 {
		t.Errorf("did something anyway: %d requests, %d sent", len(s2.requests), len(r.sent))
	}
}

func TestShippedReplyPromptIsFilledIn(t *testing.T) {
	raw, err := os.ReadFile("../../prompts/reply.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, life := range shippedLives(t) {
		m, s := promiseMind(&fakeReplier{}, &fakeSystem2{})
		m.Life, m.ReplyPrompt = life, Prompt{Text: string(raw)}
		got := m.instructions(context.Background(), s.people[sam], names{agent: life.Name, speaker: "Sam"})
		if i := strings.IndexAny(got, "{}"); i >= 0 {
			t.Errorf("%s: placeholder left at %q", life.Name, got[max(0, i-30):min(len(got), i+30)])
		}
		for _, want := range []string{"- " + life.Makes[0].Kind + ": ", "- look_up: look it up"} {
			if !strings.Contains(got, want) {
				t.Errorf("%s: reply prompt is missing %q", life.Name, want)
			}
		}
	}
}

func TestPromisesAreMadeAheadAndSentOnTime(t *testing.T) {
	r := &fakeReplier{}
	s2 := &fakeSystem2{formats: map[string]string{"activity": verseJSON}}
	m, s := promiseMind(r, s2)
	// "In five minutes", promised a minute ago; and one due in 20 minutes, promised this morning.
	soon := promised("Tobi writes Sam a verse", "", "", now.Add(4*time.Minute))
	soon.CreatedAt = now.Add(-time.Minute)
	later := promised("Tobi writes Sam a poem", "", "", now.Add(20*time.Minute))
	tomorrow := promised("Tobi writes Sam a song", "", "", now.Add(24*time.Hour))
	s.intentions = []memory.Intention{soon, later, tomorrow}
	made := func(i memory.Intention) bool { _, ok, _ := s.IntentionWork(context.Background(), i.ID); return ok }

	m.tick(context.Background(), now)
	if made(soon) || !made(later) || made(tomorrow) || len(r.sent) != 0 {
		t.Fatalf("at first: soon made %v, later made %v, tomorrow made %v, sent %d; want only the settled one made, nothing sent",
			made(soon), made(later), made(tomorrow), len(r.sent))
	}
	m.tick(context.Background(), now.Add(3*time.Minute)) // a minute before it's due: made, not sent
	if !made(soon) || len(r.sent) != 0 || s.intention(soon.ID).Status != memory.IntentionPending {
		t.Fatalf("a minute before: made %v, sent %d", made(soon), len(r.sent))
	}
	requests := len(s2.requests)
	m.tick(context.Background(), now.Add(4*time.Minute)) // due: only the message is written
	if len(r.sent) != 1 || !strings.Contains(r.sent[0], "Bells over Yaba") || len(s2.requests) != requests+1 ||
		s.intention(soon.ID).Status != memory.IntentionDone {
		t.Errorf("when due: sent %q, %d more requests, status %s", r.sent, len(s2.requests)-requests, s.intention(soon.ID).Status)
	}
	if len(s.works) != 2 {
		t.Errorf("works = %d, want each made once", len(s.works))
	}
}

func TestDeliveryWordsAreKeptShort(t *testing.T) {
	for in, want := range map[string]string{
		"Here's that verse I promised 😄":                                          "Here's that verse I promised 😄",
		"Here's that verse I promised 😄\n\nOne Small Move\nLagos traffic said...": "Here's that verse I promised 😄",
		"  \n\nHere it is.  ": "Here it is.",
	} {
		if got := firstParagraph(in); got != want {
			t.Errorf("firstParagraph(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTitledSendsTheTitleOnce(t *testing.T) {
	for _, c := range []struct{ title, text, want string }{
		{"Sunday Mornings", "Bells over Yaba.", "Sunday Mornings\n\nBells over Yaba."},
		{"NEPA Took the Light", "NEPA Took the Light\n\nNEPA took the light...", "NEPA Took the Light\n\nNEPA took the light..."},
		{"NEPA Took the Light", "**nepa took the light**\nNEPA took the light...", "**nepa took the light**\nNEPA took the light..."},
	} {
		if got := titled(memory.Work{Title: c.title, Text: c.text}); got != c.want {
			t.Errorf("titled(%q, %q) = %q, want %q", c.title, c.text, got, c.want)
		}
	}
}

func TestPromisesNoteSaysNotToMakeItNow(t *testing.T) {
	s2 := &fakeSystem2{}
	m, s := promiseMind(&fakeReplier{}, s2)
	s.intentions = []memory.Intention{promised("Tobi writes Sam a poem", "", "", now.Add(time.Hour))}
	m.handle(context.Background(), percept("1", "Make it about my generator too"))
	if note := s2.requests[0].Note; !strings.Contains(note, "If they add to one, just say you'll work it in; don't make it now") {
		t.Errorf("note:\n%s", note)
	}
}
