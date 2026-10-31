package mind

import (
	"context"
	"strings"
	"testing"

	"github.com/Grandbusta/minime/internal/ability"
)

// chained is a tool that can be followed by its server's tools.
type chained struct {
	*fakeAbility
	next *[]ability.Ability
}

func (c chained) Next() []ability.Ability { return *c.next }

func TestAToolCanBeFollowedUp(t *testing.T) {
	var server []ability.Ability
	search := chained{&fakeAbility{name: "notes.search", describe: "find files", found: map[string]string{`{"pattern":"*clinic*"}`: "/shared/clinic-notes.md"},
		risk: ability.UsesTools}, &server}
	read := chained{&fakeAbility{name: "notes.read", describe: "read a file", found: map[string]string{`{"path":"/shared/clinic-notes.md"}`: "Start with Lekki."},
		risk: ability.UsesTools}, &server}
	server = []ability.Ability{search, read}
	m, s, s2 := abilityMind(search, read)
	s2.formats["next_step"] = `{"ability":"notes.read","query":"{\"path\":\"/shared/clinic-notes.md\"}"}`
	s.intentions = append(s.intentions, activity("Read Busta's clinic notes", "notes.search", `{"pattern":"*clinic*"}`))
	m.tick(context.Background(), at(29, 10, 0))

	if len(search.acts) != 1 || len(read.acts) != maxSteps-1 {
		t.Fatalf("searched %d times, read %d times; want 1 and at most %d steps in all", len(search.acts), len(read.acts), maxSteps)
	}
	var made string
	for _, r := range s2.requests {
		if r.Format != nil && r.Format.Name == "activity" {
			made = r.Note
		}
	}
	if !strings.Contains(made, "/shared/clinic-notes.md") || !strings.Contains(made, "Start with Lekki.") {
		t.Errorf("what it wrote from lacks the search or the read:\n%s", made)
	}

	s2.formats["next_step"] = `{"ability":"","query":""}` // it has what it needs
	s.intentions = append(s.intentions, activity("Find Busta's clinic notes", "notes.search", `{"pattern":"*clinic*"}`))
	reads := len(read.acts)
	m.tick(context.Background(), at(29, 10, 1))
	if len(search.acts) != 2 || len(read.acts) != reads {
		t.Errorf("searched %d, read %d more", len(search.acts), len(read.acts)-reads)
	}
}
