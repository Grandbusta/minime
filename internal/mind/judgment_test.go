package mind

import (
	"context"
	"strings"
	"testing"

	"github.com/Grandbusta/minime/internal/memory"
	"github.com/Grandbusta/minime/internal/self"
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
		if note := m.judgmentNote(context.Background(), planJudgment, ""); note != "" {
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
		note := m.judgmentNote(context.Background(), planJudgment, "")
		if !strings.Contains(note, "Start small, then build on it.") || !strings.Contains(note, "the tiniest part") ||
			strings.Contains(note, "switchable") || strings.Contains(note, "managed Postgres") { // engineering: not for planning its day
			t.Errorf("note:\n%s", note)
		}
	})
}

func TestPrioritiesAndBoundaries(t *testing.T) {
	ctx := context.Background()
	m, s := newMind(&fakeReplier{}, &fakeSystem1{}, &fakeSystem2{})
	m.Judgment = self.Judgment{Priorities: []string{"Health", "Honesty"}, Boundaries: []string{"Never lie to family."},
		Principles: []self.Principle{{Text: "Finish what you started.", Types: []string{"life"}}}}
	m.seedJudgment(ctx)
	m.seedJudgment(ctx)
	if len(s.principles) != 4 || s.principles[1].Kind != memory.PrincipleRank || s.principles[1].Rank != 1 || s.principles[2].Rank != 2 ||
		s.principles[3].Kind != memory.PrincipleLimit {
		t.Fatalf("principles = %+v", s.principles)
	}
	if note := m.boundariesNote(ctx); note != "" {
		t.Errorf("boundaries before approval = %q", note)
	}
	for i := range s.principles {
		s.principles[i].Status = memory.JudgmentApproved
	}
	if !strings.Contains(m.boundariesNote(ctx), "- Never lie to family.") {
		t.Errorf("boundaries = %q", m.boundariesNote(ctx))
	}
}
