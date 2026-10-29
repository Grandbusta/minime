// Package self loads the agent's life story: who it is, written by its creator.
package self

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Life is the agent's life story. The agent never contradicts it; anything
// else about itself it decides when it comes up, and remembers.
type Life struct {
	Name  string   `yaml:"name"`
	Story []string `yaml:"story"` // third person, with the agent's name
	// Personality is how the agent is with people: its temperament and how it
	// talks, third person with its name. Optional.
	Personality []string `yaml:"personality"`
	// Routine steers how the agent plans its days, third person with its name,
	// e.g. "Daniel makes music in the evenings". Optional; it plans the rest itself.
	Routine []string `yaml:"routine"`
	// Family the agent is born knowing, like anyone knows their parents and
	// cousins whether or not they ever talk to them. Copied into memory the
	// first time; after that memory wins.
	Family []Relative `yaml:"family"`
}

// Relative is a member of the agent's family.
type Relative struct {
	Name     string `yaml:"name"`
	Relation string `yaml:"relation"` // to the agent: father, mother, cousin, ...
	// Account is how they talk to the agent, e.g. "telegram:123"; empty if
	// they don't. With it, the agent knows them by account, not by name.
	Account string `yaml:"account"`
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
	l.Name = strings.TrimSpace(l.Name)
	l.Story = nonEmpty(l.Story)
	l.Personality = nonEmpty(l.Personality)
	l.Routine = nonEmpty(l.Routine)
	if l.Name == "" || len(l.Story) == 0 {
		return Life{}, errors.New(path + ": write the agent's name and at least one line of its life story first")
	}
	accounts := map[string]bool{}
	for i, r := range l.Family {
		r.Name, r.Relation, r.Account = strings.TrimSpace(r.Name), strings.TrimSpace(r.Relation), strings.TrimSpace(r.Account)
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
