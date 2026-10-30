// Package self loads the agent's life story: who it is, written by its creator.
package self

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Life is the agent's life story. The agent never contradicts it; anything
// else about itself it decides when it comes up, and remembers.
type Life struct {
	Name  string   `yaml:"name"`
	Story []string `yaml:"story"`
	// Timezone is overridden by the config's initiative.timezone when set.
	Timezone    string   `yaml:"timezone"`
	Personality []string `yaml:"personality"`
	Routine     []string `yaml:"routine"`
	Makes       []Craft  `yaml:"makes"`
	// Dreams, Skills and Family are copied into memory the first time; after
	// that memory wins.
	Dreams []string   `yaml:"dreams"`
	Skills []Skill    `yaml:"skills"`
	Family []Relative `yaml:"family"`
}

type Craft struct {
	Kind  string `yaml:"kind"` // snake_case; works are filed under it
	About string `yaml:"about"`
}

var BuiltIn = []Craft{
	{Kind: "notes", About: "notes on something you're learning or thinking through"},
	{Kind: "writing", About: "a short piece of writing of any other kind"},
}

// Reserved are kinds an ability sets itself (the journal, reading notes).
var Reserved = []string{"journal", "reading_notes", "review"}

var kindName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func (l Life) Crafts() []Craft { return append(append([]Craft{}, BuiltIn...), l.Makes...) }

type Skill struct {
	Name    string   `yaml:"name"`
	Level   int      `yaml:"level"` // 1 (beginner) to 10 (master)
	ShownBy []string `yaml:"shown_by"`
}

type Relative struct {
	Name     string `yaml:"name"`
	Relation string `yaml:"relation"`
	// Account, when set, is how the agent knows them, not by name.
	Account string `yaml:"account"`
	Circle  string `yaml:"circle"` // empty: family
}

var Circles = []string{"family", "friend", "school", "work", "community"}

func CircleOf(relation string) string {
	switch r := strings.ToLower(relation); {
	case strings.Contains(r, "friend"):
		return "friend"
	case strings.Contains(r, "mentor"), strings.Contains(r, "teacher"), strings.Contains(r, "tutor"):
		return "school"
	}
	return "family"
}

func Load(path string) (Life, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Life{}, fmt.Errorf("read life story: %w", err)
	}
	var l Life
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&l); err != nil {
		return Life{}, fmt.Errorf("%s: %w", path, err)
	}
	l.Name, l.Timezone = strings.TrimSpace(l.Name), strings.TrimSpace(l.Timezone)
	if l.Timezone != "" {
		if _, err := time.LoadLocation(l.Timezone); err != nil {
			return Life{}, fmt.Errorf("%s: timezone must be a time zone like Africa/Lagos, have %q", path, l.Timezone)
		}
	}
	l.Story = nonEmpty(l.Story)
	l.Personality = nonEmpty(l.Personality)
	l.Routine = nonEmpty(l.Routine)
	if l.Name == "" || len(l.Story) == 0 {
		return Life{}, errors.New(path + ": write the agent's name and at least one line of its life story first")
	}
	kinds := map[string]bool{}
	for _, k := range Reserved {
		kinds[k] = true
	}
	for _, c := range BuiltIn {
		kinds[c.Kind] = true
	}
	for i, c := range l.Makes {
		c.Kind, c.About = strings.TrimSpace(c.Kind), strings.TrimSpace(c.About)
		switch {
		case !kindName.MatchString(c.Kind) || c.About == "":
			return Life{}, fmt.Errorf("%s: makes %d needs a kind in snake_case (like beat_idea) and what it is (about)", path, i+1)
		case kinds[c.Kind]:
			return Life{}, fmt.Errorf("%s: makes %q is already a kind of work", path, c.Kind)
		}
		kinds[c.Kind] = true
		l.Makes[i] = c
	}
	l.Dreams = nonEmpty(l.Dreams)
	skills := map[string]bool{}
	for i, sk := range l.Skills {
		sk.Name = strings.TrimSpace(sk.Name)
		switch {
		case sk.Name == "" || sk.Level < 1 || sk.Level > 10:
			return Life{}, fmt.Errorf("%s: skill %d needs a name and a level from 1 to 10", path, i+1)
		case skills[strings.ToLower(sk.Name)]:
			return Life{}, fmt.Errorf("%s: skill %q is listed twice", path, sk.Name)
		}
		skills[strings.ToLower(sk.Name)] = true
		for j, k := range sk.ShownBy {
			k = strings.TrimSpace(k)
			if !kinds[k] || k == "journal" || k == "review" {
				return Life{}, fmt.Errorf("%s: skill %q is shown by %q, which isn't a kind of work it makes", path, sk.Name, k)
			}
			sk.ShownBy[j] = k
		}
		l.Skills[i] = sk
	}
	accounts := map[string]bool{}
	for i, r := range l.Family {
		r.Name, r.Relation, r.Account, r.Circle = strings.TrimSpace(r.Name), strings.TrimSpace(r.Relation), strings.TrimSpace(r.Account),
			strings.ToLower(strings.TrimSpace(r.Circle))
		if r.Circle == "" {
			r.Circle = "family"
		}
		if !slices.Contains(Circles, r.Circle) {
			return Life{}, fmt.Errorf("%s: %s's circle must be one of %s, have %q", path, r.Name, strings.Join(Circles, ", "), r.Circle)
		}
		if r.Name == "" || r.Relation == "" {
			return Life{}, fmt.Errorf("%s: family member %d needs a name and a relation", path, i+1)
		}
		if r.Account != "" {
			if accounts[r.Account] {
				return Life{}, fmt.Errorf("%s: account %s belongs to two family members", path, r.Account)
			}
			accounts[r.Account] = true
		}
		l.Family[i] = r
	}
	return l, nil
}

func nonEmpty(lines []string) []string {
	var out []string
	for _, s := range lines {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
