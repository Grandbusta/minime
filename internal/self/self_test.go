package self

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "life.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad(t *testing.T) {
	l, err := Load(write(t, "name: Tobi\nstory:\n  - Tobi is 29 years old.\n  - '  '\n  - Tobi is a Catholic.\n"))
	if err != nil {
		t.Fatal(err)
	}
	if l.Name != "Tobi" || len(l.Story) != 2 || l.Story[1] != "Tobi is a Catholic." {
		t.Errorf("life = %+v", l)
	}
}

func TestPersonality(t *testing.T) {
	l, err := Load(write(t, "name: Tobi\nstory: [Tobi is 29.]\npersonality:\n  - Tobi is warm.\n  - ''\n  - ' Tobi asks follow-up questions. '\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Personality) != 2 || l.Personality[1] != "Tobi asks follow-up questions." {
		t.Errorf("personality = %q", l.Personality)
	}
}

func TestLoadRequiresAStory(t *testing.T) {
	for name, body := range map[string]string{
		"empty template": "name: \"\"\nstory: []\n",
		"no story":       "name: Tobi\n",
		"unknown key":    "name: Tobi\nstory: [x]\nage: 3\n",
	} {
		if _, err := Load(write(t, body)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestFamily(t *testing.T) {
	l, err := Load(write(t, `
name: Tobi
story: [Tobi is 29.]
family:
  - {name: Sam, relation: father, account: "telegram:42"}
  - {name: " Ada ", relation: " cousin "}
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Family) != 2 || l.Family[0].Account != "telegram:42" || l.Family[1] != (Relative{Name: "Ada", Relation: "cousin"}) {
		t.Errorf("family = %+v", l.Family)
	}

	for name, family := range map[string]string{
		"no relation":    "  - {name: Ada}\n",
		"no name":        "  - {relation: cousin}\n",
		"shared account": "  - {name: Sam, relation: father, account: a}\n  - {name: Bo, relation: uncle, account: a}\n",
	} {
		if _, err := Load(write(t, "name: Tobi\nstory: [x]\nfamily:\n"+family)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestShippedLifeStoryLoads(t *testing.T) {
	if _, err := Load("../../self/life.yaml"); err != nil {
		t.Fatal(err)
	}
}

func TestMakes(t *testing.T) {
	l, err := Load(write(t, "name: Amara\nstory: [Amara paints.]\nmakes:\n  - {kind: ' poem ', about: ' a short poem '}\n  - {kind: painting_study, about: a study for a painting}\n"))
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, c := range l.Crafts() {
		kinds = append(kinds, c.Kind)
	}
	if got := strings.Join(kinds, ","); got != "notes,writing,poem,painting_study" || l.Makes[0].About != "a short poem" {
		t.Errorf("crafts = %s, makes %+v", got, l.Makes)
	}
	for name, makes := range map[string]string{
		"no about": "  - {kind: poem}\n",
		"bad kind": "  - {kind: Big Poem, about: x}\n",
		"built in": "  - {kind: notes, about: x}\n",
		"reserved": "  - {kind: journal, about: x}\n",
		"twice":    "  - {kind: poem, about: x}\n  - {kind: poem, about: y}\n",
	} {
		if _, err := Load(write(t, "name: Amara\nstory: [x]\nmakes:\n"+makes)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestDreamsAndSkills(t *testing.T) {
	l, err := Load(write(t, "name: Amara\nstory: [Amara paints.]\nmakes:\n  - {kind: poem, about: a poem}\ndreams: [' Amara wants a show ', '']\n"+
		"skills:\n  - {name: ' poetry ', level: 4, shown_by: [poem, notes]}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Dreams) != 1 || l.Dreams[0] != "Amara wants a show" || l.Skills[0].Name != "poetry" || l.Skills[0].ShownBy[0] != "poem" {
		t.Errorf("life = %+v", l)
	}
	for name, skills := range map[string]string{
		"no level":       "  - {name: poetry}\n",
		"level too high": "  - {name: poetry, level: 11}\n",
		"twice":          "  - {name: poetry, level: 2}\n  - {name: Poetry, level: 3}\n",
		"unknown kind":   "  - {name: poetry, level: 2, shown_by: [sculpture]}\n",
		"journal":        "  - {name: poetry, level: 2, shown_by: [journal]}\n",
	} {
		if _, err := Load(write(t, "name: Amara\nstory: [x]\nmakes:\n  - {kind: poem, about: a poem}\nskills:\n"+skills)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
