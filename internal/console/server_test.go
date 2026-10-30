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
	mu     sync.Mutex
	log    []store.LogEntry
	labels []memory.Label
	// principles and cases are its owner's judgment; reviews are what the owner did to them
	principles []memory.Principle
	cases      []memory.JudgmentCase
	calls      []memory.Call
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

func (f *fakeStore) RecentApprovals(context.Context, int) ([]memory.Approval, error) {
	return []memory.Approval{{ID: ulid.Make(), Class: "reach_out", Person: "telegram:1", What: "message Sam first (reminder)",
		Text: "Don't forget the bank!", Status: memory.ApprovalPending, AskedAt: time.Now()}}, nil
}

func (f *fakeStore) Labels(context.Context, []ulid.ULID) ([]memory.Label, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.labels, nil
}

func (f *fakeStore) SaveLabel(_ context.Context, l memory.Label) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.labels = append(f.labels, l)
	return nil
}

func (f *fakeStore) Dreams(context.Context, bool) ([]memory.Dream, error) {
	return []memory.Dream{{ID: testDream, Text: "Tobi wants to be a producer", Status: memory.DreamActive, Source: memory.DreamFromLife}}, nil
}
func (f *fakeStore) Goals(context.Context, bool) ([]memory.Goal, error) {
	return []memory.Goal{{ID: testGoal, DreamID: testDream, Text: "Five beat ideas", Status: memory.GoalActive}}, nil
}
func (f *fakeStore) Skills(context.Context) ([]memory.Skill, error) {
	return []memory.Skill{{Name: "music production", Level: 4.6, ShownBy: []string{"beat_idea"}}}, nil
}
func (f *fakeStore) RecentEvidence(context.Context, string, int) ([]memory.Evidence, error) {
	return []memory.Evidence{{Skill: "music production", Score: 5.9, Source: memory.EvidenceSystem1, WorkID: ulid.Make()}}, nil
}
func (f *fakeStore) Lessons(context.Context, string, int) ([]memory.Lesson, error) {
	return []memory.Lesson{{Skill: "music production", Text: "Leave space after the drop", Source: memory.LessonFromOwner}}, nil
}
func (f *fakeStore) Plans(context.Context, bool) ([]memory.LearningPlan, error) {
	return []memory.LearningPlan{{ID: testPlan, GoalID: testGoal, Skill: "music production", Stage: 1, Status: memory.PlanActive,
		Stages: []memory.Stage{{N: 0, Learn: "drums", Practice: "a beat"}, {N: 1, Learn: "melody", Practice: "a hook"}}}}, nil
}
func (f *fakeStore) Resources(context.Context, ulid.ULID) ([]memory.Resource, error) {
	return []memory.Resource{{Stage: 1, Ability: "read_article", Query: "melody", Status: memory.ResourceTodo}}, nil
}

var testPlan, testGoal = ulid.Make(), ulid.Make()

func (f *fakeStore) LatestWork(context.Context, string) (memory.Work, bool, error) {
	return memory.Work{Kind: "review", Title: "A busy week", Text: "..."}, true, nil
}

