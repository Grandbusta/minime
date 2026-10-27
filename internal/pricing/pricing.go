// Package pricing turns token usage into a recorded cost.
package pricing

// Price is a model's rates in USD per 1M tokens.
type Price struct {
	Input       float64 `yaml:"input"`
	Output      float64 `yaml:"output"`
	CachedInput float64 `yaml:"cached_input"`
	CacheWrite  float64 `yaml:"cache_write"` // 30-minute cache writes, the default TTL
}

// Usage counts tokens the way OpenAI reports them: cached and cache-write
// tokens are part of InputTokens, not on top of it.
type Usage struct {
	InputTokens      int
	CachedTokens     int
	CacheWriteTokens int
	OutputTokens     int
}

// Cost in USD. A zero Price (model not in the table) costs 0.
func (p Price) Cost(u Usage) float64 {
	uncached := max(u.InputTokens-u.CachedTokens-u.CacheWriteTokens, 0)
	return (float64(uncached)*p.Input +
		float64(u.CachedTokens)*p.CachedInput +
		float64(u.CacheWriteTokens)*p.CacheWrite +
		float64(u.OutputTokens)*p.Output) / 1e6
}
