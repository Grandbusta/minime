package pricing

import (
	"math"
	"testing"
)

func TestCost(t *testing.T) {
	luna := Price{Input: 0.10, Output: 0.50, CachedInput: 0.01, CacheWrite: 0.125}
	cases := []struct {
		name string
		u    Usage
		want float64
	}{
		{"plain", Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000}, 0.60},
		{"cached part billed lower", Usage{InputTokens: 1_000_000, CachedTokens: 400_000}, 0.6*0.10 + 0.4*0.01},
		{"cache writes billed higher", Usage{InputTokens: 1_000_000, CacheWriteTokens: 1_000_000}, 0.125},
		{"nothing", Usage{}, 0},
	}
	for _, tc := range cases {
		if got := luna.Cost(tc.u); math.Abs(got-tc.want) > 1e-12 {
			t.Errorf("%s: cost = %v, want %v", tc.name, got, tc.want)
		}
	}
	if got := (Price{}).Cost(Usage{InputTokens: 5000, OutputTokens: 5000}); got != 0 {
		t.Errorf("unknown model cost = %v, want 0", got)
	}
}
