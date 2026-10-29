package mind

import (
	"context"
	"strings"
	"testing"

	"github.com/oklog/ulid/v2"

	"minime/internal/memory"
	"minime/internal/self"
)

func TestJudgment(t *testing.T) {
	judgment := self.Judgment{
		Principles: []self.Principle{
			{Text: "Start small, then build on it.", Types: []string{"general"}, Cases: []string{"tiny"}},
			{Text: "Keep providers switchable.", Types: []string{"engineering"}},
		},
		Cases: []self.Case{
			{Key: "tiny", Type: "general", Situation: "What to build first.", Options: []string{"all of it", "the tiniest part"},
				Choice: "the tiniest part", Reason: "so we can build on it"},
			{Key: "managed", Type: "engineering", Situation: "Where the database runs.", Choice: "managed Postgres"},
		},
	}

	t.Run("seeded as proposed, once, and nothing is used until approved", func(t *testing.T) {
		m, s := newMind(&fakeReplier{}, &fakeSystem1{}, &fakeSystem2{})
		m.Judgment = judgment
		m.seedJudgment(context.Background())
		m.seedJudgment(context.Background())
		if len(s.cases) != 2 || len(s.principles) != 2 {
			t.Fatalf("seeded %d cases, %d principles; want 2 and 2, once", len(s.cases), len(s.principles))
		}
		for _, p := range s.principles {
			if p.Status != memory.JudgmentProposed || p.Seed != p.Text || p.Source != memory.JudgmentFromSeed {
				t.Errorf("principle = %+v", p)
			}
		}
		if note := m.judgmentNote(context.Background(), planJudgment); note != "" {
			t.Errorf("note before any approval = %q; want none", note)
		}
	})

	t.Run("approved judgment of the right types goes into the note", func(t *testing.T) {
		m, s := newMind(&fakeReplier{}, &fakeSystem1{}, &fakeSystem2{})
		m.Judgment = judgment
		m.Autonomy.Owner = "telegram:1"
		s.people = map[string]memory.Person{"telegram:1": {Key: "telegram:1", Name: "Sam"}}
		m.seedJudgment(context.Background())
		for i := range s.principles {
			s.principles[i].Status = memory.JudgmentApproved
		}
		for i := range s.cases {
			s.cases[i].Status = memory.JudgmentApproved
		}
		note := m.judgmentNote(context.Background(), planJudgment)
		for _, want := range []string{"How Sam decides", "- Start small, then build on it.",
			"- What to build first: Sam chose the tiniest part. (Options: all of it; the tiniest part.) Why: so we can build on it"} {
			if !strings.Contains(note, want) {
				t.Errorf("note is missing %q:\n%s", want, note)
			}
		}
		for _, not := range []string{"switchable", "managed Postgres"} { // engineering: not for planning its day
			if strings.Contains(note, not) {
				t.Errorf("note has %q, which isn't for planning:\n%s", not, note)
			}
		}
	})

	t.Run("its learning plan is drafted with its owner's judgment", func(t *testing.T) {
		m, s, s2, _ := learningPlanMind()
		s.principles = []memory.Principle{{ID: ulid.Make(), Text: "Start small, then build on it.", Types: []string{"general"},
			Status: memory.JudgmentApproved}}
		s.goals = []memory.Goal{{ID: ulid.Make(), DreamID: ulid.Make(), Text: "Write a hook", Skill: "lyric writing",
			Status: memory.GoalActive, Due: at(30, 0, 0)}}
		m.EnsurePlans(context.Background(), at(29, 9, 0))
		if len(s2.requests) != 1 || !strings.Contains(s2.requests[0].Note, "- Start small, then build on it.") {
			t.Errorf("plan note doesn't have the approved principle")
		}
	})
}
