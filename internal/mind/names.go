package mind

import (
	"strings"

	"github.com/Grandbusta/minime/internal/memory"
	"github.com/Grandbusta/minime/internal/perception"
)

const fallbackName = "Friend"

type names struct{ agent, speaker string }

func (m *Mind) namesFor(p perception.Percept) names {
	speaker := strings.TrimSpace(p.SpeakerName)
	if speaker == "" {
		speaker = fallbackName
	}
	return names{agent: m.Life.Name, speaker: speaker}
}

func (n names) message(text string) string { return resolvePronouns(text, n.speaker, n.agent) }

// fact resolves a fact in its subject's voice, so a person's "I live with my
// parents" and the agent's own facts both name who they are about.
func (n names) fact(f memory.Fact) string {
	if f.Subject == memory.SubjectSelf {
		return resolvePronouns(f.Text, n.agent, n.speaker)
	}
	return resolvePronouns(f.Text, n.speaker, n.agent)
}

func (n names) fill(s string) string {
	return strings.NewReplacer("{agent}", n.agent, "{speaker}", n.speaker).Replace(s)
}
