package mind

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/Grandbusta/minime/internal/action"
	"github.com/Grandbusta/minime/internal/memory"
	"github.com/Grandbusta/minime/internal/perception"
	"github.com/Grandbusta/minime/internal/providers/system2"
)

const owner = "test:owner"

type fakeAsker struct {
	asked []string
	ids   []string
}

func (f *fakeAsker) Ask(_ context.Context, p perception.Percept, text, id string) (json.RawMessage, error) {
	if p.Speaker != owner {
		panic("asked someone who isn't the owner: " + p.Speaker)
	}
	f.asked, f.ids = append(f.asked, text), append(f.ids, id)
	return json.RawMessage(`{"message_id":1}`), nil
}

func (f *fakeAsker) Choose(_ context.Context, p perception.Percept, text, id string, n, pick int) (json.RawMessage, error) {
	if p.Speaker != owner {
		panic("asked someone who isn't the owner: " + p.Speaker)
	}
	f.asked, f.ids = append(f.asked, text), append(f.ids, id)
	return json.RawMessage(`{"message_id":2}`), nil
}

func (f *fakeAsker) Settle(context.Context, json.RawMessage, string) error { return nil }

func gatedMind(r *fakeReplier, s2 *fakeSystem2) (*Mind, *fakeStore, *fakeAsker) {
	m, s := promiseMind(r, s2)
	m.Autonomy = action.Policy{Owner: owner, Classes: action.Defaults(), AskExpires: 24 * time.Hour}
	asker := &fakeAsker{}
	m.Asker = asker
	s.people[owner] = memory.Person{Key: owner, Name: "Busta", Circle: "family"}
	s.convs = append(s.convs, memory.Conversation{Person: owner, Source: "test", Thread: "chat-owner", LastActivity: now.Add(-time.Hour)})
	return m, s, asker
}

func tap(from, id string, approve bool) perception.Percept {
	payload, _ := json.Marshal(perception.Answer{Approval: id, Approve: approve})
	return perception.Percept{ID: ulid.Make(), Source: "test", ExternalID: "callback:" + ulid.Make().String(), Thread: "chat-owner",
		Speaker: from, Kind: perception.KindApproval, Payload: payload, At: now}
}

func reminder(subject string) memory.Intention {
	return memory.Intention{ID: ulid.Make(), Subject: subject, Kind: memory.IntentionRemind, Text: "call the bank",
		DueAt: now.Add(-time.Minute), Status: memory.IntentionPending, CreatedAt: now.Add(-time.Hour)}
}

func TestMessagingOthersFirstWaitsForTheOwner(t *testing.T) {
	r := &fakeReplier{}
	m, s, asker := gatedMind(r, &fakeSystem2{})
	i := reminder(sam)
	s.intentions = []memory.Intention{i}
	m.tick(context.Background(), now)

	if len(r.sent) != 0 {
		t.Fatalf("sent before the owner approved: %q", r.sent)
	}
	if got := s.intention(i.ID); got.Status != memory.IntentionAwaiting {
		t.Errorf("intention = %+v, want awaiting", got)
	}
	if len(asker.asked) != 1 || s.approvals[0].Class != "reach_out" || s.approvals[0].Person != sam {
		t.Fatalf("asked %q, approvals %+v", asker.asked, s.approvals)
	}

	m.handle(context.Background(), tap(sam, asker.ids[0], true))
	if len(r.sent) != 0 {
		t.Fatal("someone who isn't the owner approved it")
	}
	m.handle(context.Background(), tap(owner, asker.ids[0], true))
	m.handle(context.Background(), tap(owner, asker.ids[0], false))
	if len(r.sent) != 1 || r.sent[0] != "a thoughtful reply" {
		t.Fatalf("sent = %q, want the approved text, once", r.sent)
	}
	if got := s.intention(i.ID); got.Status != memory.IntentionDone {
		t.Errorf("intention = %+v", got)
	}
}

func TestDeniedOrUnansweredMessagesAreNotSent(t *testing.T) {
	r := &fakeReplier{}
	m, s, asker := gatedMind(r, &fakeSystem2{})
	denied, old := reminder(sam), reminder(sam)
	old.Status = memory.IntentionAwaiting
	s.intentions = []memory.Intention{denied, old}
	s.approvals = []memory.Approval{{ID: ulid.Make(), Class: "reach_out", Person: sam, What: "message Sam first", Text: "hey",
		Kind: perception.KindReminder, Ref: old.ID, Status: memory.ApprovalPending, AskedAt: now.Add(-25 * time.Hour),
		SentAt: now.Add(-25 * time.Hour), Message: json.RawMessage(`{"message_id":2}`)}}
	m.tick(context.Background(), now)

	if got := s.intention(old.ID); got.Status != memory.IntentionMissed || got.Result != "no answer from the owner" {
		t.Errorf("expired: %+v", got)
	}
	m.handle(context.Background(), tap(owner, asker.ids[0], false))
	if got := s.intention(denied.ID); got.Status != memory.IntentionMissed || got.Result != "the owner said no" {
		t.Errorf("denied: %+v", got)
	}
	if len(r.sent) != 0 {
		t.Errorf("sent = %q", r.sent)
	}
}

