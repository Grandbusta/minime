package mind

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"minime/internal/ability"
	"minime/internal/ability/journal"
	"minime/internal/action"
	"minime/internal/memory"
	"minime/internal/perception"
)

// callsMind is a mind that makes calls, with an owner to ask.
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
	if !strings.Contains(s2.requests[0].Instructions, "planning for Busta") {
		t.Errorf("instructions don't name the owner: %q", s2.requests[0].Instructions)
	}

	// One ask a day: the one it's least sure of.
	m.sendCalls(ctx, at(29, 8, 2))
	m.sendCalls(ctx, at(29, 9, 0))
	if len(asker.asked) != 1 || asker.ids[0] != s.calls[1].ID.String() {
		t.Fatalf("asked %q (%v); want only the start call", asker.asked, asker.ids)
	}
	for _, want := range []string{"Starting a new hook or going on.", "1. Start a new hook\n2. Go on with the motif ★",
		"I went with 2 because finish first (40% sure you'd pick it too). What would you do?"} {
		if !strings.Contains(asker.asked[0], want) {
			t.Errorf("ask is missing %q:\n%s", want, asker.asked[0])
		}
	}
	if strings.Contains(asker.asked[0], "—") {
		t.Error("an em dash in the ask")
	}

	// The owner picks differently, then says why: the reason is theirs, not a message to answer.
	start := s.calls[1]
	m.handle(ctx, ownerTaps(start.ID, 0))
	got := s.callAt(start.ID)
	if got.Status != memory.CallAnswered || got.Agreed == nil || *got.Agreed || got.Waiting != memory.CallWaitingReason || got.CaseID.IsZero() {
		t.Fatalf("call after the tap = %+v", got)
	}
	if last := asker.settled[len(asker.settled)-1]; !strings.HasSuffix(last, "You'd pick 1. What tipped it?") {
		t.Errorf("settled = %q", last)
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

	// The next message is a message again.
	m.handle(ctx, ownerSays("How's the motif going?"))
	if len(s2.requests) == asked && len(r.sent) == 1 {
		t.Error("a later message was taken as a reason too")
	}
}

