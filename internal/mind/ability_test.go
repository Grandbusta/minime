package mind

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/oklog/ulid/v2"

	"github.com/Grandbusta/minime/internal/ability"
	"github.com/Grandbusta/minime/internal/ability/journal"
	"github.com/Grandbusta/minime/internal/memory"
)

// fakeAbility looks things up from a map.
type fakeAbility struct {
	name     string
	describe string
	found    map[string]string
	acts     []ability.Act
}

func (f *fakeAbility) Name() string       { return f.name }
func (f *fakeAbility) Describe() string   { return f.describe }
func (f *fakeAbility) Risk() ability.Risk { return ability.ReadsWeb }

func (f *fakeAbility) Prepare(_ context.Context, a ability.Act) (ability.Material, error) {
	f.acts = append(f.acts, a)
	text, ok := f.found[a.Query()]
	if !ok {
		return ability.Material{}, errors.New("not found")
	}
	return ability.Material{Note: ability.Fence(text), Project: a.Project}, nil
}

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
