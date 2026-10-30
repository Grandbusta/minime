package mind

import (
	"context"
	"github.com/Grandbusta/minime/internal/decision"
	"github.com/Grandbusta/minime/internal/decision/questions"
	"github.com/Grandbusta/minime/internal/providers/system1"
	"strings"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/Grandbusta/minime/internal/ability"
	"github.com/Grandbusta/minime/internal/ability/journal"
	"github.com/Grandbusta/minime/internal/memory"
)

// at is a time on a day in October 2026, in the agent's zone (lagos, UTC+1).
func at(day, hour, minute int) time.Time { return time.Date(2026, 10, day, hour, minute, 0, 0, lagos) }

func lifeMind(s2 *fakeSystem2) (*Mind, *fakeStore) {
	m, s := newMind(&fakeReplier{}, &fakeSystem1{}, s2)
	m.Zone = lagos
	m.Life.Routine = []string{"Tobi makes music in the evenings."}
	m.Day = Day{PlanAt: 8 * time.Hour, JournalAt: 22 * time.Hour, MaxActivities: 3, BudgetUSD: 0.05,
		PlanPrompt:     Prompt{Ref: "plan@test", Text: "You are {name}. Routine:\n{routine}\nUp to {max}."},
		ActivityPrompt: Prompt{Ref: "activity@test", Text: "You are {name}, doing something."}}
	m.Abilities = ability.NewSet(journal.Journal{Store: s})
	return m, s
}

func TestPlanDay(t *testing.T) {
	ctx := context.Background()
	song := memory.Project{ID: ulid.Make(), Title: "Lagos Rain", Kind: "music", Status: memory.ProjectActive,
		Notes: "Verse 1 done; next: verse 2", CreatedAt: at(27, 10, 0), UpdatedAt: at(28, 18, 0)}
	s2 := &fakeSystem2{formats: map[string]string{"day_plan": `{"activities":[
		{"at":"10:00","what":"Write verse 2 of Lagos Rain","project_id":"` + song.ID.String() + `","new_project_title":"","project_kind":""},
		{"at":"07:00","what":"Already past","project_id":"","new_project_title":"","project_kind":""},
		{"at":"23:00","what":"After the journal","project_id":"","new_project_title":"","project_kind":""},
		{"at":"14:00","what":"Sketch a beat for a new song","project_id":"not-an-id","new_project_title":"Night Drive","project_kind":"music"},
		{"at":"16:30","what":"Write notes on chord progressions","project_id":"","new_project_title":"","project_kind":""},
		{"at":"18:00","what":"One too many","project_id":"","new_project_title":"","project_kind":""}]}`}}
	m, s := lifeMind(s2)
	s.projects[song.ID] = song

	m.tick(ctx, at(29, 7, 59))
	if len(s2.requests) != 0 {
		t.Fatal("planned before plan time")
	}
	m.tick(ctx, at(29, 9, 0))
	if len(s2.requests) != 1 {
		t.Fatalf("system 2 requests = %d, want one plan", len(s2.requests))
	}
	var got []string
	var newProject ulid.ULID
	for _, i := range s.intentions {
		got = append(got, i.DueAt.In(lagos).Format("15:04")+" "+i.Text)
		if strings.HasPrefix(i.Text, "Sketch") {
			newProject = i.ProjectID
		}
		if strings.HasPrefix(i.Text, "Write verse 2") && i.ProjectID != song.ID {
			t.Errorf("verse 2 is not part of Lagos Rain: %+v", i)
		}
	}
	want := []string{"10:00 Write verse 2 of Lagos Rain", "14:00 Sketch a beat for a new song",
		"16:30 Write notes on chord progressions", "22:00 " + journalText}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("planned:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if p, ok := s.projects[newProject]; !ok || p.Title != "Night Drive" || p.Kind != "music" {
		t.Errorf("new project = %+v", p)
	}

	m.tick(ctx, at(29, 9, 1))
	restarted, _ := lifeMind(s2)
	restarted.Store = s
	restarted.tick(ctx, at(29, 9, 2))
	if len(s2.requests) != 1 {
		t.Errorf("planned the same day again: %d requests", len(s2.requests))
	}
}

