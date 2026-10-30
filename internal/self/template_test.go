package self

import (
	"github.com/Grandbusta/minime/internal/config"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLives fills in every life in lives/ for every relation it allows, and
// checks the result is a life that loads, where it lives, with its owner.
func TestLives(t *testing.T) {
	ts, err := Templates("../../lives")
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != 5 {
		t.Errorf("%d lives; want 5", len(ts))
	}
	for _, tmpl := range ts {
		for _, rel := range tmpl.Relations {
			for _, account := range []string{"telegram:42", ""} {
				files, err := tmpl.Render(Owner{Name: "Sam", Relation: rel, Account: account})
				if err != nil {
					t.Fatalf("%s as %s: %v", tmpl.Key, rel, err)
				}
				dir := t.TempDir()
				for name, raw := range files {
					text := string(raw)
					for _, p := range []string{"{owner}", "{relation}", "{account}", "{circle}"} {
						if strings.Contains(text, p) {
							t.Errorf("%s/%s: %s is left", tmpl.Key, name, p)
						}
					}
					if strings.Contains(text, "—") {
						t.Errorf("%s/%s: an em dash", tmpl.Key, name)
					}
					os.WriteFile(filepath.Join(dir, name), raw, 0o600)
				}
				l, err := Load(filepath.Join(dir, "life.yaml"))
				if err != nil {
					t.Fatalf("%s as %s: %v", tmpl.Key, rel, err)
				}
				if l.Name != tmpl.Name || l.Timezone == "" || len(l.Dreams) == 0 || len(l.Skills) == 0 || len(l.Makes) == 0 {
					t.Errorf("%s: life = %+v", tmpl.Key, l)
				}
				if j, err := LoadJudgment(filepath.Join(dir, "judgment.yaml")); err != nil || len(j.Principles) < 3 || len(j.Cases) < 3 ||
					len(j.Priorities) < 3 || len(j.Boundaries) < 1 {
					t.Errorf("%s: judgment = %d principles, %d cases, %v; want a real start", tmpl.Key, len(j.Principles), len(j.Cases), err)
				} else if !strings.Contains(string(files["judgment.yaml"]), "ask Sam first") {
					t.Errorf("%s: judgment doesn't name its owner", tmpl.Key)
				}
				living(t, dir, l)
				owner := l.Family[0]
				if owner.Name != "Sam" || owner.Relation != rel || owner.Account != account || owner.Circle != CircleOf(rel) {
					t.Errorf("%s as %s: owner = %+v", tmpl.Key, rel, owner)
				}
				if !strings.Contains(strings.Join(l.Story, "\n"), "Sam is "+l.Name+"'s "+rel+".") {
					t.Errorf("%s: the story doesn't say who Sam is", tmpl.Key)
				}
			}
		}
	}
}

// living checks a person's living.yaml is read over the config, with what
// the agent needs to run.
func living(t *testing.T, dir string, l Life) {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "minime.yaml")
	os.WriteFile(cfgPath, []byte("self:\n  dir: "+dir+"\ndatabase:\n  url: postgres://x\n"), 0o600)
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("%s: %v", l.Name, err)
	}
	cfg.Initiative.Timezone = l.Timezone
	if _, err := cfg.Policy(); err != nil {
		t.Errorf("%s: autonomy: %v", l.Name, err)
	}
	if _, _, err := cfg.DailyLife.Times(); err != nil {
		t.Errorf("%s: %v", l.Name, err)
	}
	if _, _, err := cfg.DailyLife.ReviewTime(); err != nil {
		t.Errorf("%s: %v", l.Name, err)
	}
	if ts, err := cfg.Initiative.CheckInTimes(); err != nil || len(ts) == 0 || !cfg.DailyLife.Enabled || !cfg.Judgment.Calls {
		t.Errorf("%s: check-ins %v, %v; daily life %v; calls %v", l.Name, ts, err, cfg.DailyLife.Enabled, cfg.Judgment.Calls)
	}
}

func TestRenderRefuses(t *testing.T) {
	tmpl, err := LoadTemplate("../../lives/daniel")
	if err != nil {
		t.Fatal(err)
	}
	for name, o := range map[string]Owner{
		"no name":        {Relation: "father"},
		"a quote":        {Name: `Sam "the man"`, Relation: "father"},
		"not a relation": {Name: "Sam", Relation: "landlord"},
		"bad account":    {Name: "Sam", Relation: "father", Account: "telegram: 42"},
	} {
		if _, err := tmpl.Render(o); err == nil {
			t.Errorf("%s: rendered", name)
		}
	}
}

func TestCircleOf(t *testing.T) {
	for rel, want := range map[string]string{"father": "family", "older sister": "family", "mentor": "school", "friend": "friend",
		"Best Friend": "friend", "granddaughter": "family"} {
		if got := CircleOf(rel); got != want {
			t.Errorf("CircleOf(%q) = %q; want %q", rel, got, want)
		}
	}
}
