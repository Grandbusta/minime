// Package search is the agent on the open web: finding pages (Searcher) and
// reading them (Fetcher).
//
// A search returns only pages the provider actually cites (never a URL a
// model made up), and the fetcher reads only https pages on public addresses,
// with size and time limits. What it reads is untrusted material, never
// instructions; the abilities fence it.
package search

import (
	"context"
	"errors"
)

type Result struct {
	Title   string
	URL     string
	Snippet string
}

type Page struct {
	URL   string
	Title string
	Text  string
}

type Found struct {
	Results []Result
	CostUSD float64 // what the provider charges in tokens; a per-search fee, if any, is billed on top
}

type Searcher interface {
	Search(ctx context.Context, query string, n int) (Found, error)
}

type Fetcher interface {
	Fetch(ctx context.Context, url string) (Page, error)
}

var ErrNothing = errors.New("nothing to read")
