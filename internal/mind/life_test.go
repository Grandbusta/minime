package mind

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/Grandbusta/minime/internal/ability"
	"github.com/Grandbusta/minime/internal/ability/journal"
	"github.com/Grandbusta/minime/internal/memory"
	"github.com/Grandbusta/minime/internal/providers/system2"
	"github.com/Grandbusta/minime/internal/self"
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
	s.works = []memory.Work{{ID: ulid.Make(), Kind: "journal", Title: "28 Oct", Text: "Good day; verse 1 is done.", CreatedAt: at(28, 22, 0)}}

	m.tick(ctx, at(29, 7, 59))
	if len(s2.requests) != 0 {
		t.Fatal("planned before plan time")
	}
	m.tick(ctx, at(29, 9, 0))
	if len(s2.requests) != 1 {
		t.Fatalf("system 2 requests = %d, want one plan", len(s2.requests))
	}
	req := s2.requests[0]
	if !strings.Contains(req.Instructions, "Tobi makes music in the evenings.") || !strings.Contains(req.Instructions, "Up to 3.") {
		t.Errorf("instructions = %q", req.Instructions)
	}
	for _, want := range []string{"id " + song.ID.String() + ": Lagos Rain", "Verse 1 done; next: verse 2", "Good day; verse 1 is done.", "22:00"} {
		if !strings.Contains(req.Note, want) {
			t.Errorf("plan note is missing %q:\n%s", want, req.Note)
		}
	}

	var got []string
	var newProject ulid.ULID
	for _, i := range s.intentions {
		got = append(got, i.DueAt.In(lagos).Format("15:04")+" "+i.Text)
		if i.Subject != memory.SubjectSelf || i.Kind != memory.IntentionActivity || i.Status != memory.IntentionPending {
			t.Errorf("intention = %+v", i)
		}
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
	if d := s.decisions[len(s.decisions)-1]; d.Action != "plan_day" || !d.PerceptID.IsZero() || !strings.Contains(d.Reply, "10:00") {
		t.Errorf("decision = %+v", d)
	}

	// Planned once a day, even after a restart.
	m.tick(ctx, at(29, 9, 1))
	restarted, _ := lifeMind(s2)
	restarted.Store = s
	restarted.tick(ctx, at(29, 9, 2))
	if len(s2.requests) != 1 {
		t.Errorf("planned the same day again: %d requests", len(s2.requests))
	}
}

func TestNoPlanAfterJournalTime(t *testing.T) {
	s2 := &fakeSystem2{}
	m, s := lifeMind(s2)
	m.tick(context.Background(), at(29, 22, 30))
	if len(s2.requests) != 0 || len(s.intentions) != 0 {
		t.Errorf("planned a day that is over: %d requests, %d intentions", len(s2.requests), len(s.intentions))
	}
}

const activityJSON = `{"title":"Lagos Rain, verse 2","kind":"lyrics","text":"Rain on the danfo roof...",
	"project_notes":"Verses done; next: the bridge","project_done":false,
	"remember":["Tobi wrote the second verse of Lagos Rain"," "]}`

