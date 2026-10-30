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
	if len(m.Judgment.Cases) == 0 && len(m.Judgment.Principles) == 0 && len(m.Judgment.Priorities) == 0 && len(m.Judgment.Boundaries) == 0 {
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
	at := now.Add(time.Duration(len(principles)) * time.Millisecond)
	for i, text := range m.Judgment.Priorities {
		principles = append(principles, memory.Principle{ID: ulid.Make(), Text: text, Seed: text, Kind: memory.PrincipleRank, Rank: i + 1,
			Status: memory.JudgmentProposed, Source: memory.JudgmentFromSeed, CreatedAt: at.Add(time.Duration(i) * time.Millisecond)})
	}
	for i, text := range m.Judgment.Boundaries {
		principles = append(principles, memory.Principle{ID: ulid.Make(), Text: text, Seed: text, Kind: memory.PrincipleLimit,
			Status: memory.JudgmentProposed, Source: memory.JudgmentFromSeed, CreatedAt: at.Add(time.Duration(i) * time.Millisecond)})
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

// judgmentNote is how its owner decides, for a note: what they never do,
// what matters most to them in order, the rules they approved that apply to
// these types of decision, and a few of their past decisions of those types.
// Empty when none are approved.
func (m *Mind) judgmentNote(ctx context.Context, types []string) string {
	ps, err := m.Store.Principles(ctx, memory.JudgmentApproved)
	if err != nil {
		m.Log.Warn("loading principles failed", "err", err)
	}
	var limits, ranks, rules []string
	for _, p := range ps {
		switch p.Kind {
		case memory.PrincipleLimit:
			limits = append(limits, p.Text)
		case memory.PrincipleRank:
			ranks = append(ranks, p.Text) // in rank order
		default:
			if len(p.Types) == 0 || slices.ContainsFunc(p.Types, func(t string) bool { return slices.Contains(types, t) }) {
				rules = append(rules, p.Text)
			}
		}
	}
	cs, err := m.Store.JudgmentCases(ctx, memory.JudgmentApproved, types)
	if err != nil {
		m.Log.Warn("loading its owner's decisions failed", "err", err)
	}
	if len(cs) > judgmentCases { // the newest
		cs = cs[len(cs)-judgmentCases:]
	}
	if len(limits) == 0 && len(ranks) == 0 && len(rules) == 0 && len(cs) == 0 {
		return ""
	}
	owner := m.ownerName(ctx)
	var b strings.Builder
	fmt.Fprintf(&b, "\n\nHow %s decides (when a choice is yours, choose the way %s would):", owner, owner)
	if len(limits) > 0 {
		fmt.Fprintf(&b, "\nNever, whatever the gain:")
		for _, l := range limits {
			fmt.Fprintf(&b, "\n- %s", l)
		}
	}
	if len(ranks) > 0 {
		fmt.Fprintf(&b, "\nWhat matters most to %s, most important first (when rules pull different ways, the higher one wins):", owner)
		for i, r := range ranks {
			fmt.Fprintf(&b, "\n%d. %s", i+1, r)
		}
	}
	if len(rules) > 0 {
		fmt.Fprintf(&b, "\n%s's rules:", owner)
		for _, r := range rules {
			fmt.Fprintf(&b, "\n- %s", r)
		}
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
			if c.Outcome != "" {
				fmt.Fprintf(&b, " How it turned out: %s.", strings.TrimSuffix(c.Outcome, "."))
			}
		}
	}
	return b.String()
}

// boundariesNote is what its owner never does, for a note on making
// something: only the boundaries they approved. Empty when there are none.
func (m *Mind) boundariesNote(ctx context.Context) string {
	ps, err := m.Store.Principles(ctx, memory.JudgmentApproved)
	if err != nil {
		return ""
	}
	var b strings.Builder
	for _, p := range ps {
		if p.Kind == memory.PrincipleLimit {
			fmt.Fprintf(&b, "\n- %s", p.Text)
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return fmt.Sprintf("\n\nWhat %s never does, whatever the gain (you don't either):", m.ownerName(ctx)) + b.String()
}
