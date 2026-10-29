package console

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"minime/internal/decision"
	"minime/internal/memory"
	"minime/internal/store"
)

type fakeStore struct {
	mu  sync.Mutex
	log []store.LogEntry
}

func (f *fakeStore) DecisionLog(_ context.Context, after time.Time, limit int, newestFirst bool) ([]store.LogEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.LogEntry
	for _, e := range f.log {
		if e.At.After(after) && len(out) < limit {
			out = append(out, e)
		}
	}
	return out, nil
}
func (f *fakeStore) DecisionChain(_ context.Context, id ulid.ULID) ([]store.LogEntry, error) {
	return f.log[:1], nil
}
func (f *fakeStore) Vitals(context.Context, time.Time) (store.Vitals, error) {
	return store.Vitals{Decisions: 3, ByTier: map[string]int{"system1": 2, "system2": 1}, Escalations: 1, CostUSD: 0.0005}, nil
}
func (f *fakeStore) CostByDay(context.Context, time.Time, *time.Location) ([]store.DayCost, error) {
	return []store.DayCost{{Day: "2026-10-29", CostUSD: 0.0005, Count: 3}}, nil
}
func (f *fakeStore) Counts(context.Context) (store.Counts, error) {
	return store.Counts{Facts: 5, Works: 1}, nil
}
func (f *fakeStore) AllFacts(context.Context, int) ([]store.FactView, error) {
	yes := true
	return []store.FactView{{Fact: memory.Fact{ID: ulid.Make(), Subject: memory.SubjectSelf, Text: "Tobi likes jollof"}, Public: &yes}}, nil
}
func (f *fakeStore) AllPeople(context.Context, int) ([]memory.Person, error) {
	return []memory.Person{{Key: "telegram:1", Name: "Sam", Circle: "family"}}, nil
}
func (f *fakeStore) AllProjects(context.Context, int) ([]memory.Project, error) { return nil, nil }
func (f *fakeStore) IntentionsDue(context.Context, time.Time, time.Time) ([]memory.Intention, error) {
	return []memory.Intention{{ID: ulid.Make(), Subject: memory.SubjectSelf, Kind: memory.IntentionActivity,
		Text: "Write verse 2", Status: memory.IntentionPending, DueAt: time.Now()}}, nil
}
func (f *fakeStore) WorksBetween(context.Context, time.Time, time.Time) ([]memory.Work, error) {
	return nil, nil
}
func (f *fakeStore) RecentWorks(context.Context, int) ([]memory.Work, error) { return nil, nil }
func (f *fakeStore) Work(context.Context, ulid.ULID) (memory.Work, bool, error) {
	return memory.Work{ID: ulid.Make(), Kind: "lyrics", Title: "Verse", Text: "Rain on the roof"}, true, nil
}
func (f *fakeStore) Conversations(context.Context) ([]memory.Conversation, error) { return nil, nil }
func (f *fakeStore) RecentExchanges(context.Context, string, string, time.Time, ulid.ULID, int) ([]memory.Exchange, error) {
	return nil, nil
}

func (f *fakeStore) add(e store.LogEntry) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.log = append(f.log, e)
}

func entry(action string, at time.Time) store.LogEntry {
	return store.LogEntry{Decision: decision.Decision{ID: ulid.Make(), At: at, SetID: "incoming_message@1",
		Tier: decision.TierSystem1, Action: action, Gate: decision.GateAuto,
		Answers: map[string]decision.Answer{"intent": {Type: decision.QuestionChoice, Choice: action, Confidence: 0.9}}},
		Source: "telegram", Kind: "message", Text: "<b>hi</b>"}
}

func newServer(token string) (*Server, *fakeStore) {
	st := &fakeStore{}
	return &Server{Store: st, Token: token, Poll: 20 * time.Millisecond, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Info: Info{Name: "Tobi", Zone: time.UTC, Started: time.Now(), System1: "fake/1", System2: "fake/2"}}, st
}

