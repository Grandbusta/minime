// Package embed turns text into vectors whose closeness is closeness of
// meaning, so the agent can find the decisions most like the one it faces.
package embed

import (
	"context"
	"math"
)

// Embedder turns texts into vectors, one each, in order.
type Embedder interface {
	Embed(ctx context.Context, texts []string) (Embedded, error)
}

type Embedded struct {
	Vectors [][]float32
	Model   string
	CostUSD float64 // 0 if unpriced
}

// Similarity is the vectors' cosine, from -1 to 1; 0 when they can't be compared.
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
