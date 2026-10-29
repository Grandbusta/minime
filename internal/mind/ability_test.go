package mind

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/oklog/ulid/v2"

	"minime/internal/ability"
	"minime/internal/ability/journal"
	"minime/internal/memory"
)

// fakeAbility looks things up from a map; with continues, it also carries on
// projects of kind "reading", moving them on and finishing them.
type fakeAbility struct {
	name      string
	describe  string
	found     map[string]string
	continues bool
	acts      []ability.Act
}

func (f *fakeAbility) Name() string       { return f.name }
func (f *fakeAbility) Describe() string   { return f.describe }
func (f *fakeAbility) Risk() ability.Risk { return ability.ReadsWeb }

func (f *fakeAbility) Prepare(_ context.Context, a ability.Act) (ability.Material, error) {
	f.acts = append(f.acts, a)
	if a.Project != nil && f.continues {
		p := *a.Project
		p.Position += 10
		return ability.Material{Note: "\n\nToday's pages.", Project: &p, Done: true}, nil
	}
	text, ok := f.found[a.Query()]
	if !ok {
		return ability.Material{}, errors.New("not found")
	}
	return ability.Material{Note: ability.Fence(text), Project: a.Project}, nil
}

type continuing struct{ *fakeAbility }

func (c continuing) Continues(p memory.Project) bool { return p.Kind == "reading" }

const notesJSON = `{"title":"Polyrhythm notes","kind":"notes","text":"Three against two.",
	"project_notes":"Read about it","project_done":false,"remember":[]}`

func abilityMind(abs ...ability.Ability) (*Mind, *fakeStore, *fakeSystem2) {
	s2 := &fakeSystem2{formats: map[string]string{"activity": notesJSON}}
	m, s := lifeMind(s2)
	m.planned = "2026-10-29"
	m.Abilities = ability.NewSet(append([]ability.Ability{journal.Journal{Store: s}}, abs...)...)
	return m, s, s2
}

func activity(text, abilityName, query string) memory.Intention {
	i := memory.Intention{ID: ulid.Make(), Subject: memory.SubjectSelf, Kind: memory.IntentionActivity, Text: text,
		Ability: abilityName, DueAt: at(29, 10, 0), Status: memory.IntentionPending}
	if query != "" {
		i.Args = map[string]string{"query": query}
	}
	return i
}

func TestActivityUsesItsAbility(t *testing.T) {
	look := &fakeAbility{name: "look_up", describe: "look it up", found: map[string]string{"polyrhythm": "Two rhythms at once."}}
	m, s, s2 := abilityMind(look)
	s.intentions = []memory.Intention{activity("Learn about polyrhythm", "look_up", "polyrhythm")}
	m.tick(context.Background(), at(29, 10, 0))

	if len(look.acts) != 1 || look.acts[0].Query() != "polyrhythm" || look.acts[0].Zone != lagos {
		t.Fatalf("acts = %+v", look.acts)
	}
	if note := s2.requests[0].Note; !strings.Contains(note, "Learn about polyrhythm") || !strings.Contains(note, "<<<TEXT\nTwo rhythms at once.") {
		t.Errorf("note:\n%s", note)
	}
	if i := s.intentions[0]; i.Status != memory.IntentionDone || s.works[len(s.works)-1].Kind != "notes" {
		t.Errorf("intention %+v, work %+v", i, s.works[len(s.works)-1])
	}
}

func TestAProjectCarriesItsAbilityOn(t *testing.T) {
	reader := &fakeAbility{name: "read_book", describe: "read", continues: true}
	m, s, s2 := abilityMind(continuing{reader})
	book := memory.Project{ID: ulid.Make(), Title: "Pride and Prejudice", Kind: "reading", Status: memory.ProjectActive}
	s.projects[book.ID] = book
	i := activity("Keep reading", "", "")
	i.ProjectID = book.ID
	s.intentions = []memory.Intention{i}
	m.tick(context.Background(), at(29, 10, 0))

	if len(reader.acts) != 1 || !strings.Contains(s2.requests[0].Note, "Today's pages.") {
		t.Fatalf("the book wasn't read on: %d acts", len(reader.acts))
	}
	if p := s.projects[book.ID]; p.Position != 10 || p.Status != memory.ProjectDone {
		t.Errorf("project = %+v, want moved on and done", p)
	}
}

func TestAbilityThatFailsOrIsMissingMissesTheActivity(t *testing.T) {
	look := &fakeAbility{name: "look_up", describe: "look it up"}
	m, s, s2 := abilityMind(look)
	s.intentions = []memory.Intention{activity("Look up a thing", "look_up", "a modern novel"), activity("Search the web", "web_search", "news")}
	m.tick(context.Background(), at(29, 10, 0))

	if i := s.intentions[0]; i.Status != memory.IntentionMissed || !strings.Contains(i.Result, "a modern novel") {
		t.Errorf("failed ability: %+v", i)
	}
	if i := s.intentions[1]; i.Status != memory.IntentionMissed || !strings.Contains(i.Result, "web_search") {
		t.Errorf("missing ability: %+v", i)
	}
	if len(s2.requests) != 0 {
		t.Errorf("system 2 was asked anyway: %d requests", len(s2.requests))
	}
}

func TestPlanCanChooseAnAbility(t *testing.T) {
	look := &fakeAbility{name: "look_up", describe: "look a topic up; query: the topic"}
	s2 := &fakeSystem2{formats: map[string]string{"day_plan": `{"activities":[
		{"at":"10:00","what":"Learn about polyrhythm","project_id":"","new_project_title":"","project_kind":"","ability":"look_up","query":"polyrhythm"},
		{"at":"11:00","what":"Write my journal early","project_id":"","new_project_title":"","project_kind":"","ability":"journal","query":""},
		{"at":"12:00","what":"Write lyrics","project_id":"","new_project_title":"","project_kind":"","ability":"film","query":"x"}]}`}}
	m, s := lifeMind(s2)
	m.Abilities = ability.NewSet(journal.Journal{Store: s}, look)
	m.tick(context.Background(), at(29, 9, 0))

	note := s2.requests[0].Note
	if !strings.Contains(note, "- look_up: look a topic up; query: the topic") || strings.Contains(note, "- journal:") {
		t.Errorf("the plan wasn't told its abilities:\n%s", note)
	}
	got := map[string]memory.Intention{}
	for _, i := range s.intentions {
		got[i.Text] = i
	}
	if i := got["Learn about polyrhythm"]; i.Ability != "look_up" || i.Args["query"] != "polyrhythm" {
		t.Errorf("look-up intention = %+v", i)
	}
	for _, text := range []string{"Write my journal early", "Write lyrics"} {
		if i := got[text]; i.Ability != "" || i.Args != nil {
			t.Errorf("an ability the planner can't choose was kept: %+v", i)
		}
	}
	if i := got[journalText]; i.Ability != journal.Name {
		t.Errorf("journal intention = %+v", i)
	}
}

func TestPlanWithoutAbilitiesSaysSo(t *testing.T) {
	s2 := &fakeSystem2{formats: map[string]string{"day_plan": `{"activities":[]}`}}
	m, _ := lifeMind(s2)
	m.tick(context.Background(), at(29, 9, 0))
	if !strings.Contains(s2.requests[0].Note, "you can only write") {
		t.Errorf("note:\n%s", s2.requests[0].Note)
	}
}
