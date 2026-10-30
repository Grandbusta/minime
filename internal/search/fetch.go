package search

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"codeberg.org/readeck/go-readability/v2"
)

const (
	maxPageBytes = 2 << 20 // what it downloads at most
	maxTextBytes = 60000   // what it keeps of a page's text
	userAgent    = "minime/1.0 (https://github.com/Grandbusta/minime)"
)

// HTTPFetcher reads public https pages as plain text.
type HTTPFetcher struct {
	client *http.Client
}

// NewFetcher returns a fetcher that only connects to public addresses: no
// localhost, private networks or link-local (cloud metadata) addresses, even
// through a redirect or a name that resolves to one.
func NewFetcher() *HTTPFetcher {
	return newFetcher(publicOnly)
}

func newFetcher(allow func(netip.Addr) bool) *HTTPFetcher {
	dialer := &net.Dialer{Timeout: 10 * time.Second, Control: func(_, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		ip, err := netip.ParseAddr(host)
		if err != nil || !allow(ip.Unmap()) {
			return fmt.Errorf("address %s is not public", host)
		}
		return nil
	}}
	transport := &http.Transport{DialContext: dialer.DialContext, TLSHandshakeTimeout: 10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second, MaxIdleConns: 4}
	return &HTTPFetcher{client: &http.Client{Transport: transport, Timeout: 30 * time.Second,
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			if len(via) > 5 {
				return errors.New("too many redirects")
			}
			if r.URL.Scheme != "https" {
				return errors.New("redirected away from https")
			}
			return nil
		}}}
}

// publicOnly is whether an address is on the public internet.
func publicOnly(ip netip.Addr) bool {
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() &&
		!netip.MustParsePrefix("100.64.0.0/10").Contains(ip) // carrier-grade NAT
}

// Fetch reads a page: https only, HTML or plain text, its readable text.
func (f *HTTPFetcher) Fetch(ctx context.Context, raw string) (Page, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return Page{}, fmt.Errorf("%q is not an https address", raw)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Page{}, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html, text/plain;q=0.9")
	resp, err := f.client.Do(req)
	if err != nil {
		return Page{}, fmt.Errorf("fetch %s: %w", u.Host, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Page{}, fmt.Errorf("fetch %s: %s", u.Host, resp.Status)
	}
	kind, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if kind != "text/html" && kind != "text/plain" && kind != "application/xhtml+xml" {
		return Page{}, fmt.Errorf("fetch %s: not a page it can read (%s)", u.Host, kind)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes))
	if err != nil {
		return Page{}, fmt.Errorf("fetch %s: %w", u.Host, err)
	}
	if !utf8.Valid(body) {
		body = []byte(strings.ToValidUTF8(string(body), " "))
	}
	page := Page{URL: resp.Request.URL.String()}
	if kind == "text/plain" {
		page.Text = string(body)
	} else {
		if page.Title, page.Text, err = readable(string(body), resp.Request.URL); err != nil {
			return Page{}, fmt.Errorf("fetch %s: %w", u.Host, ErrNothing)
		}
	}
	page.Text = cut(collapse(page.Text), maxTextBytes)
	if strings.TrimSpace(page.Text) == "" {
		return Page{}, fmt.Errorf("fetch %s: %w", u.Host, ErrNothing)
	}
	return page, nil
}

// readable is an HTML page's title and the text of its content, by
// Readability (the algorithm behind Firefox's reader view): what's left
// once scripts, styles and the page's chrome (navigation, headers, footers,
// sidebars, forms) are dropped.
func readable(doc string, page *url.URL) (title, text string, err error) {
	article, err := readability.FromReader(strings.NewReader(doc), page)
	if err != nil {
		return "", "", err
	}
	var b strings.Builder
	if err := article.RenderText(&b); err != nil {
		return "", "", err
	}
	return strings.TrimSpace(article.Title()), b.String(), nil
}

// collapse trims each line, squeezes spaces, and keeps at most one blank line in a row.
func collapse(s string) string {
	var out []string
	blank := false
	for _, line := range strings.Split(s, "\n") {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			if !blank && len(out) > 0 {
				out = append(out, "")
			}
			blank = true
			continue
		}
		out, blank = append(out, line), false
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// cut keeps at most n bytes of s, ending at a whole rune.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
