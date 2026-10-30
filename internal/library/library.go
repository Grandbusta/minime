// Package library is where the agent reads: public-domain books from
// Project Gutenberg (found through its own OPDS search) and articles from
// Wikipedia. It fetches only from an allow-list of hosts, so nothing it
// reads comes from a page chosen by someone else.
package library

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Defaults: the real services.
const (
	GutenbergURL = "https://www.gutenberg.org"
	WikipediaURL = "https://en.wikipedia.org"
	userAgent    = "minime/1.0 (https://github.com/Grandbusta/minime)"
)

// ErrNotFound means a search found nothing to read.
var ErrNotFound = errors.New("nothing found")

type Client struct {
	Gutenberg string // base URL of Project Gutenberg
	Wikipedia string // base URL of Wikipedia
	MaxBytes  int64  // the most it downloads at once
	http      *http.Client
	allowed   map[string]bool // hosts it may fetch from
}

// New makes a client for the given services (empty: the real ones).
// gutenberg.org redirects books' text within itself, so both of its hosts
// are allowed.
func New(gutenberg, wikipedia string) *Client {
	if gutenberg == "" {
		gutenberg = GutenbergURL
	}
	if wikipedia == "" {
		wikipedia = WikipediaURL
	}
	c := &Client{Gutenberg: strings.TrimRight(gutenberg, "/"), Wikipedia: strings.TrimRight(wikipedia, "/"), MaxBytes: 5 << 20,
		allowed: map[string]bool{"www.gutenberg.org": true, "gutenberg.org": true}}
	for _, base := range []string{c.Gutenberg, c.Wikipedia} {
		if u, err := url.Parse(base); err == nil {
			c.allowed[u.Host] = true
		}
	}
	c.http = &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) > 5 {
			return errors.New("too many redirects")
		}
		if !c.allowed[r.URL.Host] {
			return fmt.Errorf("redirect to %s is not allowed", r.URL.Host)
		}
		if r.URL.Scheme == "http" && via[0].URL.Scheme == "https" {
			r.URL.Scheme = "https" // gutenberg.org redirects to plain http
		}
		return nil
	}}
	return c
}

// attempts: a request that hangs or fails on the server's side is tried again.
const attempts = 3

func (c *Client) get(ctx context.Context, raw string) ([]byte, error) {
	var err error
	for i := 0; i < attempts; i++ {
		var body []byte
		var retry bool
		if body, retry, err = c.getOnce(ctx, raw); err == nil || !retry || ctx.Err() != nil {
			return body, err
		}
	}
	return nil, err
}

// getOnce fetches raw once; retry reports whether trying again could help.
func (c *Client) getOnce(ctx context.Context, raw string) (body []byte, retry bool, err error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, false, err
	}
	if !c.allowed[u.Host] {
		return nil, false, fmt.Errorf("%s is not on the allow-list", u.Host)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, !strings.Contains(err.Error(), "not allowed"), err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests,
			fmt.Errorf("GET %s: %s", u.Host+u.Path, resp.Status)
	}
	body, err = io.ReadAll(io.LimitReader(resp.Body, c.MaxBytes+1))
	if err != nil {
		return nil, true, err
	}
	if int64(len(body)) > c.MaxBytes {
		return nil, false, fmt.Errorf("GET %s: larger than %d bytes", u.Host+u.Path, c.MaxBytes)
	}
	return body, false, nil
}

// Book is a public-domain book on Project Gutenberg.
type Book struct {
	ID      int
	Title   string
	Author  string
	TextURL string // plain text
}

// FindBook returns the book that best matches query, e.g. "Pride and
// Prejudice Austen", from Project Gutenberg's own catalogue search (OPDS).
func (c *Client) FindBook(ctx context.Context, query string) (Book, error) {
	body, err := c.get(ctx, c.Gutenberg+"/ebooks/search.opds/?query="+url.QueryEscape(query))
	if err != nil {
		return Book{}, err
	}
	var feed struct {
		Entries []struct {
			ID      string `xml:"id"`
			Title   string `xml:"title"`
			Content string `xml:"content"` // the author, for a book
		} `xml:"entry"`
	}
	if err := xml.Unmarshal(body, &feed); err != nil {
		return Book{}, fmt.Errorf("gutenberg search: %w", err)
	}
	for _, e := range feed.Entries {
		// Books are ".../ebooks/1342.opds"; the rest are author and subject lists.
		m := bookEntry.FindStringSubmatch(e.ID)
		if m == nil {
			continue
		}
		id, _ := strconv.Atoi(m[1])
		return Book{ID: id, Title: strings.TrimSpace(e.Title), Author: authorName(strings.TrimSpace(e.Content)),
			TextURL: fmt.Sprintf("%s/ebooks/%d.txt.utf-8", c.Gutenberg, id)}, nil
	}
	return Book{}, fmt.Errorf("book %q: %w", query, ErrNotFound)
}

