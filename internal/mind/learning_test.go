package mind

import (
	"context"
	"github.com/Grandbusta/minime/internal/providers/system2"
	"strings"
	"testing"

	"github.com/oklog/ulid/v2"

	"github.com/Grandbusta/minime/internal/memory"
)

const planJSON = `{"stages":[
	{"learn":"how a hook repeats","practice":"a four-line hook built on one word","done_when":"two hooks Busta calls catchy",
		"resources":[{"ability":"look_up","query":"hook (music)","why":"what makes one"},{"ability":"fly","query":"x","why":"not an ability"}]},
	{"learn":"rhythm in lyrics","practice":"a verse on the beat","done_when":"a verse that sits on the beat","resources":[]},
	{"learn":"a whole song","practice":"a full song","done_when":"one finished song","resources":[]},
	{"learn":"","practice":"skipped: nothing to learn","done_when":"","resources":[]}]}`

func learningPlanMind() (*Mind, *fakeStore, *fakeSystem2, *fakeAbility) {
	look := &fakeAbility{name: "look_up", describe: "look a topic up", found: map[string]string{"hook (music)": "A hook is a catchy phrase."}}
	m, s, s2 := abilityMind(look)
	s2.formats["learning_plan"] = planJSON
	m.Day.LearnPrompt = Prompt{Ref: "learn@test", Text: "You are {name}, planning to learn."}
	s.skills = []memory.Skill{{Name: "lyric writing", Level: 3.3, ShownBy: []string{"lyrics"}}}
	s.lessons = []memory.Lesson{{ID: ulid.Make(), Skill: "lyric writing", Text: "Keep hooks to six punchy words"}}
	return m, s, s2, look
}

func TestASkillGoalGetsALearningPlan(t *testing.T) {
	m, s, s2, _ := learningPlanMind()
	goal := memory.Goal{ID: ulid.Make(), DreamID: ulid.Make(), Text: "Write a hook people shout back", Skill: "lyric writing",
		Status: memory.GoalActive, Due: at(30, 0, 0)}
	plain := memory.Goal{ID: ulid.Make(), DreamID: ulid.Make(), Text: "Not a skill goal", Status: memory.GoalActive, Due: at(30, 0, 0)}
	s.goals = []memory.Goal{goal, plain}
	m.EnsurePlans(context.Background(), at(29, 9, 0))

	if len(s2.requests) != 1 {
		t.Fatalf("requests = %d, want one plan, for the skill goal", len(s2.requests))
	}
	if len(s.plans) != 1 || len(s.plans[0].Stages) != 3 || s.plans[0].GoalID != goal.ID || s.plans[0].Stages[0].Practice != "a four-line hook built on one word" {
		t.Fatalf("plans = %+v", s.plans)
	}
	if len(s.resources) != 1 || s.resources[0].Ability != "look_up" || s.resources[0].Stage != 0 || s.resources[0].Status != memory.ResourceTodo {
		t.Errorf("resources = %+v; want only the one it can read", s.resources)
	}
	m.EnsurePlans(context.Background(), at(29, 9, 0))
	if len(s2.requests) != 1 {
		t.Error("planned the same goal twice")
	}
}

func TestDaysFollowTheLearningPlan(t *testing.T) {
	m, s, _, look := learningPlanMind()
	goal := memory.Goal{ID: ulid.Make(), DreamID: ulid.Make(), Text: "Hooks", Skill: "lyric writing", Status: memory.GoalActive, Due: at(30, 0, 0)}
	s.goals = []memory.Goal{goal}
	m.EnsurePlans(context.Background(), at(29, 7, 0))
	res := s.resources[0]

	s2 := &fakeSystem2{formats: map[string]string{
		"day_plan": `{"activities":[{"at":"10:00","what":"Read about hooks","project_id":"","new_project_title":"","project_kind":"",
			"ability":"","query":"","goal_id":"` + goal.ID.String() + `","resource_id":"` + res.ID.String() + `"}]}`,
		"activity": activityJSON}}
	m.System2 = s2
	m.planned = ""
	m.tick(context.Background(), at(29, 9, 0))
	var i memory.Intention
	for _, x := range s.intentions {
		if x.Text == "Read about hooks" {
			i = x
		}
	}
	if i.Ability != "look_up" || i.Args["query"] != "hook (music)" || i.Args["resource"] != res.ID.String() {
		t.Fatalf("intention = %+v; want it to study the resource", i)
	}
	m.tick(context.Background(), at(29, 10, 0))
	if len(look.acts) != 1 || s.resources[0].Status != memory.ResourceDone || s.resources[0].WorkID.IsZero() {
		t.Errorf("acts %d, resource %+v", len(look.acts), s.resources[0])
	}
}

