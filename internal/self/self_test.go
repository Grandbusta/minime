package self

import (
	"os"
	"path/filepath"
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