var testDream = ulid.Make()

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
	var growth struct {
		Dreams []struct {
			Text  string           `json:"text"`
			Goals []map[string]any `json:"goals"`
		} `json:"dreams"`
		Skills []struct {
			Level    float64          `json:"level"`
			Evidence []map[string]any `json:"evidence"`
			Lessons  []map[string]any `json:"lessons"`
		} `json:"skills"`
		Review map[string]any `json:"review"`
	}
	getJSON(t, srv.URL+"/api/growth", &growth)
	plan, _ := growth.Dreams[0].Goals[0]["plan"].(map[string]any)
	if plan == nil || plan["stage"] != float64(1) || len(plan["stages"].([]any)) != 2 {
		t.Errorf("plan = %+v", plan)
	}
	if len(growth.Dreams) != 1 || len(growth.Dreams[0].Goals) != 1 || growth.Skills[0].Level != 4.6 ||
		growth.Skills[0].Evidence[0]["score"] != 5.9 || growth.Skills[0].Lessons[0]["source"] != "owner" || growth.Review["title"] != "A busy week" {
		t.Errorf("growth = %+v", growth)
	}
	var approvals []map[string]any
	getJSON(t, srv.URL+"/api/approvals", &approvals)
	if len(approvals) != 1 || approvals[0]["name"] != "Sam" || approvals[0]["status"] != "pending" || approvals[0]["sent"] != false {
		t.Errorf("approvals = %+v", approvals)
	}
	if log[0].Gate != "auto" {
		t.Errorf("gate = %q", log[0].Gate)
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

func TestLabelling(t *testing.T) {
	s, st := newServer("")
	e := entry("small_talk", time.Now().Add(-time.Minute))
	st.add(e)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	post := func(body string, headers map[string]string) int {
		req, _ := http.NewRequest("POST", srv.URL+"/api/labels", strings.NewReader(body))
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	good := `{"decision_id":"` + e.ID.String() + `","question":"intent","value":"ask_for_help"}`
	jsonHeader := map[string]string{"Content-Type": "application/json"}
	if code := post(good, map[string]string{"Content-Type": "application/json", "Sec-Fetch-Site": "cross-site"}); code != http.StatusForbidden {
		t.Errorf("cross-site = %d", code)
	}
	if code := post(good, map[string]string{"Content-Type": "text/plain"}); code != http.StatusForbidden {
		t.Errorf("not JSON = %d", code)
	}
	if code := post(`{"decision_id":"x","question":"intent","value":"y"}`, jsonHeader); code != http.StatusBadRequest {
		t.Errorf("bad id = %d", code)
	}
	if code := post(good, jsonHeader); code != http.StatusNoContent {
		t.Fatalf("label = %d", code)
	}
	if len(st.labels) != 1 || st.labels[0].Value != "ask_for_help" || st.labels[0].Source != memory.LabelFromConsole {
		t.Errorf("labels = %+v", st.labels)
	}
	var chain []entryView
	getJSON(t, srv.URL+"/api/chain/"+e.ID.String(), &chain)
	if len(chain) != 1 || chain[0].Labels["intent"] != "ask_for_help" {
		t.Errorf("chain = %+v", chain)
	}
}

func (f *fakeStore) Principles(context.Context, string) ([]memory.Principle, error) {
	return f.principles, nil
}
func (f *fakeStore) JudgmentCases(context.Context, string, []string) ([]memory.JudgmentCase, error) {
	return f.cases, nil
}

func (f *fakeStore) ReviewPrinciple(_ context.Context, id ulid.ULID, status, text string, _ time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, p := range f.principles {
		if p.ID == id {
			f.principles[i].Status = status
			if text != "" {
				f.principles[i].Text = text
			}
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeStore) RankPriority(_ context.Context, id ulid.ULID, rank int, _ time.Time) (bool, error) {
	for i := range f.principles {
		if f.principles[i].ID == id && f.principles[i].Kind == memory.PrincipleRank {
			f.principles[i].Rank = rank
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeStore) ReviewCase(_ context.Context, id ulid.ULID, status, reason string, _ time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, c := range f.cases {
		if c.ID == id {
			f.cases[i].Status = status
			if reason != "" {
				f.cases[i].Reason = reason
			}
			return true, nil
		}
	}
	return false, nil
}

func TestJudgmentReview(t *testing.T) {
	s, st := newServer("")
	p := memory.Principle{ID: ulid.Make(), Text: "Start small.", Seed: "Start small.", Types: []string{"general"}, Cases: []string{"tiny"},
		Status: memory.JudgmentProposed}
	c := memory.JudgmentCase{ID: ulid.Make(), Key: "tiny", Type: "general", Situation: "What to build first.", Choice: "the tiniest part",
		Status: memory.JudgmentProposed}
	st.principles, st.cases = []memory.Principle{p}, []memory.JudgmentCase{c}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	post := func(body string, headers map[string]string) int {
		req, _ := http.NewRequest("POST", srv.URL+"/api/judgment", strings.NewReader(body))
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	jsonHeader := map[string]string{"Content-Type": "application/json"}
	edit := `{"kind":"principle","id":"` + p.ID.String() + `","status":"approved","text":"Start small, then build on it."}`
	if code := post(edit, map[string]string{"Content-Type": "application/json", "Sec-Fetch-Site": "cross-site"}); code != http.StatusForbidden {
		t.Errorf("cross-site = %d", code)
	}
	for name, body := range map[string]string{
		"bad status": `{"kind":"principle","id":"` + p.ID.String() + `","status":"maybe"}`,
		"bad kind":   `{"kind":"dream","id":"` + p.ID.String() + `","status":"approved"}`,
		"bad id":     `{"kind":"case","id":"x","status":"approved"}`,
	} {
		if code := post(body, jsonHeader); code != http.StatusBadRequest {
			t.Errorf("%s = %d", name, code)
		}
	}
	if code := post(`{"kind":"case","id":"`+ulid.Make().String()+`","status":"approved"}`, jsonHeader); code != http.StatusNotFound {
		t.Errorf("unknown case = %d", code)
	}
	if code := post(edit, jsonHeader); code != http.StatusNoContent {
		t.Fatalf("edit = %d", code)
	}
	if code := post(`{"kind":"case","id":"`+c.ID.String()+`","status":"rejected","text":"I'd go bigger now"}`, jsonHeader); code != http.StatusNoContent {
		t.Fatalf("reject = %d", code)
	}
	var got struct {
		Principles []map[string]any `json:"principles"`
		Cases      []map[string]any `json:"cases"`
	}
	getJSON(t, srv.URL+"/api/judgment", &got)
	if len(got.Principles) != 1 || got.Principles[0]["text"] != "Start small, then build on it." || got.Principles[0]["status"] != "approved" ||
		got.Principles[0]["edited"] != true {
		t.Errorf("principles = %+v", got.Principles)
	}
	if len(got.Cases) != 1 || got.Cases[0]["status"] != "rejected" || got.Cases[0]["reason"] != "I'd go bigger now" {
		t.Errorf("cases = %+v", got.Cases)
	}
}

func (f *fakeStore) RecentCalls(context.Context, string, int) ([]memory.Call, error) {
	return f.calls, nil
}
func (f *fakeStore) Agreement(context.Context, int) ([]memory.Agreement, error) {
	var a []memory.Agreement
	for _, c := range f.calls {
		if c.Agreed != nil {
			a = append(a, memory.Agreement{Type: c.Type, Answered: 1, Agreed: map[bool]int{true: 1}[*c.Agreed]})
		}
	}
	return a, nil
}

func (f *fakeStore) Call(_ context.Context, id ulid.ULID) (memory.Call, bool, error) {
	for _, c := range f.calls {
		if c.ID == id {
			return c, true, nil
		}
	}
	return memory.Call{}, false, nil
}

func (f *fakeStore) AnswerCall(_ context.Context, c memory.Call, _ bool) (bool, error) {
	for i := range f.calls {
		if f.calls[i].ID == c.ID && f.calls[i].Status != memory.CallAnswered {
			agreed := c.OwnerPick == c.Pick
			c.Status, c.Agreed = memory.CallAnswered, &agreed
			f.calls[i] = c
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeStore) GiveCallReason(_ context.Context, id ulid.ULID, reason string, _ time.Time) error {
	for i := range f.calls {
		if f.calls[i].ID == id {
			f.calls[i].OwnerReason = reason
		}
	}
	return nil
}

func TestGradeCalls(t *testing.T) {
	s, st := newServer("")
	logged := memory.Call{ID: ulid.Make(), Type: memory.CallFocus, Situation: "What to focus on", Options: []string{"a", "b"}, Pick: 0,
		Status: memory.CallLogged}
	asked := memory.Call{ID: ulid.Make(), Type: memory.CallFocus, Situation: "s", Options: []string{"a", "b"}, Status: memory.CallAsked}
	st.calls = []memory.Call{logged, asked}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	post := func(body string) int {
		req, _ := http.NewRequest("POST", srv.URL+"/api/judgment/calls", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	steps := []struct {
		name, body string
		want       int
	}{
		{"out of range", `{"id":"` + logged.ID.String() + `","option":2}`, http.StatusBadRequest},
		{"something else, empty", `{"id":"` + logged.ID.String() + `","option":-1}`, http.StatusBadRequest},
		{"asked on Telegram", `{"id":"` + asked.ID.String() + `","option":0}`, http.StatusConflict},
		{"unknown", `{"id":"` + ulid.Make().String() + `","option":0}`, http.StatusNotFound},
		{"graded", `{"id":"` + logged.ID.String() + `","option":1,"reason":"rest first"}`, http.StatusNoContent},
		{"graded again", `{"id":"` + logged.ID.String() + `","option":0,"reason":"x"}`, http.StatusConflict},
	}
	for _, step := range steps {
		if code := post(step.body); code != step.want {
			t.Errorf("%s = %d, want %d", step.name, code, step.want)
		}
	}
	var got struct {
		Calls     []map[string]any `json:"calls"`
		Agreement []map[string]any `json:"agreement"`
	}
	getJSON(t, srv.URL+"/api/judgment", &got)
	if len(got.Calls) != 2 || got.Calls[0]["agreed"] != false || got.Calls[0]["owner_pick"] != float64(1) || got.Calls[0]["owner_reason"] != "rest first" ||
		got.Calls[0]["answered_by"] != memory.AnsweredInConsole {
		t.Errorf("calls = %+v", got.Calls)
	}
	if len(got.Agreement) != 1 || got.Agreement[0]["answered"] != float64(1) || got.Agreement[0]["agreed"] != float64(0) {
		t.Errorf("agreement = %+v", got.Agreement)
	}
}

func (f *fakeStore) SetOutcome(_ context.Context, id ulid.ULID, outcome, verdict, note, by string, _ time.Time) (bool, error) {
	for i := range f.calls {
		if f.calls[i].ID == id {
			f.calls[i].Outcome, f.calls[i].Verdict, f.calls[i].OutcomeNote, f.calls[i].OutcomeBy = outcome, verdict, note, by
			return true, nil
		}
	}
	return false, nil
}

func TestJudgeOutcome(t *testing.T) {
	s, st := newServer("")
	done := memory.Call{ID: ulid.Make(), Type: memory.CallFocus, Options: []string{"a", "b"}, Status: memory.CallAnswered,
		Outcome: memory.OutcomeWell, Verdict: memory.VerdictGood, OutcomeBy: memory.OutcomeBySelf}
	asked := memory.Call{ID: ulid.Make(), Type: memory.CallFocus, Options: []string{"a", "b"}, Status: memory.CallAsked}
	st.calls = []memory.Call{done, asked}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	post := func(body string) int {
		req, _ := http.NewRequest("POST", srv.URL+"/api/judgment/outcomes", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	for name, step := range map[string]struct {
		body string
		want int
	}{
		"bad verdict": {`{"id":"` + done.ID.String() + `","outcome":"went_well","verdict":"meh"}`, http.StatusBadRequest},
		"bad outcome": {`{"id":"` + done.ID.String() + `","outcome":"great","verdict":"lucky"}`, http.StatusBadRequest},
		"still asked": {`{"id":"` + asked.ID.String() + `","outcome":"went_well","verdict":"lucky"}`, http.StatusNotFound},
	} {
		if code := post(step.body); code != step.want {
			t.Errorf("%s = %d, want %d", name, code, step.want)
		}
	}
	if code := post(`{"id":"` + done.ID.String() + `","outcome":"went_well","verdict":"lucky","note":"the deadline moved"}`); code != http.StatusNoContent {
		t.Fatalf("judge = %d", code)
	}
	var got struct {
		Calls []map[string]any `json:"calls"`
	}
	getJSON(t, srv.URL+"/api/judgment", &got)
	if got.Calls[0]["verdict"] != "lucky" || got.Calls[0]["outcome_by"] != "owner" || got.Calls[0]["outcome_note"] != "the deadline moved" {
		t.Errorf("call = %+v", got.Calls[0])
	}
}
