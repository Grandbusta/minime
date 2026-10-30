package embed

import (
	"math"
	"testing"
)

func TestSimilarity(t *testing.T) {
	for name, c := range map[string]struct {
		a, b []float32
		want float64
	}{
		"same":       {[]float32{1, 2}, []float32{2, 4}, 1},
		"orthogonal": {[]float32{1, 0}, []float32{0, 3}, 0},
		"opposite":   {[]float32{1, 0}, []float32{-1, 0}, -1},
		"lengths":    {[]float32{1}, []float32{1, 0}, 0},
		"zero":       {[]float32{0, 0}, []float32{1, 0}, 0},
	} {
		if got := Similarity(c.a, c.b); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%s: %v, want %v", name, got, c.want)
		}
	}
}
