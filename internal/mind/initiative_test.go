package mind

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"minime/internal/memory"
	"minime/internal/perception"
	"minime/internal/providers/system2"
)

// lagos is the agent's time zone in these tests: UTC+1, so now (13:16 UTC) is 14:16 there.
var lagos = time.FixedZone("Africa/Lagos", 3600)

func TestRemindersAreSavedInTheAgentsZone(t *testing.T) {
	s2 := &fakeSystem2{reminders: []system2.Reminder{
		{When: "2026-10-28T09:00", What: "Sam should call the bank"},
		{When: "2026-10-27T10:00", What: "in the past"},
		{When: "tomorrow", What: "not a time"},
	}}
	m, s := newMind(&fakeReplier{}, &fakeSystem1{choice: "remind", confidence: 0.95}, s2)
	m.Zone = lagos
	m.handle(context.Background(), percept("1", "Remind me tomorrow at 9 to call the bank"))

	if len(s.intentions) != 1 {
		t.Fatalf("intentions = %+v, want only the valid future reminder", s.intentions)
	}
	r := s.intentions[0]
	if !r.DueAt.Equal(time.Date(2026, 10, 28, 8, 0, 0, 0, time.UTC)) || r.Subject != sam || r.Kind != memory.IntentionRemind ||
		r.Status != memory.IntentionPending || r.Text != "Sam should call the bank" {
		t.Errorf("reminder = %+v", r)
	}
	if note := s2.requests[0].Note; !strings.Contains(note, "It is now Tuesday 27 October 2026, 14:16 (Africa/Lagos time).") {
		t.Errorf("note does not give the time: %q", note)
	}
}

func initiativeMind(r *fakeReplier, s1 *fakeSystem1, s2 *fakeSystem2) (*Mind, *fakeStore) {
	m, s := newMind(r, s1, s2)
	m.Zone = lagos
	m.CheckIns = CheckIns{Times: []time.Duration{11 * time.Hour, 18 * time.Hour}, Quiet: 4 * time.Hour}
	s.people[sam] = memory.Person{Key: sam, Name: "Sam", Relation: "Sam is Tobi's friend.", Circle: "friend"}
	s.convs = []memory.Conversation{{Person: sam, Source: "test", Thread: "chat-1", LastActivity: now.Add(-10 * time.Hour)}}
	return m, s
}

func TestDueRemindersAreSent(t *testing.T) {
	r := &fakeReplier{}
	s2 := &fakeSystem2{}
	m, s := initiativeMind(r, &fakeSystem1{}, s2)
	remind := func(text string, due time.Time) memory.Intention {
		return memory.Intention{ID: ulid.Make(), Subject: sam, Kind: memory.IntentionRemind, Text: text, DueAt: due,
			Status: memory.IntentionPending, CreatedAt: now.Add(-24 * time.Hour)}
	}
	s.intentions = []memory.Intention{remind("Sam should call the bank", now.Add(-time.Minute)),
		remind("old", now.Add(-48*time.Hour)), remind("later", now.Add(time.Hour))}

	m.tick(context.Background(), now)

	if len(r.sent) != 1 || r.sent[0] != "a thoughtful reply" {
		t.Fatalf("sent = %v, want one reminder", r.sent)
	}
	if got := []string{s.intentions[0].Status, s.intentions[1].Status, s.intentions[2].Status}; got[0] != memory.IntentionDone ||
		got[1] != memory.IntentionMissed || got[2] != memory.IntentionPending {
		t.Errorf("statuses = %v, want done, missed, pending", got)
	}
	req := s2.requests[0]
	if req.Message != "" || !strings.Contains(req.Note, "Sam should call the bank") || !strings.Contains(req.Note, "It is now") {
		t.Errorf("system 2 request = %+v", req)
	}
	if len(s.percepts) != 1 || s.percepts[0].Kind != perception.KindReminder || s.percepts[0].Thread != "chat-1" {
		t.Errorf("percepts = %+v", s.percepts)
	}
	if len(s.decisions) != 1 || s.decisions[0].Action != "remind" || s.decisions[0].Reply != "a thoughtful reply" ||
		s.decisions[0].PerceptID != s.percepts[0].ID {
		t.Errorf("decisions = %+v", s.decisions)
	}
}

