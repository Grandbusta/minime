package mind

import (
	"strings"

	"minime/internal/memory"
	"minime/internal/perception"
)

// fallbackName is what the agent calls someone whose name it does not know.
const fallbackName = "Friend"

// names is who is in a conversation: the agent and the person it is talking with.
type names struct{ agent, speaker string }

func (m *Mind) namesFor(p perception.Percept) names {
	speaker := strings.TrimSpace(p.SpeakerName)
	if speaker == "" {
		speaker = fallbackName
	}
	return names{agent: m.Life.Name, speaker: speaker}
}

// message resolves a message in the speaker's voice: "my church" → "Sam's church".
func (n names) message(text string) string { return resolvePronouns(text, n.speaker, n.agent) }

// fact resolves a fact in its subject's voice, so a person's "I live with my
// parents" and the agent's own facts both name who they are about.
func (n names) fact(f memory.Fact) string {
	if f.Subject == memory.SubjectSelf {
		return resolvePronouns(f.Text, n.agent, n.speaker)
	}
	return resolvePronouns(f.Text, n.speaker, n.agent)
}

// fill puts the names into a question's {agent} and {speaker} placeholders.
func (n names) fill(s string) string {
	return strings.NewReplacer("{agent}", n.agent, "{speaker}", n.speaker).Replace(s)
}
