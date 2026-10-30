package mind

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/Grandbusta/minime/internal/ability"
	"github.com/Grandbusta/minime/internal/ability/journal"
	"github.com/Grandbusta/minime/internal/memory"
	"github.com/Grandbusta/minime/internal/perception"
)

func callsMind(s2 *fakeSystem2) (*Mind, *fakeStore, *fakeAsker, *fakeReplier) {
	r := &fakeReplier{}
	m, s, asker := gatedMind(r, s2)
	m.Zone = lagos
	m.Day = Day{PlanAt: 8 * time.Hour, JournalAt: 22 * time.Hour, MaxActivities: 3, BudgetUSD: 0.05,
		PlanPrompt:     Prompt{Ref: "plan@test", Text: "You are {name}, planning for {owner}."},
		ActivityPrompt: Prompt{Ref: "activity@test", Text: "You are {name}, doing something."},
		ReviewDay:      time.Sunday, ReviewAt: 20 * time.Hour, ReviewPrompt: Prompt{Ref: "review@test", Text: "You are {name}, reviewing."}}
	m.Abilities = ability.NewSet(journal.Journal{Store: s})
	m.Calls = Calls{On: true, AsksPerDay: 1, ReplyWindow: 30 * time.Minute}
	return m, s, asker, r
}

func ownerSays(text string) perception.Percept {
	return perception.Percept{ID: ulid.Make(), Source: "test", ExternalID: ulid.Make().String(), Thread: "chat-owner", Speaker: owner,
		SpeakerName: "Busta", Kind: "message", Text: text, At: time.Now()}
}

func ownerTaps(call ulid.ULID, option int) perception.Percept {
	payload, _ := json.Marshal(perception.Pick{Call: call.String(), Option: option})
	return perception.Percept{ID: ulid.Make(), Source: "test", ExternalID: ulid.Make().String(), Thread: "chat-owner", Speaker: owner,
		Kind: perception.KindCall, Payload: payload, At: time.Now()}
}

func TestDayPlanCalls(t *testing.T) {
	ctx := context.Background()
	s2 := &fakeSystem2{formats: map[string]string{"day_plan": `{"activities":[
		{"at":"10:00","what":"Work on the motif","project_id":"","new_project_title":"Motif","project_kind":"music","ability":"","query":"","goal_id":"","resource_id":""}],
		"calls":[
		{"type":"focus","situation":"What to focus on today","options":["The motif for Lagos After Rain","Equiano Chapter I","A new hook"],"pick":0,"why":"The stage-1 practice is due","confidence":0.7},
		{"type":"start","situation":"Starting a new hook or going on","options":["Start a new hook","Go on with the motif"],"pick":1,"why":"Finish first","confidence":0.4},
		{"type":"focus","situation":"Only one option","options":["just this"],"pick":0,"why":"","confidence":1},
		{"type":"publish","situation":"Not a plan's call","options":["a","b"],"pick":0,"why":"","confidence":1}]}`}}
	m, s, asker, r := callsMind(s2)

	m.planDay(ctx, at(29, 8, 1))
	if len(s.calls) != 2 || s.calls[0].Type != memory.CallFocus || s.calls[0].Mode != memory.CallAfter || s.calls[0].Status != memory.CallLogged {
		t.Fatalf("calls = %+v", s.calls)
	}

	// One ask a day: the one it's least sure of.
	m.sendCalls(ctx, at(29, 8, 2))
	m.sendCalls(ctx, at(29, 9, 0))
	if len(asker.asked) != 1 || asker.ids[0] != s.calls[1].ID.String() {
		t.Fatalf("asked %q (%v); want only the start call", asker.asked, asker.ids)
	}

	// The owner's reason is theirs, not a message to answer.
	start := s.calls[1]
	m.handle(ctx, ownerTaps(start.ID, 0))
	got := s.callAt(start.ID)
	if got.Status != memory.CallAnswered || got.Agreed == nil || *got.Agreed || got.Waiting != memory.CallWaitingReason || got.CaseID.IsZero() {
		t.Fatalf("call after the tap = %+v", got)
	}
	m.handle(ctx, ownerTaps(start.ID, 1))
	if *s.callAt(start.ID).Agreed {
		t.Error("a second tap changed the answer")
	}
	asked := len(s2.requests)
	m.handle(ctx, ownerSays("A new hook keeps the ideas fresh while the motif rests"))
	if got := s.callAt(start.ID); got.OwnerReason != "A new hook keeps the ideas fresh while the motif rests" || got.Waiting != "" {
		t.Errorf("call after the reason = %+v", got)
	}
	if len(s2.requests) != asked || len(r.sent) != 1 || r.sent[0] != "Thanks, that helps. I'll remember it." {
		t.Errorf("replies = %q, System 2 asked %d more times", r.sent, len(s2.requests)-asked)
	}
	var kept *memory.JudgmentCase
	for i := range s.cases {
		if s.cases[i].ID == got.CaseID {
			kept = &s.cases[i]
		}
	}
	if kept == nil || kept.Choice != "Start a new hook" || kept.Reason == "" || kept.Status != memory.JudgmentApproved || kept.Type != "life" {
		t.Errorf("case = %+v", kept)
	}

	m.handle(ctx, ownerSays("How's the motif going?"))
	if len(s2.requests) == asked && len(r.sent) == 1 {
		t.Error("a later message was taken as a reason too")
	}
}

