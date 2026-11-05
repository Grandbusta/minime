package self

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Judgment is copied into memory the first time as proposed; only what the
// owner approves in the console is used.
type Judgment struct {
	// Priorities are most important first: when principles clash, the higher wins.
	Priorities []string    `yaml:"priorities"`
	Boundaries []string    `yaml:"boundaries"`
	Principles []Principle `yaml:"principles"`
	Cases      []Case      `yaml:"cases"`
}

type Principle struct {
	Text  string   `yaml:"text"`
	Types []string `yaml:"types"`
	Cases []string `yaml:"cases"` // keys of the cases behind it
}

type Case struct {
	Key       string   `yaml:"key"`
	Type      string   `yaml:"type"`
	Situation string   `yaml:"situation"`
	Options   []string `yaml:"options"`
	Choice    string   `yaml:"choice"`
	Reason    string   `yaml:"reason"` // the owner's own words; empty when they gave none
}

var DecisionTypes = []string{"general", "engineering", "product", "autonomy", "life"}

// ParseJudgment reads and checks a judgment (judgment.yaml); empty is none.
func ParseJudgment(raw []byte) (Judgment, error) {
	const section = "judgment"
	var j Judgment
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&j); err != nil && !errors.Is(err, io.EOF) {
		return Judgment{}, fmt.Errorf("%s: %w", section, err)
	}
	keys := map[string]bool{}
	for i, c := range j.Cases {
		c.Key, c.Type = strings.TrimSpace(c.Key), strings.TrimSpace(c.Type)
		c.Situation, c.Choice, c.Reason = strings.TrimSpace(c.Situation), strings.TrimSpace(c.Choice), strings.TrimSpace(c.Reason)
		c.Options = nonEmpty(c.Options)
		switch {
		case c.Key == "" || c.Situation == "" || c.Choice == "":
			return Judgment{}, fmt.Errorf("%s: case %d needs a key, a situation and a choice", section, i+1)
		case keys[c.Key]:
			return Judgment{}, fmt.Errorf("%s: case %q is listed twice", section, c.Key)
		case !slices.Contains(DecisionTypes, c.Type):
			return Judgment{}, fmt.Errorf("%s: case %q has type %q; want one of %s", section, c.Key, c.Type, strings.Join(DecisionTypes, ", "))
		}
		keys[c.Key] = true
		j.Cases[i] = c
	}
	texts := map[string]bool{}
	j.Priorities, j.Boundaries = nonEmpty(j.Priorities), nonEmpty(j.Boundaries)
	for _, t := range append(append([]string{}, j.Priorities...), j.Boundaries...) {
		if texts[t] {
			return Judgment{}, fmt.Errorf("%s: %q is listed twice", section, t)
		}
		texts[t] = true
	}
	for i, p := range j.Principles {
		p.Text = strings.TrimSpace(p.Text)
		p.Types, p.Cases = nonEmpty(p.Types), nonEmpty(p.Cases)
		switch {
		case p.Text == "":
			return Judgment{}, fmt.Errorf("%s: principle %d has no text", section, i+1)
		case texts[p.Text]:
			return Judgment{}, fmt.Errorf("%s: principle %q is listed twice", section, p.Text)
		}
		texts[p.Text] = true
		for _, t := range p.Types {
			if !slices.Contains(DecisionTypes, t) {
				return Judgment{}, fmt.Errorf("%s: principle %q has type %q; want one of %s", section, p.Text, t, strings.Join(DecisionTypes, ", "))
			}
		}
		for _, k := range p.Cases {
			if !keys[k] {
				return Judgment{}, fmt.Errorf("%s: principle %q names case %q, which isn't listed", section, p.Text, k)
			}
		}
		j.Principles[i] = p
	}
	return j, nil
}