func TestTokenIsRequiredWhenSet(t *testing.T) {
	s, _ := newServer("a-long-enough-secret")
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	for _, path := range []string{"/", "/api/overview", "/?token=wrong"} {
		resp, err := client.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s without the token = %d, want 401", path, resp.StatusCode)
		}
	}

	resp, err := client.Get(srv.URL + "/?token=a-long-enough-secret")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	cookies := resp.Cookies()
	if resp.StatusCode != http.StatusSeeOther || len(cookies) != 1 || !cookies[0].HttpOnly {
		t.Fatalf("token link = %d, cookies %+v", resp.StatusCode, cookies)
	}
	req, _ := http.NewRequest("GET", srv.URL+"/api/overview", nil)
	req.AddCookie(cookies[0])
	if resp, err := client.Do(req); err != nil || resp.StatusCode != http.StatusOK {
		t.Errorf("with the cookie: %v, %v", resp, err)
	}
	req, _ = http.NewRequest("GET", srv.URL+"/api/overview", nil)
	req.Header.Set("Authorization", "Bearer a-long-enough-secret")
	if resp, err := client.Do(req); err != nil || resp.StatusCode != http.StatusOK {
		t.Errorf("with the bearer header: %v, %v", resp, err)
	}
}

func TestPageAndAPI(t *testing.T) {
	s, st := newServer("")
	st.add(entry("small_talk", time.Now().Add(-time.Minute)))
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), `<canvas id="brain">`) || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "default-src 'self'") {
		t.Errorf("page: %d, CSP %q", resp.StatusCode, resp.Header.Get("Content-Security-Policy"))
	}
	for _, path := range []string{"/app.js", "/app.css"} {
		if resp, err := http.Get(srv.URL + path); err != nil || resp.StatusCode != http.StatusOK {
			t.Errorf("%s: %v", path, err)
		}
	}

	var o struct {
		Name  string `json:"name"`
		Today struct {
			Decisions int            `json:"decisions"`
			ByTier    map[string]int `json:"by_tier"`
		} `json:"today"`
		Next *struct {
			Text string `json:"text"`
		} `json:"next"`
	}
	getJSON(t, srv.URL+"/api/overview", &o)
	if o.Name != "Tobi" || o.Today.Decisions != 3 || o.Today.ByTier["system1"] != 2 || o.Next == nil || o.Next.Text != "Write verse 2" {
		t.Errorf("overview = %+v", o)
	}

	var log []entryView
	getJSON(t, srv.URL+"/api/log", &log)
	if len(log) != 1 || log[0].Action != "small_talk" || log[0].Text != "<b>hi</b>" || !strings.Contains(string(log[0].Answers), `"choice":"small_talk"`) {
		t.Errorf("log = %+v", log)
	}

	var mem struct {
		Facts []map[string]any `json:"facts"`
	}
	getJSON(t, srv.URL+"/api/memory", &mem)
	if len(mem.Facts) != 1 || mem.Facts[0]["about"] != "Tobi" || mem.Facts[0]["public"] != true {
		t.Errorf("memory = %+v", mem)
	}

	var work map[string]any
	getJSON(t, srv.URL+"/api/works/"+ulid.Make().String(), &work)
	if work["text"] != "Rain on the roof" || work["words"] != float64(4) {
		t.Errorf("work = %+v", work)
	}
	if resp, _ := http.Get(srv.URL + "/api/chain/not-an-id"); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad chain id = %d", resp.StatusCode)
	}
}

func TestEventsStreamNewDecisions(t *testing.T) {
	s, st := newServer("")
	st.add(entry("old", time.Now().Add(-time.Hour))) // before connecting: not streamed
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/events", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type = %q", ct)
	}
	time.Sleep(50 * time.Millisecond)
	st.add(entry("share", time.Now()))

	r := bufio.NewReader(resp.Body)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("stream ended before the decision: %v", err)
		}
		if strings.HasPrefix(line, "data: ") {
			var e entryView
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &e); err != nil || e.Action != "share" {
				t.Fatalf("event = %q, %v; want the new decision only", line, err)
			}
			return
		}
	}
}

func getJSON(t *testing.T, url string, v any) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s = %d", url, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("%s: %v", url, err)
	}
}