func TestGoalCalls(t *testing.T) {
	ctx := context.Background()
	dream := memory.Dream{ID: ulid.Make(), Text: "Tobi wants to be a producer", Status: memory.DreamActive}
	old := memory.Goal{ID: ulid.Make(), DreamID: dream.ID, Text: "three beat ideas", Status: memory.GoalActive, Due: at(30, 0, 0)}
	review := `{"title":"A week","text":"Fine.","goals":[{"goal_id":"` + old.ID.String() + `","progress":"not for me","status":"dropped","skill":"","confidence":0.6}],
		"new_goals":[{"dream_id":"` + dream.ID.String() + `","text":"Five beat ideas for an EP","done_when":"five in the EP","due":"2026-11-30","project_ids":[],"skill":"","why":"an EP is next","confidence":0.8},
			{"dream_id":"` + dream.ID.String() + `","text":"A hook a day","done_when":"seven hooks","due":"2026-11-20","project_ids":[],"skill":"","why":"practice","confidence":0.5}],
		"projects":[],"dreams":[],"retire_lessons":[],"new_lessons":[],"plans":[],"new_resources":[],"principles":[]}`
	s2 := &fakeSystem2{formats: map[string]string{"weekly_review": review}}
	m, s, asker, _ := callsMind(s2)
	s.dreams, s.goals = []memory.Dream{dream}, []memory.Goal{old}
	if err := m.Review(ctx, at(26, 20, 1)); err != nil {
		t.Fatal(err)
	}
	if len(s.calls) != 3 {
		t.Fatalf("calls = %+v", s.calls)
	}
	goal := func(id ulid.ULID) memory.Goal {
		for _, g := range s.goals {
			if g.ID == id {
				return g
			}
		}
		t.Fatalf("no goal %s", id)
		return memory.Goal{}
	}
	if goal(old.ID).Status != memory.GoalActive {
		t.Error("dropped a goal before its owner answered")
	}
	if g := goal(s.calls[1].Ref); g.Status != memory.GoalProposed || s.calls[1].Type != memory.CallNewGoal || s.calls[1].Mode != memory.CallBefore {
		t.Errorf("new goal = %+v, call %+v", g, s.calls[1])
	}

	m.sendCalls(ctx, time.Now()) // goal calls are always asked
	if len(asker.asked) != 3 {
		t.Fatalf("asked %q", asker.asked)
	}
	m.handle(ctx, ownerTaps(s.calls[0].ID, 1)) // keep the old goal
	m.handle(ctx, ownerTaps(s.calls[1].ID, 0)) // set the EP goal
	if goal(old.ID).Status != memory.GoalActive || goal(s.calls[1].Ref).Status != memory.GoalActive {
		t.Errorf("after the answers: old %s, EP %s", goal(old.ID).Status, goal(s.calls[1].Ref).Status)
	}
	// Unanswered: it goes with its pick, and it isn't counted.
	m.expireCalls(ctx, time.Now().Add(25*time.Hour))
	if c := s.callAt(s.calls[2].ID); c.Status != memory.CallExpired || c.Agreed != nil || goal(c.Ref).Status != memory.GoalActive {
		t.Errorf("expired call = %+v, goal %s", c, goal(c.Ref).Status)
	}
}
