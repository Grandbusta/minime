// Package console is the owner's mind console: a private, read-only web view
// of everything about the agent, live. Its brain (the parts of its mind,
// firing as decisions flow), what it decided and why, what it remembers,
// its day, what it made, and its conversations.
//
// It never shares a server with the public profile page. It listens on
// localhost, or, anywhere else, only with a token.
package console

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"minime/internal/memory"
	"minime/internal/store"
)

//go:embed web
var web embed.FS

// Store is what the console reads; *store.Store.
type Store interface {
	DecisionLog(ctx context.Context, after time.Time, limit int, newestFirst bool) ([]store.LogEntry, error)
	DecisionChain(ctx context.Context, id ulid.ULID) ([]store.LogEntry, error)
	Vitals(ctx context.Context, since time.Time) (store.Vitals, error)
	CostByDay(ctx context.Context, since time.Time, zone *time.Location) ([]store.DayCost, error)
	Counts(ctx context.Context) (store.Counts, error)
	AllFacts(ctx context.Context, limit int) ([]store.FactView, error)
	AllPeople(ctx context.Context, limit int) ([]memory.Person, error)
	AllProjects(ctx context.Context, limit int) ([]memory.Project, error)
	IntentionsDue(ctx context.Context, from, to time.Time) ([]memory.Intention, error)
	WorksBetween(ctx context.Context, from, to time.Time) ([]memory.Work, error)
	RecentWorks(ctx context.Context, limit int) ([]memory.Work, error)
	Work(ctx context.Context, id ulid.ULID) (memory.Work, bool, error)
	Conversations(ctx context.Context) ([]memory.Conversation, error)
	RecentExchanges(ctx context.Context, source, thread string, since time.Time, exclude ulid.ULID, limit int) ([]memory.Exchange, error)
}

// Info is what the console says about the running agent.
type Info struct {
	Name    string
	Zone    *time.Location
	Started time.Time
	System1 string   // e.g. "openai/gpt-6-luna"
	System2 string   // e.g. "openai/gpt-6-luna (effort low)"
	Sets    []string // question sets and prompts in use, e.g. "incoming_message@1"
}

type Server struct {
	Info  Info
	Store Store
	Token string // required when set; empty only on localhost
	Log   *slog.Logger
	// Poll is how often the live stream looks for new decisions.
	Poll time.Duration
}

func (s *Server) Handler() http.Handler {
	static, _ := fs.Sub(web, "web")
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServer(http.FS(static)))
	mux.HandleFunc("GET /api/overview", s.overview)
	mux.HandleFunc("GET /api/log", s.log)
	mux.HandleFunc("GET /api/chain/{id}", s.chain)
	mux.HandleFunc("GET /api/memory", s.memory)
	mux.HandleFunc("GET /api/day", s.day)
	mux.HandleFunc("GET /api/works", s.works)
	mux.HandleFunc("GET /api/works/{id}", s.work)
	mux.HandleFunc("GET /api/conversation", s.conversation)
	mux.HandleFunc("GET /api/events", s.events)
	return s.auth(headers(mux))
}

const cookieName = "minime_console"

// auth lets a request in with the token: as a cookie, a bearer header, or
// once as ?token= on the page, which sets the cookie.
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.Token == "" || s.tokenOK(r.Header.Get("Authorization"), "Bearer ") {
			next.ServeHTTP(w, r)
			return
		}
		if c, err := r.Cookie(cookieName); err == nil && s.tokenOK(c.Value, "") {
			next.ServeHTTP(w, r)
			return
		}
		if t := r.URL.Query().Get("token"); t != "" && s.tokenOK(t, "") {
			http.SetCookie(w, &http.Cookie{Name: cookieName, Value: s.Token, Path: "/", HttpOnly: true,
				Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: 30 * 24 * 3600})
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		http.Error(w, "Open the console with ?token=… once.", http.StatusUnauthorized)
	})
}

func (s *Server) tokenOK(got, prefix string) bool {
	if len(got) < len(prefix) || got[:len(prefix)] != prefix {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got[len(prefix):]), []byte(s.Token)) == 1
}