func TestTheReviewMovesTheLearningPlanOn(t *testing.T) {
	m, s, _, _ := learningPlanMind()
	dream := memory.Dream{ID: ulid.Make(), Text: "be a songwriter", Status: memory.DreamActive}
	goal := memory.Goal{ID: ulid.Make(), DreamID: dream.ID, Text: "Hooks", Status: memory.GoalActive, Due: at(30, 0, 0)}
	s.dreams, s.goals = []memory.Dream{dream}, []memory.Goal{goal}
	plan := memory.LearningPlan{ID: ulid.Make(), GoalID: goal.ID, Skill: "lyric writing", Status: memory.PlanActive,
		Stages: []memory.Stage{{N: 0, Learn: "a", Practice: "a"}, {N: 1, Learn: "b", Practice: "b"}}}
	s.plans = []memory.LearningPlan{plan}
	review := `{"title":"t","text":"x","goals":[{"goal_id":"` + goal.ID.String() + `","progress":"halfway","status":"active","skill":"Lyric Writing"}],
		"new_goals":[],"projects":[],"dreams":[],"retire_lessons":[],"new_lessons":[],
		"plans":[{"plan_id":"` + plan.ID.String() + `","stage_done":true}],
		"new_resources":[{"plan_id":"` + plan.ID.String() + `","ability":"look_up","query":"rhyme","why":"stage two"},{"plan_id":"nope","ability":"look_up","query":"x","why":""}]}`
	m.System2 = &fakeSystem2{formats: map[string]string{"weekly_review": review, "learning_plan": planJSON}}
	m.Day.ReviewPrompt = Prompt{Ref: "review@test", Text: "Review."}
	if err := m.Review(context.Background(), at(25, 20, 0)); err != nil {
		t.Fatal(err)
	}
	if p := s.plans[0]; p.Stage != 1 || p.Stages[0].DoneAt.IsZero() {
		t.Errorf("plan = %+v", p)
	}
	if len(s.resources) != 1 || s.resources[0].Stage != 1 || s.resources[0].Query != "rhyme" {
		t.Errorf("resources = %+v; want one for the new stage", s.resources)
	}

	done := strings.Replace(review, `"status":"active"`, `"status":"done"`, 1)
	m.System2 = &fakeSystem2{formats: map[string]string{"weekly_review": done}}
	if err := m.Review(context.Background(), at(25, 21, 0)); err != nil {
		t.Fatal(err)
	}
	if s.plans[0].Status != memory.PlanDone {
		t.Errorf("plan = %s, want done with its goal", s.plans[0].Status)
	}
}

func TestTakingOnAGoalTheOwnerAsksFor(t *testing.T) {
	ctx := context.Background()
	m, s, s2, _ := learningPlanMind()
	m.Autonomy.Owner = owner
	m.ReplyPrompt.Text += "{feedback_help}"
	m.Life.Name = "Daniel"
	music := memory.Dream{ID: ulid.Make(), Text: "Daniel wants to be a producer", Status: memory.DreamActive}
	s.dreams = []memory.Dream{music}
	s.people[owner] = memory.Person{Key: owner, Name: "Busta"}
	s2.goals = []system2.GoalTaken{{Skill: "sales and marketing", Level: 1, Text: "Learn to sell a B2B app",
		DoneWhen: "three written pitches to realistic B2B customers", Due: "2026-12-01", NewDream: "Daniel wants to build things people pay for."}}

	p := percept("1", "So add sales and marketing to your goals and start planning around it")
	p.Speaker, p.SpeakerName = owner, "Busta"
	m.handle(ctx, p)

	var g memory.Goal
	for _, x := range s.goals {
		if x.Text == "Learn to sell a B2B app" {
			g = x
		}
	}
	if g.Status != memory.GoalActive || g.Skill != "sales and marketing" || g.DoneWhen == "" || g.Due.IsZero() {
		t.Fatalf("goal = %+v (all: %+v)", g, s.goals)
	}
	if len(s.dreams) != 2 || s.dreams[1].Source != memory.DreamFromOwner || g.DreamID != s.dreams[1].ID {
		t.Errorf("dreams = %+v, goal's dream %s", s.dreams, g.DreamID)
	}

	// The learning plan comes on the next tick, once the reply is sent.
	if len(s.plans) != 0 {
		t.Fatal("drafted a plan before replying")
	}
	m.tick(ctx, at(30, 13, 0))
	if len(s.plans) != 1 || s.plans[0].GoalID != g.ID {
		t.Errorf("plans = %+v", s.plans)
	}

	before := len(s.goals)
	m.handle(ctx, percept("2", "Learn juggling"))
	if len(s.goals) != before {
		t.Error("took on a goal someone who isn't the owner asked for")
	}
}
