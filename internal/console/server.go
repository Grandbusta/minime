// Package console is the owner's mind console: a private live web view of
// everything about the agent. It writes only what the owner reviews: labels on
// decisions, and their judgment.
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
	"mime"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/Grandbusta/minime/internal/memory"
	"github.com/Grandbusta/minime/internal/render"
	"github.com/Grandbusta/minime/internal/store"
)

//go:embed web
var web embed.FS

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
	RecentApprovals(ctx context.Context, limit int) ([]memory.Approval, error)
	Labels(ctx context.Context, decisions []ulid.ULID) ([]memory.Label, error)
	SaveLabel(ctx context.Context, l memory.Label) error
	Dreams(ctx context.Context, activeOnly bool) ([]memory.Dream, error)
	Goals(ctx context.Context, activeOnly bool) ([]memory.Goal, error)
	Skills(ctx context.Context) ([]memory.Skill, error)
	RecentEvidence(ctx context.Context, skill string, limit int) ([]memory.Evidence, error)
	LatestWork(ctx context.Context, kind string) (memory.Work, bool, error)
	Lessons(ctx context.Context, skill string, limit int) ([]memory.Lesson, error)
	Plans(ctx context.Context, activeOnly bool) ([]memory.LearningPlan, error)
	Resources(ctx context.Context, plan ulid.ULID) ([]memory.Resource, error)
	Principles(ctx context.Context, status string) ([]memory.Principle, error)
	JudgmentCases(ctx context.Context, status string, types []string) ([]memory.JudgmentCase, error)
	ReviewPrinciple(ctx context.Context, id ulid.ULID, status, text string, at time.Time) (bool, error)
	RankPriority(ctx context.Context, id ulid.ULID, rank int, at time.Time) (bool, error)
	ReviewCase(ctx context.Context, id ulid.ULID, status, reason string, at time.Time) (bool, error)
	RecentCalls(ctx context.Context, status string, limit int) ([]memory.Call, error)
	Agreement(ctx context.Context, n int) ([]memory.Agreement, error)
	AgreementByStakes(ctx context.Context, n int) ([]memory.Agreement, error)
	Call(ctx context.Context, id ulid.ULID) (memory.Call, bool, error)
	AnswerCall(ctx context.Context, c memory.Call, asCase bool) (bool, error)
	GiveCallReason(ctx context.Context, id ulid.ULID, reason string, at time.Time) error
	SetOutcome(ctx context.Context, id ulid.ULID, outcome, verdict, note, by string, at time.Time) (bool, error)
	SaveTaste(ctx context.Context, t memory.Taste) error
	Tastes(ctx context.Context, limit int) ([]memory.Taste, error)
	RetireTaste(ctx context.Context, id ulid.ULID, at time.Time) (bool, error)
}

const agreementOver = 20

type Info struct {
	Name    string
	Zone    *time.Location
	Started time.Time
	System1 string
	System2 string
	Sets    []string
}

type Server struct {
	Info  Info
	Store Store
	Token string // required when set; empty only on localhost
	Log   *slog.Logger
	Poll  time.Duration
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
	mux.HandleFunc("GET /api/works/{id}/pdf", s.workFile(render.PDF))
	mux.HandleFunc("GET /api/works/{id}/midi", s.workFile(render.MIDI))
	mux.HandleFunc("GET /api/conversation", s.conversation)
	mux.HandleFunc("GET /api/events", s.events)
	mux.HandleFunc("GET /api/approvals", s.approvals)
	mux.HandleFunc("POST /api/labels", s.label)
	mux.HandleFunc("GET /api/growth", s.growth)
	mux.HandleFunc("GET /api/judgment", s.judgment)
	mux.HandleFunc("POST /api/judgment", s.reviewJudgment)
	mux.HandleFunc("POST /api/judgment/calls", s.gradeCall)
	mux.HandleFunc("POST /api/judgment/outcomes", s.judgeOutcome)
	mux.HandleFunc("POST /api/tastes", s.addTaste)
	mux.HandleFunc("POST /api/tastes/retire", s.retireTaste)
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
	views := entryViews(entries)
	ids := make([]ulid.ULID, len(entries))
	for i, e := range entries {
		ids[i] = e.ID
	}
	labels, err := s.Store.Labels(r.Context(), ids)
	if s.failed(w, err) {
		return
	}
	for _, l := range labels {
		for i := range views {
			if views[i].ID == l.DecisionID.String() {
				if views[i].Labels == nil {
					views[i].Labels = map[string]string{}
				}
				views[i].Labels[l.Question] = l.Value
			}
		}
	}
	s.json(w, views)
}