var bookEntry = regexp.MustCompile(`/ebooks/(\d+)\.opds$`)

// authorName turns Gutenberg's "Austen, Jane" into "Jane Austen".
func authorName(s string) string {
	if last, first, ok := strings.Cut(s, ", "); ok {
		return first + " " + last
	}
	return s
}

// BookText returns a book's text without Project Gutenberg's header and licence.
func (c *Client) BookText(ctx context.Context, b Book) (string, error) {
	body, err := c.get(ctx, b.TextURL)
	if err != nil {
		return "", err
	}
	return StripGutenberg(strings.ReplaceAll(string(body), "\r\n", "\n")), nil
}

// StripGutenberg keeps what is between "*** START OF ..." and "*** END OF ...".
func StripGutenberg(text string) string {
	if i := strings.Index(text, "*** START OF"); i >= 0 {
		if j := strings.Index(text[i:], "\n"); j >= 0 {
			text = text[i+j+1:]
		}
	}
	if i := strings.Index(text, "*** END OF"); i >= 0 {
		text = text[:i]
	}
	return strings.TrimSpace(text)
}

// Article is a Wikipedia article as plain text.
type Article struct {
	Title string
	URL   string
	Text  string
}

// FindArticle returns the Wikipedia article that best matches query.
func (c *Client) FindArticle(ctx context.Context, query string) (Article, error) {
	body, err := c.get(ctx, c.Wikipedia+"/w/api.php?action=query&list=search&srlimit=1&format=json&formatversion=2&srsearch="+
		url.QueryEscape(query))
	if err != nil {
		return Article{}, err
	}
	var search struct {
		Query struct {
			Search []struct {
				Title string `json:"title"`
			} `json:"search"`
		} `json:"query"`
	}
	if err := json.Unmarshal(body, &search); err != nil {
		return Article{}, fmt.Errorf("wikipedia search: %w", err)
	}
	if len(search.Query.Search) == 0 {
		return Article{}, fmt.Errorf("article %q: %w", query, ErrNotFound)
	}
	title := search.Query.Search[0].Title
	body, err = c.get(ctx, c.Wikipedia+"/w/api.php?action=query&prop=extracts&explaintext=1&redirects=1&format=json&formatversion=2&titles="+
		url.QueryEscape(title))
	if err != nil {
		return Article{}, err
	}
	var page struct {
		Query struct {
			Pages []struct {
				Title   string `json:"title"`
				Extract string `json:"extract"`
			} `json:"pages"`
		} `json:"query"`
	}
	if err := json.Unmarshal(body, &page); err != nil {
		return Article{}, fmt.Errorf("wikipedia article: %w", err)
	}
	if len(page.Query.Pages) == 0 || strings.TrimSpace(page.Query.Pages[0].Extract) == "" {
		return Article{}, fmt.Errorf("article %q: %w", title, ErrNotFound)
	}
	p := page.Query.Pages[0]
	return Article{Title: p.Title, URL: c.Wikipedia + "/wiki/" + url.PathEscape(strings.ReplaceAll(p.Title, " ", "_")),
		Text: strings.TrimSpace(p.Extract)}, nil
}

// Passage returns the part of text starting at from, about size characters
// long, ended at a paragraph (or failing that, a sentence) break; and where
// the next passage starts. next == len(text) at the end.
func Passage(text string, from, size int) (passage string, next int) {
	for from < len(text) && (text[from] == '\n' || text[from] == ' ' || text[from] == '\r' || text[from] == '\t') {
		from++
	}
	if from >= len(text) {
		return "", len(text)
	}
	end := from + size
	if end >= len(text) {
		return strings.TrimSpace(text[from:]), len(text)
	}
	window := text[from:end]
	cut := strings.LastIndex(window, "\n\n")
	if cut < size/2 {
		cut = strings.LastIndex(window, ".") + 1
	}
	if cut < size/2 {
		cut = len(window)
		for cut > 0 && !utf8.RuneStart(window[cut-1]) { // never split a character
			cut--
		}
		if cut > 0 && window[cut-1] >= utf8.RuneSelf {
			cut--
		}
	}
	return strings.TrimSpace(window[:cut]), from + cut
}