func TestTheOwnerNeedNotApproveWhatIsForThem(t *testing.T) {
	r := &fakeReplier{}
	m, s, asker := gatedMind(r, &fakeSystem2{})
	s.intentions = []memory.Intention{reminder(owner)}
	m.tick(context.Background(), now)
	if len(r.sent) != 1 || len(asker.asked) != 0 {
		t.Errorf("sent %q, asked %q", r.sent, asker.asked)
	}
}

func TestForbiddenMessagesAreNotEvenWritten(t *testing.T) {
	s2 := &fakeSystem2{}
	m, s, _ := gatedMind(&fakeReplier{}, s2)
	m.Autonomy.Classes[action.ReachOut] = action.Forbid
	i := reminder(sam)
	s.intentions = []memory.Intention{i}
	m.tick(context.Background(), now)
	if got := s.intention(i.ID); got.Status != memory.IntentionMissed || len(s2.requests) != 0 {
		t.Errorf("intention %+v, %d requests", got, len(s2.requests))
	}
}

func TestPromisesToOthersWaitForTheOwner(t *testing.T) {
	r := &fakeReplier{}
	s2 := &fakeSystem2{promises: []system2.Promise{{By: "2026-10-27T20:00", What: "Tobi writes Sam a verse"}}}
	m, s, asker := gatedMind(r, s2)
	m.handle(context.Background(), percept("1", "Write me a verse tonight?"))

	if len(s.intentions) != 1 || s.intentions[0].Status != memory.IntentionAwaiting {
		t.Fatalf("intentions = %+v", s.intentions)
	}
	i := s.intentions[0]
	if len(asker.asked) != 1 {
		t.Fatalf("asked %q", asker.asked)
	}

	sentBefore := len(r.sent)
	m.handle(context.Background(), tap(owner, asker.ids[0], true))
	if got := s.intention(i.ID); got.Status != memory.IntentionPending {
		t.Errorf("approved promise = %+v", got)
	}
	if len(r.sent) != sentBefore+1 || len(asker.asked) != 1 {
		t.Errorf("follow-up: sent %d, asked %d; want one message to Sam and no new request", len(r.sent)-sentBefore, len(asker.asked))
	}
}

func TestADeniedPromiseIsDroppedKindly(t *testing.T) {
	r := &fakeReplier{}
	s2 := &fakeSystem2{promises: []system2.Promise{{By: "2026-10-27T20:00", What: "Tobi writes Sam a verse"}}}
	m, s, asker := gatedMind(r, s2)
	m.handle(context.Background(), percept("1", "Write me a verse tonight?"))
	m.handle(context.Background(), tap(owner, asker.ids[0], false))
	if got := s.intentions[0]; got.Status != memory.IntentionCancelled {
		t.Errorf("denied promise = %+v", got)
	}
}

func TestPublishingIsDecidedByTheOwner(t *testing.T) {
	m, s, asker := gatedMind(&fakeReplier{}, &fakeSystem2{})
	work := ulid.Make()
	s.approvals = []memory.Approval{
		{ID: ulid.Make(), Class: "publish", What: "Lagos Rain (lyrics)", Ref: work, Status: memory.ApprovalPending, AskedAt: now}}
	m.tick(context.Background(), now) // sends what the profile writer saved
	if len(asker.asked) != 1 {
		t.Fatalf("asked %q", asker.asked)
	}
	m.handle(context.Background(), tap(owner, asker.ids[0], true))
	if s.public[work] != "true by owner" {
		t.Errorf("work = %q", s.public[work])
	}
}

func TestReadingTheWebCanBeForbidden(t *testing.T) {
	look := &fakeAbility{name: "look_up", describe: "look it up", found: map[string]string{"x": "y"}}
	m, s, _ := abilityMind(look)
	m.Autonomy = action.Policy{Owner: owner, Classes: map[action.Class]action.Verdict{action.ReadWeb: action.Forbid}}
	if len(m.plannable()) != 0 {
		t.Errorf("plannable = %v", m.plannable())
	}
	s.intentions = []memory.Intention{activity("Look x up", "look_up", "x")}
	m.tick(context.Background(), at(29, 10, 0))
	if got := s.intentions[0]; got.Status != memory.IntentionMissed || len(look.acts) != 0 {
		t.Errorf("intention %+v, %d acts", got, len(look.acts))
	}
}
