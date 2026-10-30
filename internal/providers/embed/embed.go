// Package embed turns text into vectors whose closeness is closeness of
// meaning, so the agent can find the decisions most like the one it faces.
// Providers are switchable, like System 1 and System 2.
package embed

import (
	"context"
	"math"
)

// Embedder turns texts into vectors, one each, in order.
type Embedder interface {
	Embed(ctx context.Context, texts []string) (Embedded, error)
}

// Embedded is what an Embed call returned.
type Embedded struct {
	Vectors [][]float32
	Model   string  // e.g. "openai/text-embedding-3-small"
	CostUSD float64 // what it cost; 0 if unpriced
}

// Similarity is how close two vectors are in meaning: their cosine, from -1
// to 1 (1 the same). 0 when they can't be compared.
func Similarity(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