func headers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; "+
			"font-src https://fonts.gstatic.com; img-src 'self' data:; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) midnight(t time.Time) time.Time {
	l := t.In(s.Info.Zone)
	return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, s.Info.Zone)
}

func (s *Server) overview(w http.ResponseWriter, r *http.Request) {
	ctx, now := r.Context(), time.Now()
	today := s.midnight(now)
	vitals, err := s.Store.Vitals(ctx, today)
	if s.failed(w, err) {
		return
	}
	costs, err := s.Store.CostByDay(ctx, today.AddDate(0, 0, -13), s.Info.Zone)
	if s.failed(w, err) {
		return
	}
	counts, err := s.Store.Counts(ctx)
	if s.failed(w, err) {
		return
	}
	plan, err := s.Store.IntentionsDue(ctx, today, today.AddDate(0, 0, 1))
	if s.failed(w, err) {
		return
	}
	var next *intentionView
	for _, i := range plan {
		if i.Status == memory.IntentionPending {
			v := s.intention(i)
			next = &v
			break
		}
	}
	byDay := make([]map[string]any, len(costs))
	for i, c := range costs {
		byDay[i] = map[string]any{"day": c.Day, "cost_usd": c.CostUSD, "decisions": c.Count}
	}
	s.json(w, map[string]any{
		"name": s.Info.Name, "timezone": s.Info.Zone.String(), "started_at": s.Info.Started, "now": now,
		"system1": s.Info.System1, "system2": s.Info.System2, "sets": s.Info.Sets,
		"today": map[string]any{
			"decisions": vitals.Decisions, "by_tier": vitals.ByTier, "escalations": vitals.Escalations,
			"cost_usd": vitals.CostUSD, "avg_latency_ms": vitals.AvgLatency.Milliseconds(),
		},
		"cost_by_day": byDay,
		"counts": map[string]int{"facts": counts.Facts, "self_facts": counts.SelfFacts, "people": counts.People,
			"intentions": counts.Intentions, "projects": counts.Projects, "works": counts.Works,
			"percepts": counts.Percepts, "decisions": counts.Decisions},
		"next": next,
	})
}

func (s *Server) log(w http.ResponseWriter, r *http.Request) {
	entries, err := s.Store.DecisionLog(r.Context(), time.Time{}, intParam(r, "limit", 60, 500), true)
	if s.failed(w, err) {
		return
	}
	s.json(w, entryViews(entries))
}

func (s *Server) chain(w http.ResponseWriter, r *http.Request) {
	id, err := ulid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	entries, err := s.Store.DecisionChain(r.Context(), id)
	if s.failed(w, err) {
		return
	}
	s.json(w, entryViews(entries))
}

func (s *Server) memory(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	facts, err := s.Store.AllFacts(ctx, 500)
	if s.failed(w, err) {
		return
	}
	people, err := s.Store.AllPeople(ctx, 500)
	if s.failed(w, err) {
		return
	}
	names := map[string]string{}
	pv := make([]map[string]any, len(people))
	for i, p := range people {
		names[p.Key] = p.Name
		pv[i] = map[string]any{"key": p.Key, "name": p.Name, "relation": p.Relation, "circle": p.Circle,
			"source": p.Source, "first_seen": p.FirstSeen, "updated_at": p.UpdatedAt}
	}
	fv := make([]map[string]any, len(facts))
	for i, f := range facts {
		about := names[f.Subject]
		if f.Subject == memory.SubjectSelf {
			about = s.Info.Name
		}
		fv[i] = map[string]any{"id": f.ID.String(), "subject": f.Subject, "about": about, "text": f.Text, "at": f.At,
			"areas": nonNil(f.Areas), "public": f.Public, "asked_at": f.AskedAt}
	}
	s.json(w, map[string]any{"facts": fv, "people": pv})
}

type intentionView struct {
	ID        string    `json:"id"`
	Subject   string    `json:"subject"`
	Kind      string    `json:"kind"`
	Text      string    `json:"text"`
	DueAt     time.Time `json:"due_at"`
	Status    string    `json:"status"`
	Result    string    `json:"result"`
	ProjectID string    `json:"project_id"`
	Ability   string    `json:"ability"` // e.g. "read_book"; "" if it only writes
	Query     string    `json:"query"`
}

