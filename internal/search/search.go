// Package search is the agent on the open web: finding pages (Searcher) and
// reading them (Fetcher). Providers are switchable like the models: OpenAI's
// built-in web search today (same key as System 2), TinyFish later.
//
// The open web is where people can write things at it, so: a search returns
// only pages the provider actually cites (never a URL a model made up), and
// the fetcher reads only https pages on public addresses, with size and time
// limits, as plain text. What it reads is material, never instructions; that
// is the abilities' job (fenced), and nothing it reads can make it act.
package search

import (
	"context"
	"errors"
)

// Result is a page a search found.
type Result struct {
	Title   string
	URL     string
	Snippet string // why it's worth reading, in a sentence
}

// Page is a page's readable text.
type Page struct {
	URL   string
	Title string
	Text  string
}

// Found is what a search found, and what it cost.
type Found struct {
	Results []Result
	CostUSD float64 // what the provider charges in tokens; a per-search fee, if any, is billed on top
}

// Searcher finds pages on the web.
type Searcher interface {
	Search(ctx context.Context, query string, n int) (Found, error)
}

// Fetcher reads a page.
type Fetcher interface {
	Fetch(ctx context.Context, url string) (Page, error)
}

// ErrNothing means a search or a page had nothing to read.
var ErrNothing = errors.New("nothing to read")
