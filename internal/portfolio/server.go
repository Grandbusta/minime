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
)

//go:embed page.html
var pageHTML string

var pageTemplate = template.Must(template.New("page").Parse(pageHTML))

// Profiles is what the server reads the page from: the store.
type Profiles interface {
	Profile(ctx context.Context) (page []byte, inputs string, at time.Time, ok bool, err error)
}

// Handler serves the page at /, read-only, as the agent last wrote it.
func Handler(name string, profiles Profiles, log *slog.Logger) http.Handler {
	initial, _ := utf8.DecodeRuneInString(name)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		view := struct {
			Name, Initial string
			Page          *Page
			Updated       string
		}{Name: name, Initial: string(unicode.ToUpper(initial))}

		raw, _, at, ok, err := profiles.Profile(r.Context())
		if err != nil {
			log.Error("load profile", "err", err)
			http.Error(w, "Something went wrong.", http.StatusInternalServerError)
			return
		}
		if ok {
			var p Page
			if err := json.Unmarshal(raw, &p); err != nil {
				log.Error("decode profile", "err", err)
				http.Error(w, "Something went wrong.", http.StatusInternalServerError)
				return
			}
			view.Page, view.Updated = &p, at.Format("2 January 2006")
		}

		var b bytes.Buffer
		if err := pageTemplate.Execute(&b, view); err != nil {
			log.Error("render profile", "err", err)
			http.Error(w, "Something went wrong.", http.StatusInternalServerError)
			return
		}
		h := w.Header()
		h.Set("Content-Type", "text/html; charset=utf-8")
		h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-cache")
		w.Write(b.Bytes())
	})
	return mux
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