func (s *Server) intention(i memory.Intention) intentionView {
	v := intentionView{ID: i.ID.String(), Subject: i.Subject, Kind: i.Kind, Text: i.Text, DueAt: i.DueAt,
		Status: i.Status, Result: i.Result, Ability: i.Ability, Query: i.Args["query"]}
	if !i.ProjectID.IsZero() {
		v.ProjectID = i.ProjectID.String()
	}
	return v
}

func (s *Server) day(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	from := s.midnight(time.Now())
	if d := r.URL.Query().Get("date"); d != "" {
		t, err := time.ParseInLocation("2006-01-02", d, s.Info.Zone)
		if err != nil {
			http.Error(w, "date must be YYYY-MM-DD", http.StatusBadRequest)
			return
		}
		from = t
	}
	to := from.AddDate(0, 0, 1)
	intentions, err := s.Store.IntentionsDue(ctx, from, to)
	if s.failed(w, err) {
		return
	}
	works, err := s.Store.WorksBetween(ctx, from, to)
	if s.failed(w, err) {
		return
	}
	projects, err := s.Store.AllProjects(ctx, 100)
	if s.failed(w, err) {
		return
	}
	iv := make([]intentionView, len(intentions))
	for i, in := range intentions {
		iv[i] = s.intention(in)
	}
	s.json(w, map[string]any{"date": from.Format("2006-01-02"), "intentions": iv, "works": workViews(works, false),
		"projects": projectViews(projects)})
}

func (s *Server) works(w http.ResponseWriter, r *http.Request) {
	works, err := s.Store.RecentWorks(r.Context(), intParam(r, "limit", 100, 500))
	if s.failed(w, err) {
		return
	}
	s.json(w, workViews(works, false))
}

func (s *Server) work(w http.ResponseWriter, r *http.Request) {
	id, err := ulid.Parse(r.PathValue("id"))
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	work, ok, err := s.Store.Work(r.Context(), id)
	if s.failed(w, err) {
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	s.json(w, workViews([]memory.Work{work}, true)[0])
}

// conversation is the recent thread with a person (or everyone it talks
// with, each with their latest exchanges).
func (s *Server) conversation(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	convs, err := s.Store.Conversations(ctx)
	if s.failed(w, err) {
		return
	}
	person := r.URL.Query().Get("person")
	since := time.Now().AddDate(0, 0, -14)
	out := []map[string]any{}
	for _, c := range convs {
		if person != "" && c.Person != person {
			continue
		}
		ex, err := s.Store.RecentExchanges(ctx, c.Source, c.Thread, since, ulid.ULID{}, 200)
		if s.failed(w, err) {
			return
		}
		ev := make([]map[string]any, len(ex))
		for i, e := range ex {
			ev[i] = map[string]any{"at": e.At, "message": e.Message, "reply": e.Reply}
		}
		out = append(out, map[string]any{"person": c.Person, "source": c.Source, "last_activity": c.LastActivity,
			"exchanges": ev})
	}
	s.json(w, out)
}

// events streams new decisions as they are made (server-sent events), so the
// console's brain fires live.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	fmt.Fprint(w, "retry: 3000\n\n")
	flusher.Flush()

	poll := s.Poll
	if poll <= 0 {
		poll = 1500 * time.Millisecond
	}
	tick, ping := time.NewTicker(poll), time.NewTicker(15*time.Second)
	defer tick.Stop()
	defer ping.Stop()
	last := time.Now().UTC()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case <-tick.C:
			entries, err := s.Store.DecisionLog(r.Context(), last, 100, false)
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					s.Log.Warn("console stream", "err", err)
				}
				continue
			}
			for _, e := range entryViews(entries) {
				b, _ := json.Marshal(e)
				fmt.Fprintf(w, "event: decision\ndata: %s\n\n", b)
				last = e.At
			}
			if len(entries) > 0 {
				flusher.Flush()
			}
		}
	}
}