func TestSomethingElseAndAgreeing(t *testing.T) {
	ctx := context.Background()
	m, s, asker, r := callsMind(&fakeSystem2{})
	m.Calls.AsksPerDay = 3
	now := time.Now()
	for i := range 2 {
		c, _ := newCall(memory.CallFocus, memory.CallAfter, "What to focus on today", []string{"a", "b"}, 0, "", 0.5, now)
		c.Situation += string(rune('A' + i))
		s.calls = append(s.calls, c)
	}
	m.sendCalls(ctx, now)
	if len(asker.asked) != 2 {
		t.Fatalf("asked %d", len(asker.asked))
	}
	m.handle(ctx, ownerTaps(s.calls[0].ID, 0))
	if c := s.callAt(s.calls[0].ID); c.Agreed == nil || !*c.Agreed || c.Waiting != "" {
		t.Errorf("agreed call = %+v", c)
	}
	m.handle(ctx, ownerTaps(s.calls[0].ID, 1)) // a second tap changes nothing
	if c := s.callAt(s.calls[0].ID); !*c.Agreed {
		t.Error("a second tap changed the answer")
	}
	m.handle(ctx, ownerTaps(s.calls[1].ID, -1))
	if c := s.callAt(s.calls[1].ID); c.Status != memory.CallAsked || c.Waiting != memory.CallWaitingAnswer {
		t.Fatalf("something else = %+v", c)
	}
	m.handle(ctx, ownerSays("Rest today; you worked all week"))
	c := s.callAt(s.calls[1].ID)
	if c.Status != memory.CallAnswered || c.OwnerPick != -1 || c.OwnerChoice != "Rest today; you worked all week" || *c.Agreed {
		t.Errorf("their own answer = %+v", c)
	}
	if len(r.sent) != 1 || !strings.HasPrefix(r.sent[0], "Got it") {
		t.Errorf("replies = %q", r.sent)
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
	if len(asker.asked) != 3 || !strings.Contains(asker.asked[1], "1. Set it ★\n2. Not now") || !strings.Contains(asker.asked[1], "I'll go with mine.") {
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
	if last := asker.settled[len(asker.settled)-1]; !strings.HasSuffix(last, "No answer, so I went with mine.") {
		t.Errorf("settled = %q", last)
	}
}

func TestApprovalsCountAsCalls(t *testing.T) {
	ctx := context.Background()
	m, s, _, _ := callsMind(&fakeSystem2{})
	a := memory.Approval{ID: ulid.Make(), Class: string(action.Publish), What: "a poem", Status: memory.ApprovalPending, AskedAt: time.Now()}
	s.approvals = append(s.approvals, a)
	m.decide(ctx, a, memory.ApprovalDenied, ulid.ULID{})
	if len(s.calls) != 1 || s.calls[0].Type != "publish" || s.calls[0].Agreed == nil || *s.calls[0].Agreed ||
		s.calls[0].AnsweredBy != memory.AnsweredByApproval || len(s.cases) != 0 {
		t.Errorf("calls = %+v, cases %d", s.calls, len(s.cases))
	}
}

func TestReviewDrawsPrinciples(t *testing.T) {
	ctx := context.Background()
	m, s, _, _ := callsMind(&fakeSystem2{})
	agreed := false
	c := memory.Call{ID: ulid.Make(), Type: memory.CallFocus, Situation: "What to focus on today", Options: []string{"the motif", "rest"}, Pick: 0,
		Status: memory.CallAnswered, OwnerPick: 1, OwnerReason: "you worked all week", Agreed: &agreed, AnsweredBy: memory.AnsweredOnTelegram,
		AnsweredAt: at(25, 9, 0), CaseID: ulid.Make()}
	s.calls = append(s.calls, c)
	review := `{"title":"A week","text":"Fine.","goals":[],"new_goals":[],"projects":[],"dreams":[],"retire_lessons":[],"new_lessons":[],"plans":[],"new_resources":[],
		"principles":[{"text":"Rest after a hard week.","types":["life","chores"],"calls":["` + c.ID.String() + `"]},{"text":"A guess.","types":["life"],"calls":[]}]}`
	s2 := &fakeSystem2{formats: map[string]string{"weekly_review": review}}
	m.System2 = s2
	if err := m.Review(ctx, at(26, 20, 1)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s2.requests[0].Note, `You picked "the motif"; Busta picked "rest". Why: you worked all week`) {
		t.Errorf("note:\n%s", s2.requests[0].Note)
	}
	if len(s.principles) != 1 || s.principles[0].Text != "Rest after a hard week." || s.principles[0].Status != memory.JudgmentProposed ||
		len(s.principles[0].Types) != 1 || len(s.principles[0].Cases) != 1 || s.principles[0].Cases[0] != c.CaseID.String() {
		t.Errorf("principles = %+v", s.principles)
	}
}

func TestLookingBackOnCalls(t *testing.T) {
	ctx := context.Background()
	reviewAt := at(26, 20, 1)
	agreed, differed := true, false
	theirs := memory.Call{ID: ulid.Make(), Type: memory.CallFocus, Mode: memory.CallAfter, Situation: "What to focus on today",
		Options: []string{"the motif", "rest"}, Pick: 0, Status: memory.CallAnswered, OwnerPick: 1, Agreed: &differed, CaseID: ulid.Make(),
		CreatedAt: reviewAt.Add(-5 * 24 * time.Hour)}
	ok := memory.Call{ID: ulid.Make(), Type: memory.CallStart, Mode: memory.CallAfter, Situation: "Start a new hook or go on",
		Options: []string{"start it", "go on"}, Pick: 1, Status: memory.CallAnswered, OwnerPick: 1, Agreed: &agreed, CreatedAt: reviewAt.Add(-4 * 24 * time.Hour)}
	tooNew := memory.Call{ID: ulid.Make(), Type: memory.CallFocus, Options: []string{"a", "b"}, Status: memory.CallLogged, CreatedAt: reviewAt.Add(-time.Hour)}
	approval := memory.Call{ID: ulid.Make(), Type: "publish", Options: []string{"yes", "no"}, Status: memory.CallAnswered, CreatedAt: reviewAt.Add(-4 * 24 * time.Hour)}
	review := `{"title":"A week","text":"Fine.","goals":[],"new_goals":[],"projects":[],"dreams":[],"retire_lessons":[],"new_lessons":[],"plans":[],"new_resources":[],
		"principles":[],"outcomes":[
		{"call_id":"` + theirs.ID.String() + `","outcome":"went_badly","verdict":"bad_call","note":"I pushed on tired and the motif got worse"},
		{"call_id":"` + ok.ID.String() + `","outcome":"went_well","verdict":"good_call","note":"Finishing first worked"},
		{"call_id":"` + tooNew.ID.String() + `","outcome":"went_well","verdict":"good_call","note":"not asked"},
		{"call_id":"` + ok.ID.String() + `","outcome":"great","verdict":"good_call","note":"not an outcome"}]}`
	s2 := &fakeSystem2{formats: map[string]string{"weekly_review": review}}
	m, s, _, _ := callsMind(s2)
	s.calls = []memory.Call{theirs, ok, tooNew, approval}
	s.cases = []memory.JudgmentCase{{ID: theirs.CaseID, Key: theirs.CaseID.String(), Type: "life", Situation: "What to focus on today",
		Choice: "rest", Status: memory.JudgmentApproved}}
	if err := m.Review(ctx, reviewAt); err != nil {
		t.Fatal(err)
	}
	note := s2.requests[0].Note
	for _, want := range []string{"[call " + theirs.ID.String() + "]", `you did "the motif". Busta would have chosen "rest".`, "[call " + ok.ID.String() + "]"} {
		if !strings.Contains(note, want) {
			t.Errorf("note is missing %q:\n%s", want, note)
		}
	}
	if strings.Contains(note, tooNew.ID.String()) || strings.Contains(note, approval.ID.String()) {
		t.Error("looked back on a call too new, or an approval")
	}
	if c := s.callAt(theirs.ID); c.Outcome != memory.OutcomeBadly || c.Verdict != memory.VerdictBad || c.OutcomeBy != memory.OutcomeBySelf {
		t.Errorf("call = %+v", c)
	}
	if c := s.callAt(ok.ID); c.Outcome != memory.OutcomeWell {
		t.Errorf("call = %+v", c)
	}
	if c := s.callAt(tooNew.ID); c.Outcome != "" {
		t.Error("recorded an outcome for a call it wasn't asked about")
	}
	// How it turned out goes with Busta's decision into later notes.
	if note := m.judgmentNote(ctx, planJudgment); !strings.Contains(note, "How it turned out: went_badly, bad_call: I pushed on tired") {
		t.Errorf("judgment note:\n%s", note)
	}
	// The owner's verdict stands over a later review's.
	s.SetOutcome(ctx, ok.ID, memory.OutcomeMixed, memory.VerdictLucky, "", memory.OutcomeByOwner, now)
	if done, _ := s.SetOutcome(ctx, ok.ID, memory.OutcomeWell, memory.VerdictGood, "", memory.OutcomeBySelf, now); done {
		t.Error("its review overrode the owner's verdict")
	}
}
