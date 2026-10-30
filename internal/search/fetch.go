package search

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
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
		page.Title, page.Text = readable(string(body))
	}
	page.Text = cut(collapse(page.Text), maxTextBytes)
	if strings.TrimSpace(page.Text) == "" {
		return Page{}, fmt.Errorf("fetch %s: %w", u.Host, ErrNothing)
	}
	return page, nil
}

// Elements whose text isn't the page's content, and elements that end a line.
var (
	skipTags  = []string{"script", "style", "noscript", "nav", "header", "footer", "aside", "form", "svg", "iframe", "template", "button", "select"}
	skipRes   = make([]*regexp.Regexp, len(skipTags))
	blockRe   = regexp.MustCompile(`(?i)</?(p|div|br|li|h[1-6]|tr|pre|blockquote|section|article|table|ul|ol)\b[^>]*>`)
	tagRe     = regexp.MustCompile(`(?s)<[^>]*>`)
	commentRe = regexp.MustCompile(`(?s)<!--.*?-->`)
	titleRe   = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
)

func init() {
	for i, t := range skipTags {
		skipRes[i] = regexp.MustCompile(`(?is)<` + t + `\b[^>]*>.*?</` + t + `\s*>`)
	}
}

// readable is an HTML page's title and the text of its content: what's
// left once scripts, styles and the page's chrome (navigation, headers,
// footers, forms) are dropped and the tags removed.
func readable(doc string) (title, text string) {
	if m := titleRe.FindStringSubmatch(doc); m != nil {
		title = strings.TrimSpace(html.UnescapeString(tagRe.ReplaceAllString(m[1], "")))
	}
	doc = commentRe.ReplaceAllString(doc, "")
	doc = titleRe.ReplaceAllString(doc, "")
	for _, re := range skipRes {
		doc = re.ReplaceAllString(doc, " ")
	}
	doc = blockRe.ReplaceAllString(doc, "\n")
	doc = tagRe.ReplaceAllString(doc, " ")
	return title, html.UnescapeString(doc)
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
