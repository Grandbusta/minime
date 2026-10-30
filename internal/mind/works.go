package mind

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/oklog/ulid/v2"

	"github.com/Grandbusta/minime/internal/memory"
	"github.com/Grandbusta/minime/internal/providers/embed"
)

const (
	worksListed  = 8  // recent works named in a note
	worksScanned = 40 // recent works it looks among for the one meant
	worksMeant   = 2
	// minWorkSimilarity is how alike (cosine) a work and what is said must
	// be for the work to count as what it's about: a request names its work
	// at about 0.35 to 0.5, small talk stays under 0.25.
	minWorkSimilarity = 0.3
	talkTurns         = 3 // exchanges before a message that say what it's about
	// closeMatch is how near (cosine) two works' likeness to what is said is
	// for either to be what it's about.
	closeMatch     = 0.05
	workEmbedChars = 2000
	workNoteChars  = 6000
)

// worksNote lists what it has made that it may bring up with person, each
// with a ref like "w1", and gives in full the ones about seems to mean. It
// costs an embedding.
func (m *Mind) worksNote(ctx context.Context, person, about string) (string, map[string]memory.Work, float64) {
	ws, err := m.Store.WorksFor(ctx, person, worksScanned)
	if err != nil {
		m.Log.Warn("loading its works failed", "err", err)
		return "", nil, 0
	}
	if len(ws) == 0 {
		return "", nil, 0
	}
	meant, close, cost := m.meantWorks(ctx, about, ws)
	listed := ws[:min(len(ws), worksListed)]
	for _, w := range meant {
		if !slices.ContainsFunc(listed, func(l memory.Work) bool { return l.ID == w.ID }) {
			listed = append(listed, w) // older, but meant
		}
	}
	refs := map[string]memory.Work{}
	ref := map[ulid.ULID]string{}
	whose := m.whoseWork(ctx)
	var b strings.Builder
	b.WriteString("\n\nWhat you've made lately, newest first, each with a ref:")
	for n, w := range listed {
		ref[w.ID] = fmt.Sprintf("w%d", n+1)
		refs[ref[w.ID]] = w
		fmt.Fprintf(&b, "\n- [%s] %s: %q", ref[w.ID], whose(w), w.Title)
	}
	if len(meant) > 0 {
		b.WriteString("\n\nWhat this seems to be about, exactly as you made it:")
		for _, w := range meant {
			fmt.Fprintf(&b, "\n\n[%s] %q (%s):\n<<<WORK\n%s\nWORK>>>", ref[w.ID], w.Title, whose(w),
				cutRunes(strings.TrimSpace(w.Text), workNoteChars))
		}
	}
	if close {
		fmt.Fprintf(&b, "\n\n[%s] and [%s] fit about equally well. If the talk makes clear which is meant, use that one and say "+
			"which it is (its title, or when you made it); if it doesn't, ask, naming each that way.", ref[meant[0].ID], ref[meant[1].ID])
	}
	return b.String(), refs, cost
}

// whoseWork describes where a work came from: when, what kind, and whether
// it was its own or made for someone who asked.
func (m *Mind) whoseWork(ctx context.Context) func(memory.Work) string {
	names := map[string]string{}
	return func(w memory.Work) string {
		origin := "your own"
		if w.MadeFor != "" && w.MadeFor != memory.SubjectSelf {
			if _, ok := names[w.MadeFor]; !ok {
				names[w.MadeFor] = orDefault(m.person(ctx, w.MadeFor).Name, fallbackName)
			}
			origin = "made for " + names[w.MadeFor] + " when they asked, and sent to them"
		}
		return fmt.Sprintf("%s, %s, %s", w.CreatedAt.In(m.Zone).Format("Mon 2 Jan 15:04"), strings.ReplaceAll(w.Kind, "_", " "), origin)
	}
}

// meantWorks returns those of ws that about is about, most alike first, and
// whether the first two are close; it embeds any not embedded yet.
func (m *Mind) meantWorks(ctx context.Context, about string, ws []memory.Work) ([]memory.Work, bool, float64) {
	if m.Embedder == nil || strings.TrimSpace(about) == "" {
		return nil, false, 0
	}
	texts := []string{about}
	var missing []int
	for i, w := range ws {
		if len(w.Embedding) == 0 {
			texts, missing = append(texts, workText(w)), append(missing, i)
		}
	}
	out, err := m.Embedder.Embed(ctx, texts)
	if err != nil {
		m.Log.Warn("embedding failed; no works found for what was said", "err", err)
		return nil, false, 0
	}
	for n, i := range missing {
		ws[i].Embedding = out.Vectors[n+1]
		if err := m.Store.SaveWorkEmbedding(ctx, ws[i].ID, ws[i].Embedding, out.Model); err != nil {
			m.Log.Warn("saving an embedding failed", "err", err)
		}
	}
	type scored struct {
		w   memory.Work
		sim float64
	}
	var best []scored
	for _, w := range ws {
		if sim := embed.Similarity(out.Vectors[0], w.Embedding); sim >= minWorkSimilarity {
			best = append(best, scored{w, sim})
		}
	}
	slices.SortStableFunc(best, func(a, b scored) int { return cmp.Compare(b.sim, a.sim) })
	var picked []memory.Work
	for i := 0; i < len(best) && i < worksMeant; i++ {
		picked = append(picked, best[i].w)
	}
	close := len(best) > 1 && best[0].sim-best[1].sim < closeMatch
	return picked, close, out.CostUSD
}

func workText(w memory.Work) string {
	return cutRunes(w.Title+" ("+strings.ReplaceAll(w.Kind, "_", " ")+")\n\n"+w.Text, workEmbedChars)
}

func cutRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
