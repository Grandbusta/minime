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
	"github.com/Grandbusta/minime/internal/self"
)

var testSkillLevel = questions.Set{ID: "skill_level", Version: 1,
	Questions: []system1.Question{{Name: "level", Type: decision.QuestionScore, Instructions: "How good is `work` as `skill`?"}}}

func TestBornWanting(t *testing.T) {
	m, s := lifeMind(&fakeSystem2{})
	m.Life.Dreams = []string{"Tobi wants to be a producer"}
	m.Life.Skills = []self.Skill{{Name: "music production", Level: 3, ShownBy: []string{"beat_idea"}}}
	m.bornWanting(context.Background())
	if len(s.dreams) != 1 || s.dreams[0].Source != memory.DreamFromLife || len(s.skills) != 1 || s.skills[0].Level != 3 {
		t.Fatalf("dreams %+v, skills %+v", s.dreams, s.skills)
	}
	s.dreams[0].Status, s.skills[0].Level = memory.DreamDropped, 5.2
	m.Life.Skills = append(m.Life.Skills, self.Skill{Name: "Yoruba", Level: 2})
	m.bornWanting(context.Background())
	if len(s.dreams) != 1 || s.dreams[0].Status != memory.DreamDropped || s.skills[0].Level != 5.2 || len(s.skills) != 2 {
		t.Errorf("dreams %+v, skills %+v", s.dreams, s.skills)
	}
}

func TestWorksAreScoredIntoSkills(t *testing.T) {
	s1 := &fakeSystem1{scores: map[string]float64{"Lagos Rain, verse 2": 4.9}} // index 4.9 → 5.9
	m, s, _, _, i := activityMind()
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
	d := s.decisions[len(s.decisions)-1]
	if d.Action != "score_skill" || d.ParentID == nil || !strings.Contains(d.Outcome.Detail, "scored 5.9; level 4.0 → 4.6") {
		t.Errorf("decision = %+v %+v", d, d.Outcome)
	}
	if got := s.intention(i.ID); got.Status != memory.IntentionDone {
		t.Errorf("intention = %+v", got)
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
	if help := m.feedbackHelp(context.Background(), s.people[owner]); !strings.Contains(help, "one of: lyric writing") {
		t.Errorf("help = %q", help)
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
	s.skills = []memory.Skill{{Name: "music production", Level: 4.5}}

	m.tick(context.Background(), at(25, 19, 59)) // Sunday 25 October, before the review time
	if len(s2.requests) != 0 {
		t.Fatal("reviewed early")
	}
	m.tick(context.Background(), at(25, 20, 0))
	if len(s2.requests) != 1 {
		t.Fatalf("requests = %d", len(s2.requests))
	}
	note := s2.requests[0].Note
	for _, want := range []string{"[dream " + dream.ID.String() + "]", "[goal " + done.ID.String() + "] three beat ideas", "music production: 4.5",
		"[project " + project.ID.String() + "] Lagos Rain"} {
		if !strings.Contains(note, want) {
			t.Errorf("note is missing %q:\n%s", want, note)
		}
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
	if w := s.works[len(s.works)-1]; w.Kind != "review" || w.Title != "A busy week" {
		t.Errorf("review = %+v", w)
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

func TestThePlanWorksTowardGoals(t *testing.T) {
	m, s := lifeMind(&fakeSystem2{})
	goal := memory.Goal{ID: ulid.Make(), DreamID: ulid.Make(), Text: "Five beat ideas for an EP", Status: memory.GoalActive, Due: at(30, 0, 0)}
	s.goals = []memory.Goal{goal}
	m.System2 = &fakeSystem2{formats: map[string]string{"day_plan": `{"activities":[
		{"at":"10:00","what":"Sketch beat idea four","project_id":"","new_project_title":"The EP","project_kind":"music","ability":"","query":"","goal_id":"` + goal.ID.String() + `"}]}`}}
	m.tick(context.Background(), at(29, 9, 0))
	if note := m.System2.(*fakeSystem2).requests[0].Note; !strings.Contains(note, "[goal "+goal.ID.String()+"] Five beat ideas for an EP") {
		t.Errorf("the plan wasn't told its goals:\n%s", note)
	}
	for _, p := range s.projects {
		if p.Title == "The EP" && p.GoalID != goal.ID {
			t.Errorf("new project = %+v, want it to serve the goal", p)
		}
	}
}

func TestTheStartingLevelIsAFirstScore(t *testing.T) {
	m, s, _ := gatedMind(&fakeReplier{}, &fakeSystem2{})
	m.Life.Skills = []self.Skill{{Name: "lyric writing", Level: 4, ShownBy: []string{"lyrics"}}}
	m.bornWanting(context.Background())
	if len(s.evidence) != 1 || s.evidence[0].Source != memory.EvidenceLife || s.evidence[0].Score != 4 {
		t.Fatalf("evidence = %+v", s.evidence)
	}
	reply := &decision.Decision{ID: ulid.Make()}
	m.takeFeedback(context.Background(), from(owner), reply, []system2.Feedback{{Skill: "lyric writing", Level: 3}})
	if lvl := s.skills[0].Level; lvl != 3.3 { // (4 + 3×2) / 3
		t.Errorf("level = %.2f, want 3.3", lvl)
	}
	m.bornWanting(context.Background()) // counted once
	if len(s.evidence) != 2 {
		t.Errorf("evidence = %+v", s.evidence)
	}
}

func TestAStartingLevelIsCountedForASkillThatAlreadyHasScores(t *testing.T) {
	m, s, _ := gatedMind(&fakeReplier{}, &fakeSystem2{})
	s.skills = []memory.Skill{{Name: "lyric writing", Level: 3}} // its first score replaced the starting level
	s.evidence = []memory.Evidence{{Skill: "lyric writing", Score: 3, Source: memory.EvidenceOwner, At: now}}
	m.Life.Skills = []self.Skill{{Name: "lyric writing", Level: 4}}
	m.bornWanting(context.Background())
	if lvl := s.skills[0].Level; lvl != 3.3 || !s.evidence[1].At.Before(now) {
		t.Errorf("level = %.2f, evidence %+v", lvl, s.evidence)
	}
}