// fromConsole is whether a write comes from the console's own page (same
// origin), as JSON: the only writes it takes.
func fromConsole(w http.ResponseWriter, r *http.Request) bool {
	if site := r.Header.Get("Sec-Fetch-Site"); (site != "" && site != "same-origin") || r.Header.Get("Content-Type") != "application/json" {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	return true
}

func (s *Server) label(w http.ResponseWriter, r *http.Request) {
	if !fromConsole(w, r) {
		return
	}
	var in struct {
		Decision string `json:"decision_id"`
		Question string `json:"question"`
		Value    string `json:"value"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil {
		http.Error(w, "bad label", http.StatusBadRequest)
		return
	}
	id, err := ulid.Parse(in.Decision)
	in.Question, in.Value = strings.TrimSpace(in.Question), strings.TrimSpace(in.Value)
	if err != nil || in.Question == "" || in.Value == "" || len(in.Question) > 100 || len(in.Value) > 100 {
		http.Error(w, "bad label", http.StatusBadRequest)
		return
	}
	if s.failed(w, s.Store.SaveLabel(r.Context(), memory.Label{DecisionID: id, Question: in.Question, Value: in.Value,
		Source: memory.LabelFromConsole, At: time.Now().UTC()})) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
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
	Ability   string    `json:"ability"`
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

func (s *Server) workFile(format string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
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
		f, err := render.Work(format, work, s.Info.Name)
		if errors.Is(err, render.ErrNoScore) {
			http.NotFound(w, r)
			return
		}
		if s.failed(w, err) {
			return
		}
		w.Header().Set("Content-Type", f.Type)
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": f.Name}))
		w.Write(f.Data)
	}
}

// conversation is the thread with a person, or with no person, everyone's
// latest exchanges.
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

// approvals is read-only: the owner answers on Telegram.
func (s *Server) approvals(w http.ResponseWriter, r *http.Request) {
	as, err := s.Store.RecentApprovals(r.Context(), intParam(r, "limit", 20, 100))
	if s.failed(w, err) {
		return
	}
	people := map[string]string{}
	if ps, err := s.Store.AllPeople(r.Context(), 500); err == nil {
		for _, p := range ps {
			people[p.Key] = p.Name
		}
	}
	out := make([]map[string]any, len(as))
	for i, a := range as {
		v := map[string]any{"id": a.ID.String(), "class": a.Class, "person": a.Person, "name": people[a.Person], "what": a.What,
			"text": a.Text, "kind": a.Kind, "status": a.Status, "asked_at": a.AskedAt, "sent": !a.SentAt.IsZero()}
		if !a.DecidedAt.IsZero() {
			v["decided_at"] = a.DecidedAt
		}
		out[i] = v
	}
	s.json(w, out)
}

func (s *Server) growth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	dreams, err := s.Store.Dreams(ctx, false)
	if s.failed(w, err) {
		return
	}
	goals, err := s.Store.Goals(ctx, false)
	if s.failed(w, err) {
		return
	}
	projects, err := s.Store.AllProjects(ctx, 500)
	if s.failed(w, err) {
		return
	}
	skills, err := s.Store.Skills(ctx)
	if s.failed(w, err) {
		return
	}
	plans, err := s.Store.Plans(ctx, false)
	if s.failed(w, err) {
		return
	}
	planFor := map[ulid.ULID]map[string]any{}
	for _, p := range plans {
		rs, err := s.Store.Resources(ctx, p.ID)
		if s.failed(w, err) {
			return
		}
		stages := make([]map[string]any, len(p.Stages))
		for i, st := range p.Stages {
			var res []map[string]any
			for _, r := range rs {
				if r.Stage == st.N {
					res = append(res, map[string]any{"ability": r.Ability, "query": r.Query, "why": r.Why, "status": r.Status, "title": r.Title})
				}
			}
			stages[i] = map[string]any{"learn": st.Learn, "practice": st.Practice, "done_when": st.DoneWhen, "done": !st.DoneAt.IsZero(),
				"resources": nonNilMaps(res)}
		}
		planFor[p.GoalID] = map[string]any{"id": p.ID.String(), "skill": p.Skill, "stage": p.Stage, "status": p.Status, "stages": stages}
	}
	dv := make([]map[string]any, 0, len(dreams))
	for _, d := range dreams {
		var gv []map[string]any
		for _, g := range goals {
			if g.DreamID != d.ID {
				continue
			}
			var pv []map[string]any
			for _, p := range projects {
				if p.GoalID == g.ID {
					pv = append(pv, map[string]any{"id": p.ID.String(), "title": p.Title, "status": p.Status})
				}
			}
			gv = append(gv, map[string]any{"id": g.ID.String(), "text": g.Text, "done_when": g.DoneWhen, "due": g.Due,
				"status": g.Status, "progress": g.Progress, "created_at": g.CreatedAt, "projects": nonNilMaps(pv), "skill": g.Skill,
				"plan": planFor[g.ID]})
		}
		v := map[string]any{"id": d.ID.String(), "text": d.Text, "status": d.Status, "source": d.Source, "since": d.Since,
			"goals": nonNilMaps(gv)}
		if !d.EndedAt.IsZero() {
			v["ended_at"] = d.EndedAt
		}
		dv = append(dv, v)
	}
	sv := make([]map[string]any, 0, len(skills))
	for _, sk := range skills {
		ev, err := s.Store.RecentEvidence(ctx, sk.Name, 12)
		if s.failed(w, err) {
			return
		}
		evv := make([]map[string]any, len(ev))
		for i, e := range ev {
			evv[i] = map[string]any{"score": e.Score, "source": e.Source, "note": e.Note, "at": e.At}
			if !e.WorkID.IsZero() {
				evv[i]["work_id"] = e.WorkID.String()
			}
		}
		ls, err := s.Store.Lessons(ctx, sk.Name, 20)
		if s.failed(w, err) {
			return
		}
		lv := make([]map[string]any, len(ls))
		for i, l := range ls {
			lv[i] = map[string]any{"text": l.Text, "source": l.Source, "at": l.At}
		}
		sv = append(sv, map[string]any{"name": sk.Name, "level": sk.Level, "shown_by": nonNil(sk.ShownBy),
			"updated_at": sk.UpdatedAt, "evidence": evv, "lessons": lv})
	}
	out := map[string]any{"dreams": dv, "skills": sv}
	if rv, ok, err := s.Store.LatestWork(ctx, "review"); err == nil && ok {
		out["review"] = map[string]any{"id": rv.ID.String(), "title": rv.Title, "text": rv.Text, "at": rv.CreatedAt}
	}
	s.json(w, out)
}

func (s *Server) judgment(w http.ResponseWriter, r *http.Request) {
	ps, err := s.Store.Principles(r.Context(), "")
	if s.failed(w, err) {
		return
	}
	cs, err := s.Store.JudgmentCases(r.Context(), "", nil)
	if s.failed(w, err) {
		return
	}
	pv := make([]map[string]any, len(ps))
	for i, p := range ps {
		pv[i] = map[string]any{"id": p.ID.String(), "text": p.Text, "kind": p.Kind, "rank": p.Rank, "types": nonNil(p.Types), "status": p.Status, "source": p.Source,
			"edited": p.Seed != "" && p.Seed != p.Text, "cases": nonNil(p.Cases), "updated_at": p.UpdatedAt}
	}
	cv := make([]map[string]any, len(cs))
	for i, c := range cs {
		cv[i] = map[string]any{"id": c.ID.String(), "key": c.Key, "type": c.Type, "situation": c.Situation, "options": nonNil(c.Options),
			"choice": c.Choice, "reason": c.Reason, "status": c.Status, "source": c.Source, "at": c.At}
	}
	calls, err := s.Store.RecentCalls(r.Context(), "", 60)
	if s.failed(w, err) {
		return
	}
	agreement, err := s.Store.Agreement(r.Context(), agreementOver)
	if s.failed(w, err) {
		return
	}
	byStakes, err := s.Store.AgreementByStakes(r.Context(), agreementOver)
	if s.failed(w, err) {
		return
	}
	callv := make([]map[string]any, len(calls))
	for i, c := range calls {
		v := map[string]any{"id": c.ID.String(), "type": c.Type, "mode": c.Mode, "situation": c.Situation, "options": nonNil(c.Options),
			"pick": c.Pick, "why": c.Why, "confidence": c.Confidence, "status": c.Status, "waiting": c.Waiting, "created_at": c.CreatedAt,
			"answered_by": c.AnsweredBy, "owner_choice": c.OwnerChoice, "owner_reason": c.OwnerReason, "agreed": c.Agreed,
			"outcome": c.Outcome, "verdict": c.Verdict, "outcome_note": c.OutcomeNote, "outcome_by": c.OutcomeBy,
			"stakes": c.Stakes, "reversible": c.Reversible}
		if c.Agreed != nil {
			v["owner_pick"] = c.OwnerPick
		}
		callv[i] = v
	}
	views := func(as []memory.Agreement) []map[string]any {
		out := make([]map[string]any, len(as))
		for i, a := range as {
			out[i] = map[string]any{"type": a.Type, "answered": a.Answered, "agreed": a.Agreed}
		}
		return out
	}
	tastes, err := s.Store.Tastes(r.Context(), 100)
	if s.failed(w, err) {
		return
	}
	tv := make([]map[string]any, len(tastes))
	for i, t := range tastes {
		tv[i] = map[string]any{"id": t.ID.String(), "kind": t.Kind, "likes": t.Likes, "text": t.Text, "source": t.Source, "at": t.At}
		if !t.WorkID.IsZero() {
			tv[i]["work_id"] = t.WorkID.String()
		}
	}
	s.json(w, map[string]any{"principles": pv, "cases": cv, "calls": callv, "tastes": tv, "agreement": views(agreement),
		"agreement_by_stakes": views(byStakes), "agreement_over": agreementOver})
}

// gradeCall records what the owner would have chosen on one of its calls
// that they weren't asked about, or didn't answer in time; or their reason
// for an answer that has none.
func (s *Server) gradeCall(w http.ResponseWriter, r *http.Request) {
	if !fromConsole(w, r) {
		return
	}
	var in struct {
		ID     string `json:"id"`
		Option int    `json:"option"` // -1: their own answer, in choice
		Choice string `json:"choice"`
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&in); err != nil {
		http.Error(w, "bad grade", http.StatusBadRequest)
		return
	}
	id, err := ulid.Parse(in.ID)
	in.Choice, in.Reason = strings.TrimSpace(in.Choice), strings.TrimSpace(in.Reason)
	if err != nil || len([]rune(in.Choice)) > 1000 || len([]rune(in.Reason)) > 1000 {
		http.Error(w, "bad grade", http.StatusBadRequest)
		return
	}
	c, ok, err := s.Store.Call(r.Context(), id)
	if s.failed(w, err) {
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	now := time.Now().UTC()
	switch c.Status {
	case memory.CallAnswered: // only a reason can be added
		if in.Reason == "" || c.OwnerReason != "" {
			http.Error(w, "already answered", http.StatusConflict)
			return
		}
		if s.failed(w, s.Store.GiveCallReason(r.Context(), c.ID, in.Reason, now)) {
			return
		}
	case memory.CallLogged, memory.CallExpired:
		if in.Option < -1 || in.Option >= len(c.Options) || (in.Option == -1 && in.Choice == "") {
			http.Error(w, "bad grade", http.StatusBadRequest)
			return
		}
		c.OwnerPick, c.OwnerChoice, c.OwnerReason, c.AnsweredBy, c.AnsweredAt, c.Waiting = in.Option, in.Choice, in.Reason,
			memory.AnsweredInConsole, now, ""
		if in.Option >= 0 {
			c.OwnerChoice = ""
		}
		answered, err := s.Store.AnswerCall(r.Context(), c, true)
		if s.failed(w, err) {
			return
		}
		if !answered {
			http.Error(w, "already answered", http.StatusConflict)
			return
		}
	default: // asked on Telegram: answered there, where it acts on the answer
		http.Error(w, "answer it on Telegram", http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) reviewJudgment(w http.ResponseWriter, r *http.Request) {
	if !fromConsole(w, r) {
		return
	}
	var in struct {
		Kind   string `json:"kind"` // principle | case
		ID     string `json:"id"`
		Status string `json:"status"`
		Text   string `json:"text"` // a principle's new text, or a decision's reason; empty keeps it
		Rank   int    `json:"rank"` // a priority's new place, 1 first; 0 keeps it
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&in); err != nil {
		http.Error(w, "bad review", http.StatusBadRequest)
		return
	}
	id, err := ulid.Parse(in.ID)
	in.Text = strings.TrimSpace(in.Text)
	switch in.Status {
	case memory.JudgmentProposed, memory.JudgmentApproved, memory.JudgmentRejected:
	default:
		err = errors.New("bad status")
	}
	if err != nil || len([]rune(in.Text)) > 1000 {
		http.Error(w, "bad review", http.StatusBadRequest)
		return
	}
	var found bool
	switch in.Kind {
	case "principle":
		found, err = s.Store.ReviewPrinciple(r.Context(), id, in.Status, in.Text, time.Now().UTC())
		if err == nil && found && in.Rank > 0 {
			found, err = s.Store.RankPriority(r.Context(), id, in.Rank, time.Now().UTC())
		}
	case "case":
		found, err = s.Store.ReviewCase(r.Context(), id, in.Status, in.Text, time.Now().UTC())
	default:
		http.Error(w, "bad review", http.StatusBadRequest)
		return
	}
	if s.failed(w, err) {
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) judgeOutcome(w http.ResponseWriter, r *http.Request) {
	if !fromConsole(w, r) {
		return
	}
	var in struct {
		ID      string `json:"id"`
		Outcome string `json:"outcome"`
		Verdict string `json:"verdict"`
		Note    string `json:"note"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&in); err != nil {
		http.Error(w, "bad outcome", http.StatusBadRequest)
		return
	}
	id, err := ulid.Parse(in.ID)
	in.Note = strings.TrimSpace(in.Note)
	if err != nil || !slices.Contains(memory.Outcomes, in.Outcome) || !slices.Contains(memory.Verdicts, in.Verdict) || len([]rune(in.Note)) > 1000 {
		http.Error(w, "bad outcome", http.StatusBadRequest)
		return
	}
	c, ok, err := s.Store.Call(r.Context(), id)
	if s.failed(w, err) {
		return
	}
	if !ok || c.Status == memory.CallAsked {
		http.Error(w, "no such call to judge", http.StatusNotFound)
		return
	}
	if _, err := s.Store.SetOutcome(r.Context(), id, in.Outcome, in.Verdict, in.Note, memory.OutcomeByOwner, time.Now().UTC()); s.failed(w, err) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) addTaste(w http.ResponseWriter, r *http.Request) {
	if !fromConsole(w, r) {
		return
	}
	var in struct {
		WorkID string `json:"work_id"` // the work it's about; "" none
		Likes  bool   `json:"likes"`
		Text   string `json:"text"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&in); err != nil {
		http.Error(w, "bad taste", http.StatusBadRequest)
		return
	}
	in.Text = strings.TrimSpace(in.Text)
	if in.Text == "" || len([]rune(in.Text)) > 300 {
		http.Error(w, "bad taste", http.StatusBadRequest)
		return
	}
	t := memory.Taste{ID: ulid.Make(), Likes: in.Likes, Text: in.Text, Source: memory.TasteFromConsole, At: time.Now().UTC()}
	if in.WorkID != "" {
		id, err := ulid.Parse(in.WorkID)
		if err != nil {
			http.Error(w, "bad taste", http.StatusBadRequest)
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
		t.WorkID, t.Kind = work.ID, work.Kind
	}
	if s.failed(w, s.Store.SaveTaste(r.Context(), t)) {
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) retireTaste(w http.ResponseWriter, r *http.Request) {
	if !fromConsole(w, r) {
		return
	}
	var in struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&in); err != nil {
		http.Error(w, "bad taste", http.StatusBadRequest)
		return
	}
	id, err := ulid.Parse(in.ID)
	if err != nil {
		http.Error(w, "bad taste", http.StatusBadRequest)
		return
	}
	ok, err := s.Store.RetireTaste(r.Context(), id, time.Now().UTC())
	if s.failed(w, err) {
		return
	}
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func nonNilMaps(m []map[string]any) []map[string]any {
	if m == nil {
		return []map[string]any{}
	}
	return m
}

type entryView struct {
	ID        string            `json:"id"`
	ParentID  string            `json:"parent_id,omitempty"`
	At        time.Time         `json:"at"`
	PerceptID string            `json:"percept_id,omitempty"`
	Set       string            `json:"set"`
	Tier      string            `json:"tier"`
	Provider  string            `json:"provider"`
	Answers   json.RawMessage   `json:"answers"`
	Action    string            `json:"action"`
	Gate      string            `json:"gate"`
	Labels    map[string]string `json:"labels,omitempty"`
	Reply     string            `json:"reply"`
	LatencyMS int64             `json:"latency_ms"`
	CostUSD   float64           `json:"cost_usd"`
	Outcome   any               `json:"outcome"`
	Source    string            `json:"source"`
	Kind      string            `json:"kind"`
	Text      string            `json:"text"`
	Speaker   string            `json:"speaker"`
}

func entryViews(entries []store.LogEntry) []entryView {
	out := make([]entryView, len(entries))
	for i, e := range entries {
		answers, _ := json.Marshal(e.Answers)
		v := entryView{ID: e.ID.String(), At: e.At, Set: e.SetID, Tier: string(e.Tier), Provider: e.Provider,
			Answers: answers, Action: e.Action, Gate: string(e.Gate), Reply: e.Reply, LatencyMS: e.Latency.Milliseconds(), CostUSD: e.CostUSD,
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
			"words": wordCount(w.Text), "public": w.Public, "public_by": w.PublicBy, "has_score": len(w.Score) > 0}
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

// Serve has no write timeout: the live stream stays open.
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
