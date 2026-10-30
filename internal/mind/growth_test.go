package mind

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/Grandbusta/minime/internal/action"
	"github.com/Grandbusta/minime/internal/decision"
	"github.com/Grandbusta/minime/internal/decision/questions"
	"github.com/Grandbusta/minime/internal/memory"
	"github.com/Grandbusta/minime/internal/providers/system1"
	"github.com/Grandbusta/minime/internal/providers/system2"
)

var testSkillLevel = questions.Set{ID: "skill_level", Version: 1,
	Questions: []system1.Question{{Name: "level", Type: decision.QuestionScore, Instructions: "How good is `work` as `skill`?"}}}

func TestWorksAreScoredIntoSkills(t *testing.T) {
	s1 := &fakeSystem1{scores: map[string]float64{"Lagos Rain, verse 2": 4.9}} // index 4.9 → 5.9
	m, s, _, _, _ := activityMind()
	m.System1, m.SkillLevel = s1, testSkillLevel
	s.skills = []memory.Skill{{Name: "lyric writing", Level: 4, ShownBy: []string{"lyrics"}},
		{Name: "music production", Level: 3, ShownBy: []string{"beat_idea"}}}
	s.evidence = []memory.Evidence{{Skill: "lyric writing", Score: 4, Source: memory.EvidenceOwner}} // counts double
	m.tick(context.Background(), at(29, 10, 0))

	if len(s1.scoreCalls) != 1 || s1.scoreCalls[0].State.(map[string]any)["skill"] != "lyric writing" {
		t.Fatalf("scored %d times; want once, for the skill lyrics show", len(s1.scoreCalls))
	}
	if e := s.evidence[1]; e.Score != 5.9 || e.Source != memory.EvidenceSystem1 || e.WorkID.IsZero() {
		t.Errorf("evidence = %+v", e)
	}
	if lvl := s.skills[0].Level; lvl != 4.6 { // (4×2 + 5.9) / 3
		t.Errorf("level = %.2f, want 4.6", lvl)
	}
}

func TestTheOwnersFeedbackCounts(t *testing.T) {
	m, s, _ := gatedMind(&fakeReplier{}, &fakeSystem2{})
	s.skills = []memory.Skill{{Name: "lyric writing", Level: 4}}
	reply := &decision.Decision{ID: ulid.Make()}
	fb := []system2.Feedback{{Skill: "Lyric Writing", Level: 2, About: "the verse"}, {Skill: "dancing", Level: 9}}
	if ds := m.takeFeedback(context.Background(), from(sam), reply, fb); len(ds) != 0 {
		t.Error("someone who isn't the owner moved a skill")
	}
	ds := m.takeFeedback(context.Background(), from(owner), reply, fb)
	if len(ds) != 1 || s.skills[0].Level != 2 || s.evidence[0].Source != memory.EvidenceOwner || s.evidence[0].Note != "the verse" {
		t.Errorf("decisions %d, skills %+v, evidence %+v", len(ds), s.skills, s.evidence)
	}
}

func reviewMind(review string) (*Mind, *fakeStore, *fakeSystem2, *fakeAsker) {
	s2 := &fakeSystem2{formats: map[string]string{"weekly_review": review}}
	m, s, asker := gatedMind(&fakeReplier{}, s2)
	m.Zone = lagos
	m.Day = Day{ReviewDay: time.Sunday, ReviewAt: 20 * time.Hour, ReviewPrompt: Prompt{Ref: "review@test", Text: "You are {name}, reviewing."}}
	return m, s, s2, asker
}