const activityJSON = `{"title":"Lagos Rain, verse 2","kind":"lyrics","text":"Rain on the danfo roof...",
	"project_notes":"Verses done; next: the bridge","project_done":false,
	"remember":["Tobi wrote the second verse of Lagos Rain"," "]}`

func activityMind() (*Mind, *fakeStore, *fakeSystem2, memory.Project, memory.Intention) {
	s2 := &fakeSystem2{formats: map[string]string{"activity": activityJSON}}
	m, s := lifeMind(s2)
	m.planned = "2026-10-29"
	song := memory.Project{ID: ulid.Make(), Title: "Lagos Rain", Kind: "music", Status: memory.ProjectActive,
		Notes: "Verse 1 done; next: verse 2", CreatedAt: at(27, 10, 0), UpdatedAt: at(28, 18, 0)}
	s.projects[song.ID] = song
	s.works = []memory.Work{{ID: ulid.Make(), ProjectID: song.ID, Kind: "lyrics", Title: "Lagos Rain, verse 1",
		Text: "Wake up to the horns...", CreatedAt: at(28, 18, 0)}}
	i := memory.Intention{ID: ulid.Make(), Subject: memory.SubjectSelf, Kind: memory.IntentionActivity, ProjectID: song.ID,
		Text: "Write verse 2 of Lagos Rain", DueAt: at(29, 10, 0), Status: memory.IntentionPending, CreatedAt: at(29, 8, 0)}
	s.intentions = []memory.Intention{i}
	return m, s, s2, song, i
}

func TestActivityMakesAWorkAndRemembersIt(t *testing.T) {
	m, s, s2, song, i := activityMind()
	m.tick(context.Background(), at(29, 10, 0))

	if len(s2.requests) != 1 {
		t.Fatalf("system 2 requests = %d", len(s2.requests))
	}
	w := s.works[len(s.works)-1]
	if w.Title != "Lagos Rain, verse 2" || w.Kind != "lyrics" || w.ProjectID != song.ID || w.IntentionID != i.ID {
		t.Errorf("work = %+v", w)
	}
	if p := s.projects[song.ID]; p.Notes != "Verses done; next: the bridge" || p.Status != memory.ProjectActive {
		t.Errorf("project = %+v", p)
	}
	if len(s.facts) != 1 || s.facts[0].Subject != memory.SubjectSelf || s.facts[0].Text != "Tobi wrote the second verse of Lagos Rain" {
		t.Errorf("facts = %+v", s.facts)
	}
	if got := s.intention(i.ID); got.Status != memory.IntentionDone || got.Result != "Lagos Rain, verse 2" {
		t.Errorf("intention = %+v", got)
	}
}

func TestBusyActivityWaits(t *testing.T) {
	m, s, s2, _, i := activityMind()
	s.convs = []memory.Conversation{{Person: sam, Source: "test", Thread: "chat-1", LastActivity: at(29, 9, 58)}}
	m.tick(context.Background(), at(29, 10, 0))
	if len(s2.requests) != 0 || !s.intention(i.ID).DueAt.Equal(at(29, 10, 30)) {
		t.Errorf("requests %d, due %v; want postponed to 10:30", len(s2.requests), s.intention(i.ID).DueAt.In(lagos))
	}
}

func TestOverBudgetOrLateActivityIsMissed(t *testing.T) {
	m, s, s2, _, i := activityMind()
	s.spent = 0.05
	m.tick(context.Background(), at(29, 10, 0))
	if got := s.intention(i.ID); got.Status != memory.IntentionMissed || len(s2.requests) != 0 {
		t.Errorf("over budget: %+v, %d requests", got, len(s2.requests))
	}

	m, s, s2, _, i = activityMind()
	m.tick(context.Background(), at(29, 16, 1))
	if got := s.intention(i.ID); got.Status != memory.IntentionMissed || len(s2.requests) != 0 {
		t.Errorf("6h late: %+v, %d requests", got, len(s2.requests))
	}
}