type entryView struct {
	ID        string          `json:"id"`
	ParentID  string          `json:"parent_id,omitempty"`
	At        time.Time       `json:"at"`
	PerceptID string          `json:"percept_id,omitempty"`
	Set       string          `json:"set"`
	Tier      string          `json:"tier"`
	Provider  string          `json:"provider"`
	Answers   json.RawMessage `json:"answers"`
	Action    string          `json:"action"`
	Reply     string          `json:"reply"`
	LatencyMS int64           `json:"latency_ms"`
	CostUSD   float64         `json:"cost_usd"`
	Outcome   any             `json:"outcome"`
	Source    string          `json:"source"`
	Kind      string          `json:"kind"`
	Text      string          `json:"text"`
	Speaker   string          `json:"speaker"`
}

func entryViews(entries []store.LogEntry) []entryView {
	out := make([]entryView, len(entries))
	for i, e := range entries {
		answers, _ := json.Marshal(e.Answers)
		v := entryView{ID: e.ID.String(), At: e.At, Set: e.SetID, Tier: string(e.Tier), Provider: e.Provider,
			Answers: answers, Action: e.Action, Reply: e.Reply, LatencyMS: e.Latency.Milliseconds(), CostUSD: e.CostUSD,
			Outcome: e.Outcome, Source: e.Source, Kind: e.Kind, Text: e.Text, Speaker: e.Speaker}
		if e.ParentID != nil {
			v.ParentID = e.ParentID.String()
		}
		if !e.PerceptID.IsZero() {
			v.PerceptID = e.PerceptID.String()
		}
		out[i] = v
	}
	return out
}

func workViews(ws []memory.Work, withText bool) []map[string]any {
	out := make([]map[string]any, len(ws))
	for i, w := range ws {
		v := map[string]any{"id": w.ID.String(), "kind": w.Kind, "title": w.Title, "created_at": w.CreatedAt,
			"words": wordCount(w.Text), "public": w.Public, "public_by": w.PublicBy}
		if !w.ProjectID.IsZero() {
			v["project_id"] = w.ProjectID.String()
		}
		if withText {
			v["text"] = w.Text
		} else {
			v["excerpt"] = excerpt(w.Text, 180)
		}
		out[i] = v
	}
	return out
}

func projectViews(ps []memory.Project) []map[string]any {
	out := make([]map[string]any, len(ps))
	for i, p := range ps {
		v := map[string]any{"id": p.ID.String(), "title": p.Title, "kind": p.Kind, "status": p.Status,
			"notes": p.Notes, "created_at": p.CreatedAt, "updated_at": p.UpdatedAt}
		if p.Source != nil {
			v["reading"] = map[string]any{"title": p.Source.Title, "author": p.Source.Author, "percent": p.PercentRead()}
		}
		out[i] = v
	}
	return out
}

// excerpt is the start of s, at most n runes, cut at a word.
func excerpt(s string, n int) string {
	r := []rune(strings.Join(strings.Fields(s), " "))
	if len(r) <= n {
		return string(r)
	}
	cut := string(r[:n])
	if i := strings.LastIndex(cut, " "); i > n/2 {
		cut = cut[:i]
	}
	return cut + "…"
}

func wordCount(s string) int {
	n, in := 0, false
	for _, r := range s {
		space := r == ' ' || r == '\n' || r == '\t'
		if !space && !in {
			n++
		}
		in = !space
	}
	return n
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func intParam(r *http.Request, name string, def, max int) int {
	n, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil || n < 1 {
		return def
	}
	return min(n, max)
}

func (s *Server) json(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		s.Log.Warn("console response", "err", err)
	}
}

func (s *Server) failed(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	s.Log.Error("console", "err", err)
	http.Error(w, "Something went wrong.", http.StatusInternalServerError)
	return true
}

// Serve runs the console on addr until ctx is done. No write timeout: the
// live stream stays open.
func Serve(ctx context.Context, addr string, h http.Handler, log *slog.Logger) error {
	srv := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 120 * time.Second,
		BaseContext: func(net.Listener) context.Context { return ctx }} // live streams end with the agent
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("mind console up", "url", "http://"+addr+"/")
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
		if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}
