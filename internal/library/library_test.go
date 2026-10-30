package library

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