func TestRounds(t *testing.T) {
	ctx := context.Background()
	s2 := &fakeSystem2{formats: map[string]string{"day_plan": `{"activities":[
		{"at":"08:30","what":"Study customer discovery","project_id":"","new_project_title":"","project_kind":"","ability":"","query":"","goal_id":"","resource_id":""},
		{"at":"12:00","what":"Write outreach email","project_id":"","new_project_title":"","project_kind":"","ability":"","query":"","goal_id":"","resource_id":""},
		{"at":"16:00","what":"Role-play a sales call","project_id":"","new_project_title":"","project_kind":"","ability":"","query":"","goal_id":"","resource_id":""}],
		"calls":[{"type":"focus","situation":"What to focus on","options":["sales","music"],"pick":0,"why":"","confidence":0.6,"stakes":"low","reversible":true}]}`}}
	m, s := lifeMind(s2)
	m.Calls = Calls{On: true, AsksPerDay: 3}
	m.Day.Pace = 4 // 08:00 to 22:00 in four rounds: 08:00, 11:30, 15:00, 18:30

	due := func(from, to time.Time) []string {
		var out []string
		is, _ := s.IntentionsDue(ctx, from, to)
		for _, i := range is {
			out = append(out, i.Text)
		}
		return out
	}

	m.planDay(ctx, at(29, 8, 1))
	if got := due(at(29, 0, 0), at(29, 23, 59)); len(got) != 2 || got[0] != "Study customer discovery" || got[1] != journalText {
		t.Fatalf("first round = %q; want only what's before 11:30, and the journal", got)
	}
	if len(s.calls) != 1 {
		t.Errorf("calls = %d", len(s.calls))
	}

	m.planDay(ctx, at(29, 9, 0)) // still the first round
	if len(s2.requests) != 1 {
		t.Fatal("planned the same round again")
	}

	m.planDay(ctx, at(29, 11, 31))
	if got := due(at(29, 11, 30), at(29, 15, 0)); len(got) != 1 || got[0] != "Write outreach email" {
		t.Errorf("round 2 = %q", got)
	}
	if len(s.calls) != 1 {
		t.Error("a later round made calls")
	}

	m2, _ := lifeMind(s2)
	m2.Store, m2.Day.Pace = s, 4
	m2.planDay(ctx, at(29, 12, 0))
	if len(s2.requests) != 2 {
		t.Fatal("planned again after a restart")
	}
	m2.planDay(ctx, at(29, 15, 1))
	if len(s2.requests) != 3 || !strings.Contains(s2.requests[2].Note, "round 3 of 4") {
		t.Errorf("after a restart, round 3 wasn't planned: %d requests", len(s2.requests))
	}
}

func TestAStageMovesOnOnlyWhenTheCheckAgrees(t *testing.T) {
	ctx := context.Background()
	for name, c := range map[string]struct {
		sure float64
		want int
	}{"the check agrees": {0.9, 1}, "notes about it, not the thing": {0.2, 0}} {
		m, s, s2, _, _ := activityMind()
		s1 := &fakeSystem1{stageMet: map[string]float64{"Outreach notes": c.sure}}
		m.System1 = s1
		m.StageCheck = questions.Set{ID: "stage_check", Version: 1, Primary: "met",
			Questions:  []system1.Question{{Name: "met", Type: decision.QuestionPredicate, Instructions: "?"}},
			Thresholds: questions.Thresholds{Act: 0.7, Ask: 0.5}}
		plan := memory.LearningPlan{ID: ulid.Make(), Skill: "sales and marketing", Status: memory.PlanActive, Stages: []memory.Stage{
			{Learn: "outreach", Practice: "two intro emails", DoneWhen: "two emails with one clear request"}, {Learn: "calls"}}}
		s.plans = []memory.LearningPlan{plan}
		s2.formats["activity"] = `{"title":"Outreach notes","kind":"notes","text":"How to write outreach...","project_notes":"","project_done":false,
			"remember":[],"lessons":[],"ask_for_review":false,"review_question":"","stage_done":"` + plan.ID.String() + `"}`
		m.tick(ctx, at(29, 10, 0))
		if s.plans[0].Stage != c.want {
			t.Errorf("%s: stage = %d, want %d", name, s.plans[0].Stage, c.want)
		}
		if len(s1.stageCalls) != 1 {
			t.Fatalf("%s: checked %d times", name, len(s1.stageCalls))
		}
	}
}
