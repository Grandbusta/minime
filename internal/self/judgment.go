package self

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Judgment is how the agent's owner decides, so the agent can learn to decide
// the way they would: real decisions they made (cases), and the rules drawn
// from them (principles). Optional. Copied into memory the first time as
// proposed; the owner approves, edits or rejects each in the console, and
// only approved ones are used.
type Judgment struct {
	Principles []Principle `yaml:"principles"`
	Cases      []Case      `yaml:"cases"`
}

// Principle is a rule of the owner's, drawn from their decisions.
type Principle struct {
	Text  string   `yaml:"text"`
	Types []string `yaml:"types"` // kinds of decision it applies to
	Cases []string `yaml:"cases"` // keys of the cases behind it
}

// Case is one decision the owner made.
type Case struct {
	Key       string   `yaml:"key"` // short and unique, e.g. "no-x402"
	Type      string   `yaml:"type"`
	Situation string   `yaml:"situation"`
	Options   []string `yaml:"options"`
	Choice    string   `yaml:"choice"`
	Reason    string   `yaml:"reason"` // the owner's own words; empty when they gave none
}

// DecisionTypes are the kinds of decision judgment is filed under.
var DecisionTypes = []string{"general", "engineering", "product", "autonomy", "life"}

// LoadJudgment reads the owner's judgment. A missing file is no judgment.
func LoadJudgment(path string) (Judgment, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) || path == "" {
		return Judgment{}, nil
	}
	if err != nil {
		return Judgment{}, fmt.Errorf("read judgment: %w", err)
	}
	var j Judgment
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&j); err != nil && !errors.Is(err, io.EOF) {
		return Judgment{}, fmt.Errorf("%s: %w", path, err)
	}
	keys := map[string]bool{}
	for i, c := range j.Cases {
		c.Key, c.Type = strings.TrimSpace(c.Key), strings.TrimSpace(c.Type)
		c.Situation, c.Choice, c.Reason = strings.TrimSpace(c.Situation), strings.TrimSpace(c.Choice), strings.TrimSpace(c.Reason)
		c.Options = nonEmpty(c.Options)
		switch {
		case c.Key == "" || c.Situation == "" || c.Choice == "":
			return Judgment{}, fmt.Errorf("%s: case %d needs a key, a situation and a choice", path, i+1)
		case keys[c.Key]:
			return Judgment{}, fmt.Errorf("%s: case %q is listed twice", path, c.Key)
		case !slices.Contains(DecisionTypes, c.Type):
			return Judgment{}, fmt.Errorf("%s: case %q has type %q; want one of %s", path, c.Key, c.Type, strings.Join(DecisionTypes, ", "))
		}
		keys[c.Key] = true
		j.Cases[i] = c
	}
	texts := map[string]bool{}
	for i, p := range j.Principles {
		p.Text = strings.TrimSpace(p.Text)
		p.Types, p.Cases = nonEmpty(p.Types), nonEmpty(p.Cases)
		switch {
		case p.Text == "":
			return Judgment{}, fmt.Errorf("%s: principle %d has no text", path, i+1)
		case texts[p.Text]:
			return Judgment{}, fmt.Errorf("%s: principle %q is listed twice", path, p.Text)
		}
		texts[p.Text] = true
		for _, t := range p.Types {
			if !slices.Contains(DecisionTypes, t) {
				return Judgment{}, fmt.Errorf("%s: principle %q has type %q; want one of %s", path, p.Text, t, strings.Join(DecisionTypes, ", "))
			}
		}
		for _, k := range p.Cases {
			if !keys[k] {
				return Judgment{}, fmt.Errorf("%s: principle %q names case %q, which isn't listed", path, p.Text, k)
			}
		}
		j.Principles[i] = p
	}
	return j, nil
}
