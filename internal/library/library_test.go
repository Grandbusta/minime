package library

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestFindBookAndText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("no user agent")
		}
		switch r.URL.Path {
		case "/ebooks/search.opds/":
			if r.URL.Query().Get("query") != "pride and prejudice" {
				t.Errorf("query = %v", r.URL.Query())
			}
			io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom">
<entry><id>https://www.gutenberg.org/ebooks/authors/search.opds/?query=pride</id><title>Authors</title><content type="text">2 authors</content></entry>
<entry><id>https://www.gutenberg.org/ebooks/1342.opds</id><title>Pride and Prejudice</title><content type="text">Austen, Jane</content></entry>
<entry><id>https://www.gutenberg.org/ebooks/42671.opds</id><title>Pride and Prejudice (another)</title><content type="text">Jane Austen</content></entry>
</feed>`)
		case "/ebooks/1342.txt.utf-8":
			http.Redirect(w, r, "/cache/pg1342.txt", http.StatusFound)
		case "/cache/pg1342.txt":
			io.WriteString(w, "Licence blah\r\n*** START OF THE PROJECT GUTENBERG EBOOK ***\r\nChapter 1\r\n\r\nIt is a truth...\r\n*** END OF THE PROJECT GUTENBERG EBOOK ***\r\nmore licence")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, srv.URL)

	b, err := c.FindBook(context.Background(), "pride and prejudice")
	if err != nil || b.ID != 1342 || b.Title != "Pride and Prejudice" || b.Author != "Jane Austen" || b.TextURL != srv.URL+"/ebooks/1342.txt.utf-8" {
		t.Fatalf("book = %+v, %v", b, err)
	}
	text, err := c.BookText(context.Background(), b)
	if err != nil || text != "Chapter 1\n\nIt is a truth..." {
		t.Fatalf("text = %q, %v", text, err)
	}
}

func TestNoBookFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `<feed xmlns="http://www.w3.org/2005/Atom"><entry><id>x/ebooks/authors/search.opds/</id><title>Authors</title></entry></feed>`)
	}))
	defer srv.Close()
	if _, err := New(srv.URL, srv.URL).FindBook(context.Background(), "a modern novel"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want not found", err)
	}
}

func TestAllowList(t *testing.T) {
	evil := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "{}") }))
	defer evil.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, evil.URL+"/x", http.StatusFound)
	}))
	defer srv.Close()
	c := New(srv.URL, srv.URL)
	if _, err := c.get(context.Background(), evil.URL+"/x"); err == nil || !strings.Contains(err.Error(), "allow-list") {
		t.Errorf("fetching an unlisted host: %v", err)
	}
	if _, err := c.get(context.Background(), srv.URL+"/redirect"); err == nil || !strings.Contains(err.Error(), "not allowed") {
		t.Errorf("redirect to an unlisted host: %v", err)
	}
}

func TestFindArticle(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case q.Get("list") == "search" && q.Get("srsearch") == "polyrhythm":
			io.WriteString(w, `{"query":{"search":[{"title":"Polyrhythm"}]}}`)
		case q.Get("list") == "search":
			io.WriteString(w, `{"query":{"search":[]}}`)
		case q.Get("prop") == "extracts" && q.Get("titles") == "Polyrhythm":
			io.WriteString(w, `{"query":{"pages":[{"title":"Polyrhythm","extract":" Polyrhythm is the use of two rhythms. "}]}}`)
		}
	}))
	defer srv.Close()
	c := New(srv.URL, srv.URL)
	a, err := c.FindArticle(context.Background(), "polyrhythm")
	if err != nil || a.Title != "Polyrhythm" || a.Text != "Polyrhythm is the use of two rhythms." || !strings.HasSuffix(a.URL, "/wiki/Polyrhythm") {
		t.Fatalf("article = %+v, %v", a, err)
	}
	if _, err := c.FindArticle(context.Background(), "zzz"); !errors.Is(err, ErrNotFound) {
		t.Errorf("no result: %v", err)
	}
}

func TestPassage(t *testing.T) {
	text := "One.\n\nTwo two two.\n\nThree three three three.\n\nFour."
	p, next := Passage(text, 0, 25)
	if p != "One.\n\nTwo two two." || text[next:next+2] != "\n\n" {
		t.Errorf("first = %q, next %d", p, next)
	}
	var all []string
	for from := 0; from < len(text); {
		var p string
		p, from = Passage(text, from, 25)
		all = append(all, p)
	}
	if strings.Join(all, "|") != "One.\n\nTwo two two.|Three three three three.|Four." {
		t.Errorf("passages = %q", all)
	}
	long := strings.Repeat("é", 100)
	p, next = Passage(long, 0, 51)
	if !utf8.ValidString(p) || !utf8.ValidString(long[next:]) {
		t.Errorf("split a character: %q", p)
	}
}
