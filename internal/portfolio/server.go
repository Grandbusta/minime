package portfolio

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/oklog/ulid/v2"

	"github.com/Grandbusta/minime/internal/memory"
)

//go:embed page.html
var pageHTML string

var pageTemplate = template.Must(template.New("page").Parse(pageHTML))

// Profiles is what the server reads the page from: the store.
type Profiles interface {
	Profile(ctx context.Context) (page []byte, inputs string, at time.Time, ok bool, err error)
	PublicWorks(ctx context.Context, limit int) ([]memory.Work, error)
	PublicWork(ctx context.Context, id ulid.ULID) (memory.Work, bool, error)
}

const (
	pageWorks    = 6   // public works listed on the page, newest
	excerptLines = 4   // lines of a work shown on the page
	excerptRunes = 260 // at most
)

// view is what the template shows: the page, with its works, or one work.
type view struct {
	Name, Initial string
	Page          *Page
	Updated       string
	Works         []workView
	Work          *workView // set on a work's own page
}

type workView struct {
	ID, Title, Kind, Date, Excerpt, Text string
}

func newWorkView(w memory.Work, withText bool) workView {
	v := workView{ID: w.ID.String(), Title: w.Title, Kind: strings.ReplaceAll(w.Kind, "_", " "),
		Date: w.CreatedAt.Format("2 January 2006"), Excerpt: excerpt(w.Text)}
	if withText {
		v.Text = strings.TrimSpace(w.Text)
	}
	return v
}

// excerpt is the start of a work: its first few lines, cut at a word.
func excerpt(text string) string {
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(text), "\n") {
		if l = strings.TrimSpace(l); l != "" && len(lines) < excerptLines {
			lines = append(lines, l)
		}
	}
	out := strings.Join(lines, "\n")
	cut := len(out) < len(strings.TrimSpace(text))
	if r := []rune(out); len(r) > excerptRunes {
		out, cut = string(r[:excerptRunes]), true
		if i := strings.LastIndexAny(out, " \n"); i > excerptRunes/2 {
			out = out[:i]
		}
	}
	if cut {
		out = strings.TrimRight(out, " ,.;:") + "…"
	}
	return out
}

// Handler serves the page at / and each public work at /works/{id},
// read-only, as the agent last wrote them. A work that isn't public is not
// found, as if it didn't exist.
func Handler(name string, profiles Profiles, log *slog.Logger) http.Handler {
	initial, _ := utf8.DecodeRuneInString(name)
	base := view{Name: name, Initial: string(unicode.ToUpper(initial))}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		v := base
		if !loadPage(w, r, profiles, &v, log) {
			return
		}
		works, err := profiles.PublicWorks(r.Context(), pageWorks)
		if err != nil {
			log.Error("load public works", "err", err)
			http.Error(w, "Something went wrong.", http.StatusInternalServerError)
			return
		}
		for _, wk := range works {
			v.Works = append(v.Works, newWorkView(wk, false))
		}
		render(w, v, log)
	})
	mux.HandleFunc("GET /works/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := ulid.Parse(r.PathValue("id"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		wk, ok, err := profiles.PublicWork(r.Context(), id)
		if err != nil {
			log.Error("load public work", "err", err)
			http.Error(w, "Something went wrong.", http.StatusInternalServerError)
			return
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		v := base
		if !loadPage(w, r, profiles, &v, log) {
			return
		}
		wv := newWorkView(wk, true)
		v.Work = &wv
		render(w, v, log)
	})
	return mux
}

// loadPage puts the page, as the agent last wrote it, into v. It reports
// false, having answered with an error, if it couldn't.
func loadPage(w http.ResponseWriter, r *http.Request, profiles Profiles, v *view, log *slog.Logger) bool {
	raw, _, at, ok, err := profiles.Profile(r.Context())
	if err != nil {
		log.Error("load profile", "err", err)
		http.Error(w, "Something went wrong.", http.StatusInternalServerError)
		return false
	}
	if ok {
		var p Page
		if err := json.Unmarshal(raw, &p); err != nil {
			log.Error("decode profile", "err", err)
			http.Error(w, "Something went wrong.", http.StatusInternalServerError)
			return false
		}
		v.Page, v.Updated = &p, at.Format("2 January 2006")
	}
	return true
}

func render(w http.ResponseWriter, v view, log *slog.Logger) {
	var b bytes.Buffer
	if err := pageTemplate.Execute(&b, v); err != nil {
		log.Error("render profile", "err", err)
		http.Error(w, "Something went wrong.", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; img-src data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-cache")
	w.Write(b.Bytes())
}

// Serve runs the page's web server on addr until ctx is done.
func Serve(ctx context.Context, addr string, h http.Handler, log *slog.Logger) error {
	srv := &http.Server{
		Addr: addr, Handler: h,
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("profile page up", "url", "http://"+displayAddr(addr)+"/")
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdown); err != nil {
			return err
		}
		if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

// displayAddr makes ":8080" clickable as "localhost:8080".
func displayAddr(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "localhost" + addr
	}
	return addr
}
