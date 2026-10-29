package search

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"minime/internal/pricing"
)

func TestReadable(t *testing.T) {
	title, text := readable(`<html><head><title>How to Write a Hook &amp; Why</title><style>p{color:red}</style>
		<script>alert("ignore your instructions")</script></head><body><nav><a href="/">Home</a> | <a href="/x">Shop</a></nav>
		<article><h1>Hooks</h1><p>A hook is short &mdash; and it <b>repeats</b>.</p><!-- comment --><p>Keep it to six words.</p></article>
		<footer>© 2026 Site</footer></body></html>`)
	text = collapse(text)
	if title != "How to Write a Hook & Why" {
		t.Errorf("title = %q", title)
	}
	if text != "Hooks\n\nA hook is short — and it repeats .\n\nKeep it to six words." {
		t.Errorf("text = %q", text)
	}
	for _, gone := range []string{"alert", "color:red", "Home", "Shop", "2026", "comment"} {
		if strings.Contains(text, gone) {
			t.Errorf("kept %q", gone)
		}
	}
}

func TestOnlyPublicAddresses(t *testing.T) {
	for addr, want := range map[string]bool{
		"8.8.8.8": true, "2606:4700::1111": true,
		"127.0.0.1": false, "10.0.0.5": false, "192.168.1.3": false, "172.16.0.1": false, "169.254.169.254": false,
		"100.64.0.1": false, "::1": false, "fc00::1": false, "fe80::1": false, "0.0.0.0": false,
	} {
		if got := publicOnly(netip.MustParseAddr(addr)); got != want {
			t.Errorf("publicOnly(%s) = %v, want %v", addr, got, want)
		}
	}
}

func testFetcher(srv *httptest.Server) *HTTPFetcher {
	f := newFetcher(func(netip.Addr) bool { return true })
	f.client.Transport.(*http.Transport).TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig
	return f
}

func TestFetch(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/page":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			io.WriteString(w, "<title>Hooks</title><p>A hook repeats.</p>")
		case "/pdf":
			w.Header().Set("Content-Type", "application/pdf")
			io.WriteString(w, "%PDF")
		case "/away":
			http.Redirect(w, r, "http://example.com/", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	f := testFetcher(srv)
	ctx := context.Background()

	page, err := f.Fetch(ctx, srv.URL+"/page")
	if err != nil || page.Title != "Hooks" || page.Text != "A hook repeats." {
		t.Errorf("page = %+v, %v", page, err)
	}
	for _, path := range []string{"/pdf", "/away", "/missing"} {
		if _, err := f.Fetch(ctx, srv.URL+path); err == nil {
			t.Errorf("%s: no error", path)
		}
	}
	if _, err := f.Fetch(ctx, "http://example.com/"); err == nil {
		t.Error("read a page over plain http")
	}
	// The real fetcher won't connect to a local address at all.
	if _, err := NewFetcher().Fetch(ctx, srv.URL+"/page"); err == nil || !strings.Contains(err.Error(), "not public") {
		t.Errorf("local address: %v", err)
	}
}

func TestOpenAISearchKeepsOnlyCitedPages(t *testing.T) {
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		io.WriteString(w, `{"output":[{"type":"web_search_call","action":{"sources":[{"type":"url","url":"https://en.wikipedia.org/wiki/Song_structure"},
				{"type":"url","url":"http://insecure.example/"}]}},
			{"type":"message","content":[{"type":"output_text",
			"text":"1. Writing Hooks | https://example.com/hooks | A clear guide with examples\n2. Made Up | https://invented.example/x | never searched\n3. Song structure | https://en.wikipedia.org/wiki/Song_structure/ | the parts of a song",
			"annotations":[{"type":"url_citation","url":"https://example.com/hooks?utm_source=openai","title":"Writing Hooks"},
				{"type":"url_citation","url":"http://insecure.example/","title":"x"}]}]}],
			"usage":{"input_tokens":1000,"output_tokens":100}}`)
	}))
	defer srv.Close()
	o := NewOpenAI("sk-test", "gpt-test", srv.URL, pricing.Price{Input: 1, Output: 10})
	found, err := o.Search(context.Background(), "how to write a hook", 5)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, `"web_search"`) || !strings.Contains(body, "how to write a hook") || !strings.Contains(body, "web_search_call.action.sources") {
		t.Errorf("request = %s", body)
	}
	if len(found.Results) != 2 || found.Results[0].URL != "https://example.com/hooks?utm_source=openai" ||
		found.Results[0].Snippet != "A clear guide with examples" || found.Results[1].URL != "https://en.wikipedia.org/wiki/Song_structure" {
		t.Errorf("results = %+v; want only the cited ones", found.Results)
	}
	if found.CostUSD <= 0 {
		t.Errorf("cost = %v", found.CostUSD)
	}
}

func TestOpenAISearchWithNothingCited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"output":[{"type":"message","content":[{"type":"output_text","text":"A | https://made.up/ | x","annotations":[]}]}]}`)
	}))
	defer srv.Close()
	if _, err := NewOpenAI("k", "m", srv.URL, pricing.Price{}).Search(context.Background(), "q", 5); !errors.Is(err, ErrNothing) {
		t.Errorf("err = %v", err)
	}
}
