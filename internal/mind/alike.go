package mind

import (
	"cmp"
	"context"
	"slices"

	"github.com/Grandbusta/minime/internal/providers/embed"
)

type alike[T any] struct {
	item T
	sim  float64 // cosine
}

// rankAlike returns up to limit of items at least minSim alike to about, most
// alike first. Items with no embedding yet are embedded in the same call and
// saved, so each is embedded once.
func rankAlike[T any](ctx context.Context, m *Mind, about string, items []T, minSim float64, limit int,
	vector func(*T) *[]float32, text func(T) string, save func(T, string) error) ([]alike[T], embed.Embedded, error) {
	texts := []string{about}
	var missing []int
	for i := range items {
		if len(*vector(&items[i])) == 0 {
			texts, missing = append(texts, text(items[i])), append(missing, i)
		}
	}
	out, err := m.Embedder.Embed(ctx, texts)
	if err != nil {
		return nil, out, err
	}
	for n, i := range missing {
		*vector(&items[i]) = out.Vectors[n+1]
		if err := save(items[i], out.Model); err != nil {
			m.Log.Warn("saving an embedding failed", "err", err)
		}
	}
	var ranked []alike[T]
	for i := range items {
		if sim := embed.Similarity(out.Vectors[0], *vector(&items[i])); sim >= minSim {
			ranked = append(ranked, alike[T]{items[i], sim})
		}
	}
	slices.SortStableFunc(ranked, func(a, b alike[T]) int { return cmp.Compare(b.sim, a.sim) })
	return ranked[:min(len(ranked), limit)], out, nil
}
