// Package questions loads question sets: versioned System 1 questions plus the
// thresholds the router uses, kept as data in decisions/*.yaml.
package questions

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"gopkg.in/yaml.v3"

	"minime/internal/decision"
	"minime/internal/providers/system1"
)

type Set struct {
	ID      string `yaml:"id"`
	Version int    `yaml:"version"`
	// Primary is the question the router acts on (a choice or a predicate);
	// the others add context. Empty for sets that only label, like life_area.
	Primary    string             `yaml:"primary"`
	Questions  []system1.Question `yaml:"questions"`
	Thresholds Thresholds         `yaml:"thresholds"`
}

// Thresholds on the primary answer's confidence (a predicate's probability):
// ≥ Act → clear; ≥ Ask → fairly sure; below Ask → unsure.
type Thresholds struct {
	Act float64 `yaml:"act"`
	Ask float64 `yaml:"ask"`
	// Label: every option of a choice with at least this probability counts
	// as a label, so one choice question can give several (e.g. home + family).
	Label float64 `yaml:"label"`
}

// Ref is how decisions record which set (and version) decided, e.g. "incoming_message@1".
func (s Set) Ref() string { return fmt.Sprintf("%s@%d", s.ID, s.Version) }

// LoadDir loads every *.yaml file in dir, keyed by set ID.
func LoadDir(dir string) (map[string]Set, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("no question sets (*.yaml) in %s", dir)
	}
	sets := make(map[string]Set, len(paths))
	for _, p := range paths {
		s, err := LoadFile(p)
		if err != nil {
			return nil, err
		}
		if _, dup := sets[s.ID]; dup {
			return nil, fmt.Errorf("question set %q is defined twice (%s)", s.ID, p)
		}
		sets[s.ID] = s
	}
	return sets, nil
}

func LoadFile(path string) (Set, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Set{}, err
	}
	var s Set
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true) // a misspelled key is an error, not a silently ignored field
	if err := dec.Decode(&s); err != nil {
		return Set{}, fmt.Errorf("%s: %w", path, err)
	}
	if err := s.Validate(); err != nil {
		return Set{}, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

func (s Set) Validate() error {
	if s.ID == "" || s.Version < 1 {
		return fmt.Errorf("id and version (≥ 1) are required")
	}
	if n := len(s.Questions); n < 1 || n > 400 {
		return fmt.Errorf("need 1–400 questions, have %d", n)
	}
	t := s.Thresholds
	if t.Ask < 0 || t.Ask > t.Act || t.Act > 1 {
		return fmt.Errorf("thresholds must satisfy 0 ≤ ask ≤ act ≤ 1, have ask %v, act %v", t.Ask, t.Act)
	}
	if t.Label < 0 || t.Label > 1 {
		return fmt.Errorf("thresholds.label must be between 0 and 1, have %v", t.Label)
	}
	names := make(map[string]bool, len(s.Questions))
	for _, q := range s.Questions {
		if err := validateQuestion(q); err != nil {
			return fmt.Errorf("question %q: %w", q.Name, err)
		}
		if names[q.Name] {
			return fmt.Errorf("question %q appears twice", q.Name)
		}
		names[q.Name] = true
	}
	if s.Primary == "" {
		return nil
	}
	for _, q := range s.Questions {
		if q.Name == s.Primary {
			if q.Type == decision.QuestionScore {
				return fmt.Errorf("primary question %q must be a choice or a predicate", s.Primary)
			}
			return nil
		}
	}
	return fmt.Errorf("primary question %q is not in the set", s.Primary)
}

// Labels returns the options of choice question name whose probability is at
// least the set's label threshold, most likely first.
func (s Set) Labels(answers map[string]decision.Answer, name string) []string {
	a, ok := answers[name]
	if !ok || a.Refused {
		return nil
	}
	var labels []string
	for v, p := range a.Probabilities {
		if p >= s.Thresholds.Label {
			labels = append(labels, v)
		}
	}
	sort.Slice(labels, func(i, j int) bool {
		pi, pj := a.Probabilities[labels[i]], a.Probabilities[labels[j]]
		return pi > pj || (pi == pj && labels[i] < labels[j])
	})
	return labels
}

func validateQuestion(q system1.Question) error {
	if q.Name == "" || q.Instructions == "" {
		return fmt.Errorf("name and instructions are required")
	}
	switch q.Type {
	case decision.QuestionPredicate:
		if len(q.Choices) > 0 || len(q.Levels) > 0 {
			return fmt.Errorf("a predicate takes no choices or levels")
		}
	case decision.QuestionChoice:
		if n := len(q.Choices); n < 2 || n > 255 {
			return fmt.Errorf("need 2–255 choices, have %d", n)
		}
		seen := make(map[string]bool, len(q.Choices))
		for _, c := range q.Choices {
			if c.Value == "" || seen[c.Value] {
				return fmt.Errorf("choice values must be non-empty and unique (%q)", c.Value)
			}
			seen[c.Value] = true
		}
	case decision.QuestionScore:
		if n := len(q.Levels); n < 2 || n > 10 {
			return fmt.Errorf("need 2–10 levels, have %d", n)
		}
		for _, l := range q.Levels {
			if l.Label == "" {
				return fmt.Errorf("every level needs a label")
			}
		}
	default:
		return fmt.Errorf("unknown type %q (want predicate, choice or score)", q.Type)
	}
	return nil
}
