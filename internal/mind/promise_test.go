package mind

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/Grandbusta/minime/internal/ability"
	"github.com/Grandbusta/minime/internal/ability/journal"
	"github.com/Grandbusta/minime/internal/memory"
	"github.com/Grandbusta/minime/internal/providers/system2"
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
	if verse.Subject != sam || verse.Kind != memory.IntentionTask || !verse.DueAt.Equal(time.Date(2026, 10, 27, 19, 0, 0, 0, time.UTC)) {
		t.Errorf("verse = %+v", verse)
	}
	if read.Ability != "look_up" || read.Args["query"] != "afrobeat" || audio.Ability != "" {
		t.Errorf("abilities: reading %+v, audio %+v", read, audio)
	}

	s2.promises = []system2.Promise{{By: "2026-10-29T10:00", What: "a fourth"}, {By: "2026-10-29T11:00", What: "a fifth"}}
	m.handle(context.Background(), percept("2", "And a poem?"))
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
	m.tick(context.Background(), now)

	if len(s2.requests) != 2 {
		t.Fatalf("system 2 requests = %d, want making it and the message", len(s2.requests))
	}
	if len(r.sent) != 1 || r.sent[0] != "a thoughtful reply\n\nSunday Mornings\n\nBells over Yaba, bread on the stove." {
		t.Errorf("sent = %q", r.sent)
	}
	if w := s.works[len(s.works)-1]; w.IntentionID != i.ID || w.Title != "Sunday Mornings" {
		t.Errorf("work = %+v", w)
	}
	if got := s.intention(i.ID); got.Status != memory.IntentionDone || got.Result != "Sunday Mornings" {
		t.Errorf("intention = %+v", got)
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

func TestAPromiseCanBeMovedToNow(t *testing.T) {
	r := &fakeReplier{}
	s2 := &fakeSystem2{formats: map[string]string{"activity": verseJSON}}
	m, s := promiseMind(r, s2)
	later := promised("Tobi writes Sam a verse about Sunday mornings", "", "", now.Add(20*time.Hour))
	other := promised("Tobi writes Sam a poem", "", "", now.Add(30*time.Hour))
	s.intentions = []memory.Intention{later, other}

	s2.moves = []system2.Move{{Promise: "p1", By: "now"}, {Promise: "p2", By: "2026-10-27T09:00"}, {Promise: "p9", By: "now"}}
	m.handle(context.Background(), percept("1", "Can you send the verse now? And the poem this morning"))
	if note := s2.requests[0].Note; !strings.Contains(note, "[p1] by Wed 28 Oct 10:16: Tobi writes Sam a verse") {
		t.Errorf("note:\n%s", note)
	}
	if got := s.intention(later.ID); !got.DueAt.Equal(now) {
		t.Errorf("verse due %v, want now", got.DueAt)
	}
	if got := s.intention(other.ID); !got.DueAt.Equal(now) {
		t.Errorf("a time already past isn't now: %v", got.DueAt)
	}

	m.tick(context.Background(), now.Add(time.Minute))
	if len(r.sent) < 2 || !strings.Contains(r.sent[len(r.sent)-1]+r.sent[len(r.sent)-2], "Bells over Yaba") {
		t.Errorf("not sent on the next tick: %q", r.sent)
	}
}
