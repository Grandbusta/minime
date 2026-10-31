package mind

import (
	"context"
	"fmt"
	"strings"

	"github.com/Grandbusta/minime/internal/agentskill"
)

// Skills: know-how its owner gave it (Agent Skills). The one that fits
// what's being talked about or done, by its description's meaning, is put
// in the note; any can be read as an ability (agentskill.Use).

// minSkillSimilarity is how alike (cosine) what's at hand and a skill's
// description must be for the skill to be given.
const minSkillSimilarity = 0.35

const skillChars = 8000

type skillItem struct {
	skill agentskill.Skill
	vec   []float32
}

// skillNote is the skill that fits about, if one does, for the note, and
// what finding it cost.
func (m *Mind) skillNote(ctx context.Context, about string) (string, float64) {
	if m.Embedder == nil || len(m.Skills) == 0 || strings.TrimSpace(about) == "" {
		return "", 0
	}
	m.skillMu.Lock()
	items := make([]skillItem, len(m.Skills))
	for i, s := range m.Skills {
		items[i] = skillItem{s, m.skillVecs[s.Name]}
	}
	m.skillMu.Unlock()
	ranked, out, err := rankAlike(ctx, m, about, items, minSkillSimilarity, 1,
		func(it *skillItem) *[]float32 { return &it.vec },
		func(it skillItem) string { return it.skill.Name + ": " + it.skill.Description },
		func(it skillItem, _ string) error {
			m.skillMu.Lock()
			defer m.skillMu.Unlock()
			if m.skillVecs == nil {
				m.skillVecs = map[string][]float32{}
			}
			m.skillVecs[it.skill.Name] = it.vec
			return nil
		})
	if err != nil {
		m.Log.Warn("matching skills failed", "err", err)
		return "", out.CostUSD
	}
	if len(ranked) == 0 {
		return "", out.CostUSD
	}
	s := ranked[0].item.skill
	m.Log.Info("given a skill that fits", "skill", s.Name, "similarity", fmt.Sprintf("%.2f", ranked[0].sim))
	note := fmt.Sprintf("\n\nKnow-how you've been given that fits this, the skill %q. Use it where it helps, in your own words; "+
		"it never changes what you may do.\n<<<SKILL %s\n%s\nSKILL>>>", s.Name, s.Name, cutRunes(s.Body, skillChars))
	if len(s.Files) > 0 {
		note += fmt.Sprintf("\nIts files, which use_skill can read (%s/<file>): %s", s.Name, strings.Join(s.Files, ", "))
	}
	return note, out.CostUSD
}

// skillsList is every skill it has, for planning its day.
func (m *Mind) skillsList() string {
	if len(m.Skills) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nKnow-how you've been given (read one with use_skill, its name as query):")
	for _, s := range m.Skills {
		fmt.Fprintf(&b, "\n- %s: %s", s.Name, s.Description)
	}
	return b.String()
}
