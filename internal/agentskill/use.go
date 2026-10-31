package agentskill

import (
	"context"
	"fmt"
	"strings"

	"github.com/Grandbusta/minime/internal/ability"
)

// Use reads a skill, or one of its files, as material for an activity: its
// query is a skill's name, or name/file.
type Use struct {
	Skills []Skill
}

func (Use) Name() string { return "use_skill" }

func (Use) Describe() string {
	return "read know-how you've been given (one of the skills listed) before doing something; query: its name, or name/file " +
		"to read one of its files."
}

func (Use) Risk() ability.Risk { return ability.Private }

// Next lets it read one of the skill's files after its instructions.
func (u Use) Next() []ability.Ability { return []ability.Ability{u} }

func (u Use) Prepare(_ context.Context, a ability.Act) (ability.Material, error) {
	name, file, _ := strings.Cut(strings.TrimSpace(a.Query()), "/")
	for _, s := range u.Skills {
		if s.Name != name {
			continue
		}
		if file == "" {
			note := fmt.Sprintf("\n\nYou read the skill %q, know-how you've been given:\n<<<SKILL %s\n%s\nSKILL>>>", s.Name, s.Name, s.Body)
			if len(s.Files) > 0 {
				note += fmt.Sprintf("\nIts files (use_skill %s/<file> reads one): %s", s.Name, strings.Join(s.Files, ", "))
			}
			return ability.Material{Note: note, Project: a.Project}, nil
		}
		text, err := s.Read(file)
		if err != nil {
			return ability.Material{}, err
		}
		return ability.Material{Note: fmt.Sprintf("\n\nYou read %s from the skill %q:\n<<<FILE\n%s\nFILE>>>", file, s.Name, text),
			Project: a.Project}, nil
	}
	return ability.Material{}, fmt.Errorf("no skill %q", name)
}
