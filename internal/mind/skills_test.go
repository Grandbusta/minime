package mind

import (
	"context"
	"strings"
	"testing"

	"github.com/Grandbusta/minime/internal/ability"
	"github.com/Grandbusta/minime/internal/agentskill"
	"github.com/Grandbusta/minime/internal/memory"
)

var testSkills = []agentskill.Skill{
	{Name: "beat-making", Description: "How to make a beat: drums first, then the bass.", Body: "Start with the kick on one."},
	{Name: "money-talk", Description: "Talking about money and prices with clients.", Body: "Say the price plainly."},
}

func TestTheSkillThatFitsIsGiven(t *testing.T) {
	s2 := &fakeSystem2{}
	m, _ := promiseMind(&fakeReplier{}, s2)
	m.Embedder, m.Skills = &wordEmbedder{}, testSkills
	m.handle(context.Background(), percept("1", "Can you help me with this beat?"))
	note := s2.requests[0].Note
	if !strings.Contains(note, `the skill "beat-making"`) || !strings.Contains(note, "Start with the kick on one.") ||
		strings.Contains(note, "Say the price plainly.") {
		t.Errorf("reply note:\n%s", note)
	}

	m.handle(context.Background(), percept("2", "How was your day?"))
	if note := s2.requests[1].Note; strings.Contains(note, "<<<SKILL") {
		t.Errorf("a skill was given for small talk:\n%s", note)
	}
}

func TestAnActivityIsGivenItsSkill(t *testing.T) {
	m, s, s2, _, _ := learningMind()
	m.Embedder, m.Skills = &wordEmbedder{}, testSkills
	s.intentions[0].Text = "Make a new beat for the money song"
	m.tick(context.Background(), at(29, 10, 0))
	var made string
	for _, r := range s2.requests {
		if r.Format != nil && r.Format.Name == "activity" {
			made = r.Note
		}
	}
	if !strings.Contains(made, "<<<SKILL") {
		t.Errorf("activity note has no skill:\n%s", made)
	}
}

func TestUsingASkill(t *testing.T) {
	use := agentskill.Use{Skills: testSkills}
	act := func(q string) ability.Act {
		return ability.Act{Intention: memory.Intention{Args: map[string]string{"query": q}}}
	}
	if m, err := use.Prepare(context.Background(), act("money-talk")); err != nil || !strings.Contains(m.Note, "Say the price plainly.") {
		t.Errorf("read = %q, %v", m.Note, err)
	}
	if _, err := use.Prepare(context.Background(), act("cooking")); err == nil {
		t.Error("read a skill it doesn't have")
	}
	if len(use.Next()) != 1 || use.Risk() != ability.Private {
		t.Error("use_skill can't be followed by reading a file, or isn't private")
	}
}
