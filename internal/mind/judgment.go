package mind

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"minime/internal/memory"
)

// Judgment: how its owner decides, so it can learn to decide the way they
// would. Their judgment file seeds it as proposed: decisions they made
// (cases) and the rules drawn from them (principles). The owner approves,
// edits or rejects each in the console; only approved ones go into how it
// plans its day, reviews its week and plans its learning.

const (
	// judgmentCases is how many of its owner's past decisions go into a note.
	judgmentCases = 5
)

// Decision types each kind of thinking it does draws judgment from.
var (
	planJudgment   = []string{"general", "life", "product"}
	reviewJudgment = []string{"general", "life", "product", "autonomy"}
	learnJudgment  = []string{"general", "life"}
)

// seedJudgment copies its owner's judgment file into memory, as proposed:
// anything not seeded before. What the owner edited or rejected stays so.
func (m *Mind) seedJudgment(ctx context.Context) {
	if len(m.Judgment.Cases) == 0 && len(m.Judgment.Principles) == 0 {
		return
	}
	now := time.Now().UTC()
	cases := make([]memory.JudgmentCase, len(m.Judgment.Cases))
	for i, c := range m.Judgment.Cases {
		// In the file's order, a moment apart, so they list as written.
		cases[i] = memory.JudgmentCase{ID: ulid.Make(), Key: c.Key, Type: c.Type, Situation: c.Situation, Options: c.Options,
			Choice: c.Choice, Reason: c.Reason, Status: memory.JudgmentProposed, Source: memory.JudgmentFromSeed,
			At: now.Add(time.Duration(i) * time.Millisecond)}
	}
	principles := make([]memory.Principle, len(m.Judgment.Principles))
	for i, p := range m.Judgment.Principles {
		principles[i] = memory.Principle{ID: ulid.Make(), Text: p.Text, Seed: p.Text, Types: p.Types, Cases: p.Cases,
			Status: memory.JudgmentProposed, Source: memory.JudgmentFromSeed, CreatedAt: now.Add(time.Duration(i) * time.Millisecond)}
	}
	nc, np, err := m.Store.SeedJudgment(ctx, cases, principles)
	if err != nil {
		m.Log.Warn("seeding its owner's judgment failed", "err", err)
		return
	}
	if nc > 0 || np > 0 {
		m.Log.Info("seeded its owner's judgment for review", "cases", nc, "principles", np)
	}
}

// judgmentNote is how its owner decides, for a note: the principles they
// approved that apply to these types of decision, and a few of their past
// decisions of those types. Empty when none are approved.
func (m *Mind) judgmentNote(ctx context.Context, types []string) string {
	ps, err := m.Store.Principles(ctx, memory.JudgmentApproved)
	if err != nil {
		m.Log.Warn("loading principles failed", "err", err)
	}
	var rules []string
	for _, p := range ps {
		if len(p.Types) == 0 || slices.ContainsFunc(p.Types, func(t string) bool { return slices.Contains(types, t) }) {
			rules = append(rules, p.Text)
		}
	}
	cs, err := m.Store.JudgmentCases(ctx, memory.JudgmentApproved, types)
	if err != nil {
		m.Log.Warn("loading its owner's decisions failed", "err", err)
	}
	if len(cs) > judgmentCases { // the newest
		cs = cs[len(cs)-judgmentCases:]
	}
	if len(rules) == 0 && len(cs) == 0 {
		return ""
	}
	owner := m.ownerName(ctx)
	var b strings.Builder
	fmt.Fprintf(&b, "\n\nHow %s decides (when a choice is yours, choose the way %s would):", owner, owner)
	for _, r := range rules {
		fmt.Fprintf(&b, "\n- %s", r)
	}
	if len(cs) > 0 {
		fmt.Fprintf(&b, "\nSome of %s's decisions:", owner)
		for _, c := range cs {
			fmt.Fprintf(&b, "\n- %s: %s chose %s.", strings.TrimSuffix(c.Situation, "."), owner, strings.TrimSuffix(c.Choice, "."))
			if len(c.Options) > 0 {
				fmt.Fprintf(&b, " (Options: %s.)", strings.Join(c.Options, "; "))
			}
			if c.Reason != "" {
				fmt.Fprintf(&b, " Why: %s", c.Reason)
			}
		}
	}
	return b.String()
}
