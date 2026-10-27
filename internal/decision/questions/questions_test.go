package questions

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"minime/internal/decision"
)

func TestShippedSetsAreValid(t *testing.T) {
	sets, err := LoadDir("../../../decisions")
	if err != nil {
		t.Fatal(err)
	}
	s, ok := sets["incoming_message"]
	if !ok {
		t.Fatal("incoming_message set missing")
	}
	if s.ID != "incoming_message" || s.Version < 1 || s.Primary != "intent" {
		t.Errorf("set = %+v", s)
	}
	if f, ok := sets["follow_up"]; !ok || f.Primary != "answers_previous" {
		t.Errorf("follow_up set = %+v", f)
	}
	if l, ok := sets["life_area"]; !ok || l.Primary != "" || l.Thresholds.Label == 0 {
		t.Errorf("life_area set = %+v", l)
	}
}

func TestLabels(t *testing.T) {
	s := Set{Thresholds: Thresholds{Label: 0.15}}
	answers := map[string]decision.Answer{
		"area": {Type: decision.QuestionChoice, Choice: "home", Probabilities: map[string]float64{
			"home": 0.71, "family": 0.21, "other": 0.05, "work": 0.03}},
		"gone": {Type: decision.QuestionChoice, Refused: true},
	}
	if got := s.Labels(answers, "area"); !slices.Equal(got, []string{"home", "family"}) {
		t.Errorf("labels = %v", got)
	}
	if got := s.Labels(answers, "gone"); got != nil {
		t.Errorf("refused labels = %v", got)
	}
	if got := s.Labels(answers, "missing"); got != nil {
		t.Errorf("missing labels = %v", got)
	}
}

func TestValidationErrors(t *testing.T) {
	cases := map[string]string{
		"unknown key": `
id: x
version: 1
primary: a
typo: true
questions: [{name: a, type: choice, instructions: i, choices: [{value: p}, {value: q}]}]
thresholds: {act: 0.9, ask: 0.5}`,
		"primary is a score": `
id: x
version: 1
primary: a
questions: [{name: a, type: score, instructions: i, levels: [{label: lo}, {label: hi}]}]
thresholds: {act: 0.9, ask: 0.5}`,
		"one choice": `
id: x
version: 1
primary: a
questions: [{name: a, type: choice, instructions: i, choices: [{value: p}]}]
thresholds: {act: 0.9, ask: 0.5}`,
		"ask above act": `
id: x
version: 1
primary: a
questions: [{name: a, type: choice, instructions: i, choices: [{value: p}, {value: q}]}]
thresholds: {act: 0.5, ask: 0.9}`,
		"duplicate name": `
id: x
version: 1
primary: a
questions:
  - {name: a, type: choice, instructions: i, choices: [{value: p}, {value: q}]}
  - {name: a, type: predicate, instructions: i}
thresholds: {act: 0.9, ask: 0.5}`,
		"bad type": `
id: x
version: 1
primary: a
questions:
  - {name: a, type: choice, instructions: i, choices: [{value: p}, {value: q}]}
  - {name: b, type: noul, instructions: i}
thresholds: {act: 0.9, ask: 0.5}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "set.yaml")
			if err := os.WriteFile(path, []byte(strings.TrimSpace(body)), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadFile(path); err == nil {
				t.Fatal("expected a validation error")
			}
		})
	}
}