func TestUnsentReminderStaysPending(t *testing.T) {
	m, s := initiativeMind(&fakeReplier{err: errors.New("network down")}, &fakeSystem1{}, &fakeSystem2{})
	s.intentions = []memory.Intention{{ID: ulid.Make(), Subject: sam, Kind: memory.IntentionRemind, Text: "x", DueAt: now,
		Status: memory.IntentionPending}}
	m.tick(context.Background(), now)
	if s.intentions[0].Status != memory.IntentionPending {
		t.Errorf("status = %s, want pending so the next tick retries", s.intentions[0].Status)
	}
}

func TestCheckIns(t *testing.T) {
	interview := memory.Fact{ID: ulid.Make(), Subject: sam, Text: "I have a job interview on Friday", At: now.Add(-48 * time.Hour)}
	age := memory.Fact{ID: ulid.Make(), Subject: sam, Text: "I am 40", At: now.Add(-72 * time.Hour)}
	r := &fakeReplier{}
	s1 := &fakeSystem1{relevant: map[string]bool{"Sam have a job interview on Friday": true}}
	s2 := &fakeSystem2{}
	m, s := initiativeMind(r, s1, s2)
	s.facts = []memory.Fact{interview, age}
	ctx := context.Background()
	at := func(day, hour, minute int) time.Time { return time.Date(2026, 10, day, hour, minute, 0, 0, lagos) }

	m.tick(ctx, at(27, 11, 0)) // the first tick after starting never checks in
	m.tick(ctx, at(27, 11, 1))
	if len(r.sent) != 0 {
		t.Fatalf("checked in without a check-in time passing: %v", r.sent)
	}

	m.lastTick = at(27, 10, 59)
	m.tick(ctx, at(27, 11, 0))
	if len(r.sent) != 1 {
		t.Fatalf("sent = %v, want one check-in at 11:00", r.sent)
	}
	note := s2.requests[0].Note
	if !strings.Contains(note, "Sam have a job interview on Friday") || strings.Contains(note, "40") {
		t.Errorf("note = %q, want only the interview", note)
	}
	if _, ok := s.asked[interview.ID]; !ok {
		t.Error("the interview was not marked as asked")
	}
	if _, ok := s.asked[age.ID]; ok {
		t.Error("a fact that was not asked about was marked")
	}
	if len(s.decisions) != 2 || s.decisions[0].Action != "check_in" || s.decisions[1].Action != "check_in" ||
		s.decisions[1].ParentID == nil || *s.decisions[1].ParentID != s.decisions[0].ID ||
		s.decisions[0].PerceptID != s.percepts[0].ID || s.percepts[0].Kind != perception.KindCheckIn {
		t.Errorf("decisions = %+v", s.decisions)
	}

	// 18:00 the same day: already checked in today.
	s.facts = append(s.facts, memory.Fact{ID: ulid.Make(), Subject: sam, Text: "I have a job interview on Friday", At: now})
	m.tick(ctx, at(27, 18, 0))
	if len(r.sent) != 1 || len(s1.recallCalls) != 1 {
		t.Errorf("checked in twice in one day: sent %v", r.sent)
	}

	// The next day: only the age and the new fact are unasked; the new fact
	// passes, but a busy conversation means no check-in.
	s.convs[0].LastActivity = at(28, 10, 0)
	m.tick(ctx, at(28, 11, 0))
	if len(r.sent) != 1 {
		t.Errorf("checked in on a busy conversation: %v", r.sent)
	}

	// Quiet again, but nothing new worth asking about: recorded without a message.
	s.facts = []memory.Fact{age}
	s.convs[0].LastActivity = at(27, 12, 0)
	m.tick(ctx, at(28, 18, 0))
	last := s.decisions[len(s.decisions)-1]
	if len(r.sent) != 1 || last.Action != "no_check_in" || !last.PerceptID.IsZero() {
		t.Errorf("sent %v, last decision %+v", r.sent, last)
	}
}

func TestStrangersAreNotCheckedIn(t *testing.T) {
	r := &fakeReplier{}
	m, s := initiativeMind(r, &fakeSystem1{relevant: map[string]bool{"Sam have a job interview on Friday": true}}, &fakeSystem2{})
	p := s.people[sam]
	p.Circle = memory.CircleStranger
	s.people[sam] = p
	s.facts = []memory.Fact{{ID: ulid.Make(), Subject: sam, Text: "I have a job interview on Friday", At: now}}
	m.lastTick = now.Add(-time.Hour)
	m.tick(context.Background(), time.Date(2026, 10, 27, 11, 0, 0, 0, lagos))
	if len(r.sent) != 0 {
		t.Errorf("checked in on a stranger: %v", r.sent)
	}
}
