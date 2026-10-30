package mind

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/Grandbusta/minime/internal/memory"
	"github.com/Grandbusta/minime/internal/providers/embed"
)

// Judgment: how its owner decides. The judgment file seeds it as proposed;
// only what the owner approves in the console is used.

const (
	judgmentCases = 5
)

var (
	planJudgment   = []string{"general", "life", "product"}
	reviewJudgment = []string{"general", "life", "product", "autonomy"}
	learnJudgment  = []string{"general", "life"}
)

// seedJudgment adds only what was not seeded before, so what the owner
// edited or rejected stays so.
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

// judgmentNote picks the past decisions most like about, or else the newest
// of these types. Empty when none are approved.
func (m *Mind) judgmentNote(ctx context.Context, types []string, about string) string {
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
	cs, ok := m.similarCases(ctx, about)
	if !ok {
		if cs, err = m.Store.JudgmentCases(ctx, memory.JudgmentApproved, types); err != nil {
			m.Log.Warn("loading its owner's decisions failed", "err", err)
		}
		if len(cs) > judgmentCases { // the newest
			cs = cs[len(cs)-judgmentCases:]
		}
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
		if ok {
			fmt.Fprintf(&b, "\n%s's decisions most like this one:", owner)
		} else {
			fmt.Fprintf(&b, "\nSome of %s's decisions:", owner)
		}
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

// minSimilarity is how alike (cosine) a past decision must be to count as
// like the one at hand.
const minSimilarity = 0.2

// similarCases embeds any decision not embedded yet. ok is false when it
// can't tell (no embedder, nothing to compare, or the embedder failed).
func (m *Mind) similarCases(ctx context.Context, about string) ([]memory.JudgmentCase, bool) {
	if m.Embedder == nil || strings.TrimSpace(about) == "" {
		return nil, false
	}
	cs, err := m.Store.JudgmentCases(ctx, memory.JudgmentApproved, nil)
	if err != nil || len(cs) == 0 {
		return nil, false
	}
	texts := []string{about}
	var missing []int
	for i, c := range cs {
		if len(c.Embedding) == 0 {
			texts, missing = append(texts, c.Text()), append(missing, i)
		}
	}
	out, err := m.Embedder.Embed(ctx, texts)
	if err != nil {
		m.Log.Warn("embedding failed; taking the newest of its owner's decisions instead", "err", err)
		return nil, false
	}
	for n, i := range missing {
		cs[i].Embedding = out.Vectors[n+1]
		if err := m.Store.SaveCaseEmbedding(ctx, cs[i].ID, cs[i].Embedding, out.Model); err != nil {
			m.Log.Warn("saving an embedding failed", "err", err)
		}
	}
	if len(missing) > 0 {
		m.Log.Info("embedded its owner's decisions", "cases", len(missing), "model", out.Model, "cost_usd", out.CostUSD)
	}
	type scored struct {
		c   memory.JudgmentCase
		sim float64
	}
	var best []scored
	for _, c := range cs {
		if sim := embed.Similarity(out.Vectors[0], c.Embedding); sim >= minSimilarity {
			best = append(best, scored{c, sim})
		}
	}
	slices.SortStableFunc(best, func(a, b scored) int { return cmp.Compare(b.sim, a.sim) })
	var picked []memory.JudgmentCase
	for i := 0; i < len(best) && i < judgmentCases; i++ {
		picked = append(picked, best[i].c)
	}
	return picked, true
}

func (m *Mind) dayAbout(ctx context.Context, projects []memory.Project) string {
	var parts []string
	if goals, err := m.Store.Goals(ctx, true); err == nil {
		for _, g := range goals {
			parts = append(parts, "goal: "+g.Text)
		}
	}
	for _, p := range projects {
		parts = append(parts, "project: "+p.Title)
	}
	if len(parts) == 0 {
		return "Planning the day: what to focus on, and whether to start something new."
	}
	return "Planning the day: what to focus on, and whether to start something new or finish what's started. " + strings.Join(parts, "; ") + "."
}

func reviewAbout(goals []memory.Goal, projects []memory.Project) string {
	var parts []string
	for _, g := range goals {
		parts = append(parts, "goal: "+g.Text)
	}
	for _, p := range projects {
		parts = append(parts, "project: "+p.Title)
	}
	return "Reviewing the week: whether to keep, drop or set goals, and what to focus on next. " + strings.Join(parts, "; ")
}