func TestWeeklyReview(t *testing.T) {
	dream := memory.Dream{ID: ulid.Make(), Text: "Tobi wants to be a producer", Status: memory.DreamActive}
	done := memory.Goal{ID: ulid.Make(), DreamID: dream.ID, Text: "three beat ideas", Status: memory.GoalActive, Due: at(30, 0, 0)}
	project := memory.Project{ID: ulid.Make(), Title: "Lagos Rain", Kind: "music", Status: memory.ProjectActive}
	review := `{"title":"A busy week","text":"I finished three beat ideas.",
		"goals":[{"goal_id":"` + done.ID.String() + `","progress":"all three done","status":"done"},{"goal_id":"nope","progress":"x","status":"done"}],
		"new_goals":[{"dream_id":"` + dream.ID.String() + `","text":"Five beat ideas for an EP","done_when":"five in the EP project","due":"2026-11-30","project_ids":["` + project.ID.String() + `"]},
			{"dream_id":"nope","text":"x","done_when":"x","due":"2026-11-30","project_ids":[]},
			{"dream_id":"` + dream.ID.String() + `","text":"In the past","done_when":"x","due":"2026-01-01","project_ids":[]}],
		"projects":[{"project_id":"` + project.ID.String() + `","goal_id":"not-a-goal"}],
		"dreams":[{"action":"add","dream_id":"","text":"Tobi wants to write about Nigerian history","why":"reading Equiano moved me"}]}`
	m, s, s2, asker := reviewMind(review)
	s.dreams, s.goals, s.projects[project.ID] = []memory.Dream{dream}, []memory.Goal{done}, project

	m.tick(context.Background(), at(25, 19, 59)) // Sunday 25 October, before the review time
	if len(s2.requests) != 0 {
		t.Fatal("reviewed early")
	}
	m.tick(context.Background(), at(25, 20, 0))
	if len(s2.requests) != 1 {
		t.Fatalf("requests = %d", len(s2.requests))
	}
	if g := s.goals[0]; g.Status != memory.GoalDone || g.Progress != "all three done" {
		t.Errorf("done goal = %+v", g)
	}
	if len(s.goals) != 2 || s.goals[1].Text != "Five beat ideas for an EP" || s.goals[1].Status != memory.GoalActive {
		t.Fatalf("goals = %+v; want only the valid new one added", s.goals)
	}
	if len(s.dreams) != 1 || len(asker.asked) != 1 || !strings.Contains(asker.asked[0], "Can I add a dream: Tobi wants to write about Nigerian history. Why: reading Equiano moved me?") {
		t.Errorf("dreams %+v; asked %q", s.dreams, asker.asked)
	}
	if p := s.projects[project.ID]; p.GoalID != s.goals[1].ID {
		t.Errorf("project = %+v, want it linked to the new goal", p)
	}

	m.tick(context.Background(), at(25, 20, 1))
	again, _, _, _ := reviewMind(review)
	again.Store = s
	again.tick(context.Background(), at(25, 21, 0))
	if len(s2.requests) != 1 {
		t.Errorf("reviewed twice: %d", len(s2.requests))
	}

	m.handle(context.Background(), tap(owner, asker.ids[0], true))
	if len(s.dreams) != 2 || s.dreams[1].Text != "Tobi wants to write about Nigerian history" || s.dreams[1].Source != memory.DreamFromSelf {
		t.Errorf("dreams = %+v", s.dreams)
	}
}

func TestDreamsChangeOnTheirOwnWhenAllowed(t *testing.T) {
	dream := memory.Dream{ID: ulid.Make(), Text: "Tobi wants to be a DJ", Status: memory.DreamActive}
	review := `{"title":"t","text":"x","goals":[],"new_goals":[],"projects":[],
		"dreams":[{"action":"drop","dream_id":"` + dream.ID.String() + `","text":"","why":"I prefer producing"}]}`
	m, s, _, asker := reviewMind(review)
	m.Autonomy.Classes[action.Dream] = action.Auto
	s.dreams = []memory.Dream{dream}
	if err := m.Review(context.Background(), at(25, 20, 0)); err != nil {
		t.Fatal(err)
	}
	if s.dreams[0].Status != memory.DreamDropped || len(asker.asked) != 0 {
		t.Errorf("dream %+v, asked %q", s.dreams[0], asker.asked)
	}
}
