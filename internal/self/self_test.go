package self

import "testing"

func TestLoad(t *testing.T) {
	l, err := ParseLife([]byte("name: Tobi\nstory:\n  - Tobi is 29 years old.\n  - '  '\n  - Tobi is a Catholic.\n"))
	if err != nil {
		t.Fatal(err)
	}
	if l.Name != "Tobi" || len(l.Story) != 2 || l.Story[1] != "Tobi is a Catholic." {
		t.Errorf("life = %+v", l)
	}
}

func TestLoadRequiresAStory(t *testing.T) {
	for name, body := range map[string]string{
		"empty template": "name: \"\"\nstory: []\n",
		"no story":       "name: Tobi\n",
		"unknown key":    "name: Tobi\nstory: [x]\nage: 3\n",
	} {
		if _, err := ParseLife([]byte(body)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestFamily(t *testing.T) {
	l, err := ParseLife([]byte(`
name: Tobi
story: [Tobi is 29.]
family:
  - {name: Sam, relation: father, account: "telegram:42"}
  - {name: " Ada ", relation: " cousin "}
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Family) != 2 || l.Family[0].Account != "telegram:42" || l.Family[1] != (Relative{Name: "Ada", Relation: "cousin", Circle: "family"}) {
		t.Errorf("family = %+v", l.Family)
	}

	for name, family := range map[string]string{
		"no relation":    "  - {name: Ada}\n",
		"no name":        "  - {relation: cousin}\n",
		"shared account": "  - {name: Sam, relation: father, account: a}\n  - {name: Bo, relation: uncle, account: a}\n",
	} {
		if _, err := ParseLife([]byte("name: Tobi\nstory: [x]\nfamily:\n" + family)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}