func activityMind() (*Mind, *fakeStore, *fakeSystem2, memory.Project, memory.Intention) {
	s2 := &fakeSystem2{formats: map[string]string{"activity": activityJSON}}
	m, s := lifeMind(s2)
	m.planned = "2026-10-29" // already planned today
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
	note := s2.requests[0].Note
	for _, want := range []string{"Write verse 2 of Lagos Rain", "Verse 1 done; next: verse 2", "Wake up to the horns..."} {
		if !strings.Contains(note, want) {
			t.Errorf("activity note is missing %q:\n%s", want, note)
		}
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
	d := s.decisions[len(s.decisions)-1]
	if d.Action != "activity" || d.PerceptID != s.percepts[0].ID || s.percepts[0].Source != lifeSource || s.facts[0].PerceptID != s.percepts[0].ID {
		t.Errorf("decision = %+v, percepts = %+v", d, s.percepts)
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

func TestJournalLooksBackAtTheDay(t *testing.T) {
	m, s, s2, _, _ := activityMind()
	s.intentions[0].Status, s.intentions[0].Result = memory.IntentionDone, "Lagos Rain, verse 2"
	s.works = append(s.works, memory.Work{ID: ulid.Make(), Kind: "lyrics", Title: "Lagos Rain, verse 2", CreatedAt: at(29, 10, 5)})
	journal := memory.Intention{ID: ulid.Make(), Subject: memory.SubjectSelf, Kind: memory.IntentionActivity,
		Text: journalText, Ability: journal.Name, DueAt: at(29, 22, 0), Status: memory.IntentionPending}
	s.intentions = append(s.intentions, journal)
	s2.formats["activity"] = `{"title":"29 Oct","kind":"notes","text":"Wrote verse 2 today.","project_notes":"","project_done":false,"remember":[]}`

	m.tick(context.Background(), at(29, 22, 0))
	note := s2.requests[0].Note
	if !strings.Contains(note, "journal") || !strings.Contains(note, "10:00 Write verse 2 of Lagos Rain: done") ||
		!strings.Contains(note, "lyrics: Lagos Rain, verse 2") {
		t.Errorf("journal note:\n%s", note)
	}
	if w := s.works[len(s.works)-1]; w.Kind != "journal" || w.Text != "Wrote verse 2 today." {
		t.Errorf("journal = %+v", w)
	}
}

// flakySystem2 fails its first request, then answers like fakeSystem2.
type flakySystem2 struct {
	fakeSystem2
	failed bool
}

func (f *flakySystem2) Reason(ctx context.Context, req system2.Request) (system2.Response, error) {
	if !f.failed {
		f.failed = true
		return system2.Response{}, errors.New("timeout")
	}
	return f.fakeSystem2.Reason(ctx, req)
}

func TestFailedActivityIsRetriedWithItsOwnPercept(t *testing.T) {
	m, s, _, _, i := activityMind()
	flaky := &flakySystem2{fakeSystem2: fakeSystem2{formats: map[string]string{"activity": activityJSON}}}
	m.System2 = flaky
	m.tick(context.Background(), at(29, 10, 0))
	if got := s.intention(i.ID); got.Status != memory.IntentionPending {
		t.Fatalf("after a failure: %+v, want still pending", got)
	}
	m.tick(context.Background(), at(29, 10, 1))
	if got := s.intention(i.ID); got.Status != memory.IntentionDone {
		t.Fatalf("after the retry: %+v", got)
	}
	if len(s.percepts) != 2 || s.percepts[0].ExternalID == s.percepts[1].ExternalID {
		t.Fatalf("percepts = %+v, want one per attempt", s.percepts)
	}
	last := s.decisions[len(s.decisions)-1]
	if s.facts[0].PerceptID != s.percepts[1].ID || last.PerceptID != s.percepts[1].ID {
		t.Errorf("the retry's fact and decision don't point at its saved percept")
	}
}

func TestWhatItMakesComesFromItsLife(t *testing.T) {
	m, _, s2, _, _ := activityMind()
	m.Life.Makes = []self.Craft{{Kind: "poem", About: "a short poem"}}
	m.Day.ActivityPrompt.Text = "You make:\n{makes}\nkind: one of {kinds}."
	m.tick(context.Background(), at(29, 10, 0))

	req := s2.requests[0]
	if !strings.Contains(req.Instructions, "- poem: a short poem") || !strings.Contains(req.Instructions, "kind: one of notes, writing, poem.") {
		t.Errorf("instructions:\n%s", req.Instructions)
	}
	kinds := req.Format.Schema["properties"].(map[string]any)["kind"].(map[string]any)["enum"].([]string)
	if strings.Join(kinds, ",") != "notes,writing,poem" {
		t.Errorf("kinds = %v", kinds)
	}
}

// shippedLives are the lives in lives/, each filled in for an owner, Sam.
func shippedLives(t *testing.T) []self.Life {
	t.Helper()
	ts, err := self.Templates("../../lives")
	if err != nil {
		t.Fatal(err)
	}
	var lives []self.Life
	for _, tmpl := range ts {
		files, err := tmpl.Render(self.Owner{Name: "Sam", Relation: tmpl.Relations[0], Account: owner})
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "life.yaml")
		if err := os.WriteFile(path, files["life.yaml"], 0o600); err != nil {
			t.Fatal(err)
		}
		life, err := self.Load(path)
		if err != nil {
			t.Fatal(err)
		}
		lives = append(lives, life)
	}
	return lives
}

func TestShippedLifePromptsAreFilledIn(t *testing.T) {
	for _, life := range shippedLives(t) {
		m, _ := lifeMind(&fakeSystem2{})
		m.Life = life
		shippedLifePrompts(t, m, life)
	}
}

func shippedLifePrompts(t *testing.T, m *Mind, life self.Life) {
	for _, path := range []string{"../../prompts/plan.md", "../../prompts/activity.md", "../../prompts/review.md", "../../prompts/learn.md"} {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		got := m.lifeInstructions(Prompt{Text: string(raw)})
		if i := strings.IndexAny(got, "{}"); i >= 0 {
			t.Errorf("%s: placeholder left at %q", path, got[max(0, i-20):min(len(got), i+20)])
		}
		for _, c := range life.Crafts() {
			if !strings.Contains(got, "- "+c.Kind+": "+c.About) {
				t.Errorf("%s doesn't say it makes %s", path, c.Kind)
			}
		}
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
	if len(s.calls) != 1 || !strings.Contains(s2.requests[0].Note, "round 1 of 4") || !strings.Contains(s2.requests[0].Note, "until 11:30") {
		t.Errorf("calls %d; note:\n%s", len(s.calls), s2.requests[0].Note)
	}

	m.planDay(ctx, at(29, 9, 0)) // still the first round
	if len(s2.requests) != 1 {
		t.Fatal("planned the same round again")
	}

	s.works = append(s.works, memory.Work{ID: ulid.Make(), Kind: "notes", Title: "Customer discovery notes", CreatedAt: at(29, 8, 40)})
	m.planDay(ctx, at(29, 11, 31))
	note := s2.requests[1].Note
	for _, want := range []string{"round 2 of 4", "until 15:00", "Leave calls empty", "notes: Customer discovery notes"} {
		if !strings.Contains(note, want) {
			t.Errorf("round 2 note is missing %q:\n%s", want, note)
		}
	}
	if got := due(at(29, 11, 30), at(29, 15, 0)); len(got) != 1 || got[0] != "Write outreach email" {
		t.Errorf("round 2 = %q", got)
	}
	if len(s.calls) != 1 {
		t.Error("a later round made calls")
	}

	// Restarted mid-round: it doesn't plan this round again, and goes on with the next.
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

func TestAPracticeThatMeetsItsStageMovesThePlanOn(t *testing.T) {
	ctx := context.Background()
	plan := memory.LearningPlan{ID: ulid.Make(), GoalID: ulid.Make(), Skill: "sales and marketing", Status: memory.PlanActive, Stages: []memory.Stage{
		{N: 0, Learn: "start with the customer", Practice: "a one-page customer note", DoneWhen: "a note naming the user, problem and workaround"},
		{N: 1, Learn: "B2B outreach", Practice: "two intro emails", DoneWhen: "two emails with one clear request"},
	}}
	for name, c := range map[string]struct {
		stageDone string
		want      int
	}{"met": {plan.ID.String(), 1}, "not yet": {"", 0}, "another plan": {ulid.Make().String(), 0}} {
		m, s, s2, _, _ := activityMind()
		p := plan
		p.Stages = append([]memory.Stage{}, plan.Stages...)
		s.plans = []memory.LearningPlan{p}
		s2.formats["activity"] = `{"title":"Customer note","kind":"notes","text":"The user is a clinic manager...","project_notes":"","project_done":false,
			"remember":[],"lessons":[],"ask_for_review":false,"review_question":"","stage_done":"` + c.stageDone + `"}`
		m.tick(ctx, at(29, 10, 0))
		note := s2.requests[0].Note
		if !strings.Contains(note, "[plan "+plan.ID.String()+"] sales and marketing, stage 1 of 2: practise a one-page customer note.") {
			t.Errorf("%s: note doesn't show where the plan is:\n%s", name, note)
		}
		if s.plans[0].Stage != c.want {
			t.Errorf("%s: stage = %d, want %d", name, s.plans[0].Stage, c.want)
		}
	}
}

func TestTimeWordsFollowPace(t *testing.T) {
	m, _ := lifeMind(&fakeSystem2{})
	in := m.lifeInstructions(Prompt{Text: "due {goal_due}; practise for {practice_time}"})
	if in != "due within weeks; practise for a day or two" {
		t.Errorf("pace 1: %q", in)
	}
	m.Day.Pace = 10
	in = m.lifeInstructions(Prompt{Text: "due {goal_due}; practise for {practice_time}"})
	if !strings.Contains(in, "about 10 times faster") || !strings.Contains(in, "takes you about 3 days") || !strings.Contains(in, "a round or two") {
		t.Errorf("pace 10: %q", in)
	}
	for _, path := range []string{"../../prompts/review.md", "../../prompts/learn.md"} {
		raw, _ := os.ReadFile(path)
		if got := m.lifeInstructions(Prompt{Text: string(raw)}); strings.Contains(got, "{goal_due}") || strings.Contains(got, "{practice_time}") {
			t.Errorf("%s: a pace placeholder is left", path)
		}
	}
}
