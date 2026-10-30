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
	Story []string `yaml:"story"` // third person, with the agent's name
	// Timezone is where the agent lives, an IANA name like Africa/Lagos: its
	// days, reminders and check-ins are in it. The config's
	// initiative.timezone, when set, overrides it.
	Timezone string `yaml:"timezone"`
	// Personality is how the agent is with people: its temperament and how it
	// talks, third person with its name. Optional.
	Personality []string `yaml:"personality"`
	// Routine steers how the agent plans its days, third person with its name,
	// e.g. "Daniel makes music in the evenings". Optional; it plans the rest itself.
	Routine []string `yaml:"routine"`
	// Makes is what the agent makes in its own day, in writing: its crafts,
	// e.g. lyrics or poems. Optional; it can always write notes and short
	// pieces, and it keeps a journal.
	Makes []Craft `yaml:"makes"`
	// Dreams are who the agent wants to become, third person with its name,
	// e.g. "Daniel wants to become a producer people in Lagos know". Optional;
	// copied into memory the first time, then it may change them (asking its owner).
	Dreams []string `yaml:"dreams"`
	// Skills are where it starts: what it is good at, how good (1-10), and
	// which kinds of work show it. Optional; copied into memory the first
	// time, after which they grow from what it makes and its owner's feedback.
	Skills []Skill `yaml:"skills"`
	// Family the agent is born knowing, like anyone knows their parents and
	// cousins whether or not they ever talk to them, and anyone else it has
	// always known, like a mentor or an old friend (by Circle). Copied into
	// memory the first time; after that memory wins.
	Family []Relative `yaml:"family"`
}

// Craft is a kind of thing the agent makes.
type Craft struct {
	Kind  string `yaml:"kind"`  // a short name in snake_case, e.g. "beat_idea"; works are filed under it
	About string `yaml:"about"` // what it is and what a good one has, e.g. "a beat described precisely: tempo, key, drums"
}

// BuiltIn are the kinds of work every agent has: what Makes adds to.
var BuiltIn = []Craft{
	{Kind: "notes", About: "notes on something you're learning or thinking through"},
	{Kind: "writing", About: "a short piece of writing of any other kind"},
}

// Reserved are kinds an ability sets itself (the journal, reading notes).
var Reserved = []string{"journal", "reading_notes", "review"}

var kindName = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// Crafts is everything it makes: the built-in kinds, then Makes.
func (l Life) Crafts() []Craft { return append(append([]Craft{}, BuiltIn...), l.Makes...) }

// Skill is something the agent can get better at.
type Skill struct {
	Name    string   `yaml:"name"`     // e.g. "music production"
	Level   int      `yaml:"level"`    // 1 (beginner) to 10 (master)
	ShownBy []string `yaml:"shown_by"` // kinds of work that show it, e.g. [beat_idea]
}

// Relative is a member of the agent's family.
type Relative struct {
	Name     string `yaml:"name"`
	Relation string `yaml:"relation"` // to the agent: father, mother, cousin, ...
	// Account is how they talk to the agent, e.g. "telegram:123"; empty if
	// they don't. With it, the agent knows them by account, not by name.
	Account string `yaml:"account"`
	// Circle is which part of its life they're from: family (the default),
	// friend, school (a teacher or mentor), work or community.
	Circle string `yaml:"circle"`
}

// Circles a person it is born knowing can be from.
var Circles = []string{"family", "friend", "school", "work", "community"}

// CircleOf is the circle a relation is usually from: a mentor or teacher is
// school, a friend is a friend, anyone else is family.
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

// nonEmpty trims each line and drops the blank ones.
func nonEmpty(lines []string) []string {
	var out []string
	for _, s := range lines {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
