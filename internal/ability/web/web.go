// Package web is the agent reading the open web: searching for pages, and
// reading a page it found. Each search and page counts against a daily cap.
// A page can only be read if it came from one of its own searches, so a link
// on a page can never send it somewhere; and everything it reads is fenced
// as material, never instructions.
package web

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"minime/internal/ability"
	"minime/internal/library"
	"minime/internal/search"
)

const (
	SearchName = "web_search"
	PageName   = "read_page"

	// NotesKind is the kind of work reading makes: its notes.
	NotesKind = "reading_notes"
	// passageSize is how much of a page it reads in one sitting.
	passageSize = 12000
	// results is how many pages a search lists.
	results = 5
)

// Store keeps what it found and counts what it read.
type Store interface {
	// SaveFound remembers pages a search found, so it may read them later.
	SaveFound(ctx context.Context, query string, rs []search.Result, at time.Time) error
	// Found returns a page one of its searches found, and whether one did.
	Found(ctx context.Context, url string) (search.Result, bool, error)
	// LogWeb records a search or a page read; WebSince counts them since a time.
	LogWeb(ctx context.Context, kind, what string, costUSD float64, at time.Time) error
	WebSince(ctx context.Context, kind string, since time.Time) (int, error)
}

// Limits is how much of the web it may use in a day.
type Limits struct {
	Searches int // searches a day
	Pages    int // pages read a day
}

// ErrOverCap means it has used today's searches or pages.
var ErrOverCap = errors.New("over today's limit")

// Search searches the web and reads the best page it finds.
type Search struct {
	Searcher search.Searcher
	Fetcher  search.Fetcher
	Store    Store
	Limits   Limits
}

func (Search) Name() string { return SearchName }

func (Search) Describe() string {
	return "search the web for something to learn from (guides, articles, examples of how others do it) and read the best page; " +
		"query: what to look for, e.g. \"how to write a catchy song hook\". The other pages it finds can be read later with read_page."
}

func (Search) Risk() ability.Risk { return ability.ReadsWeb }

func (s Search) Prepare(ctx context.Context, a ability.Act) (ability.Material, error) {
	if err := under(ctx, s.Store, "search", s.Limits.Searches, a); err != nil {
		return ability.Material{}, err
	}
	found, err := s.Searcher.Search(ctx, a.Query(), results)
	if logErr := s.Store.LogWeb(ctx, "search", a.Query(), found.CostUSD, a.Now.UTC()); logErr != nil {
		return ability.Material{}, logErr
	}
	if err != nil {
		return ability.Material{}, err
	}
	if err := s.Store.SaveFound(ctx, a.Query(), found.Results, a.Now.UTC()); err != nil {
		return ability.Material{}, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n\nYou searched the web for %q and found:", a.Query())
	for _, r := range found.Results {
		fmt.Fprintf(&b, "\n- %s (%s)", r.Title, r.URL)
		if r.Snippet != "" {
			b.WriteString(": " + r.Snippet)
		}
	}
	// Read the first page that can be read, within today's pages.
	for _, r := range found.Results {
		if under(ctx, s.Store, "page", s.Limits.Pages, a) != nil {
			b.WriteString("\n\nYou've read all the pages you can today; you'll read one of these another day.")
			break
		}
		page, err := s.Fetcher.Fetch(ctx, r.URL)
		if logErr := s.Store.LogWeb(ctx, "page", r.URL, 0, a.Now.UTC()); logErr != nil {
			return ability.Material{}, logErr
		}
		if err != nil {
			continue
		}
		passage, _ := library.Passage(page.Text, 0, passageSize)
		fmt.Fprintf(&b, "\n\nYou're reading %q (%s).%s", orDefault(page.Title, r.Title), page.URL, ability.Fence(passage))
		return ability.Material{Note: b.String(), Project: a.Project, WorkKind: NotesKind}, nil
	}
	b.WriteString("\n\nNone of the pages could be read: write down what you'd look for next, and why.")
	return ability.Material{Note: b.String(), Project: a.Project, WorkKind: NotesKind}, nil
}

// Page reads a page one of its searches found.
type Page struct {
	Fetcher search.Fetcher
	Store   Store
	Limits  Limits
}

func (Page) Name() string { return PageName }

func (Page) Describe() string {
	return "read a page one of your web searches found; query: its exact address (https://...), from your searches."
}

func (Page) Risk() ability.Risk { return ability.ReadsWeb }

func (p Page) Prepare(ctx context.Context, a ability.Act) (ability.Material, error) {
	found, ok, err := p.Store.Found(ctx, a.Query())
	if err != nil {
		return ability.Material{}, err
	}
	if !ok {
		return ability.Material{}, fmt.Errorf("%q didn't come from one of its searches", a.Query())
	}
	if err := under(ctx, p.Store, "page", p.Limits.Pages, a); err != nil {
		return ability.Material{}, err
	}
	page, err := p.Fetcher.Fetch(ctx, found.URL)
	if logErr := p.Store.LogWeb(ctx, "page", found.URL, 0, a.Now.UTC()); logErr != nil {
		return ability.Material{}, logErr
	}
	if err != nil {
		return ability.Material{}, err
	}
	passage, _ := library.Passage(page.Text, 0, passageSize)
	note := fmt.Sprintf("\n\nYou're reading %q (%s), a page you found searching the web.%s", orDefault(page.Title, found.Title),
		page.URL, ability.Fence(passage))
	return ability.Material{Note: note, Project: a.Project, WorkKind: NotesKind}, nil
}

// under checks that it hasn't used today's limit of kind (0: none allowed).
func under(ctx context.Context, st Store, kind string, limit int, a ability.Act) error {
	l := a.Now.In(a.Zone)
	midnight := time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, a.Zone)
	n, err := st.WebSince(ctx, kind, midnight)
	if err != nil {
		return err
	}
	if n >= limit {
		return fmt.Errorf("%d %ss today: %w", n, kind, ErrOverCap)
	}
	return nil
}

func orDefault(s, def string) string {
	if s = strings.TrimSpace(s); s != "" {
		return s
	}
	return def
}
