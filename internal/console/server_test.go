package console

import (
	"bytes"
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

	"github.com/Grandbusta/minime/internal/decision"
	"github.com/Grandbusta/minime/internal/memory"
	"github.com/Grandbusta/minime/internal/render"
	"github.com/Grandbusta/minime/internal/render/midi"
	"github.com/Grandbusta/minime/internal/render/pdf"
	"github.com/Grandbusta/minime/internal/store"
)

// fakeStore implements what the tests reach; anything else panics through the nil Store.
type fakeStore struct {
	Store
	score      json.RawMessage // the work's
	mu         sync.Mutex
	log        []store.LogEntry
	labels     []memory.Label
	principles []memory.Principle
	cases      []memory.JudgmentCase
	calls      []memory.Call
	tastes     []memory.Taste
}

func (f *fakeStore) DecisionLog(_ context.Context, after time.Time, limit int, _ bool) ([]store.LogEntry, error) {
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
func (f *fakeStore) DecisionChain(context.Context, ulid.ULID) ([]store.LogEntry, error) {
	return f.log[:1], nil
}
func (f *fakeStore) Labels(context.Context, []ulid.ULID) ([]memory.Label, error) {
	return f.labels, nil
}
func (f *fakeStore) SaveLabel(_ context.Context, l memory.Label) error {
	f.labels = append(f.labels, l)
	return nil
}
func (f *fakeStore) Work(context.Context, ulid.ULID) (memory.Work, bool, error) {
	return memory.Work{ID: ulid.Make(), Kind: "lyrics", Title: "Verse", Text: "Rain on the roof", Score: f.score}, true, nil
}

func (f *fakeStore) ReviewPrinciple(_ context.Context, id ulid.ULID, status, text string, _ time.Time) (bool, error) {
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
func (f *fakeStore) ReviewCase(_ context.Context, id ulid.ULID, status, reason string, _ time.Time) (bool, error) {
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
func (f *fakeStore) SetOutcome(_ context.Context, id ulid.ULID, outcome, verdict, note, by string, _ time.Time) (bool, error) {
	for i := range f.calls {
		if f.calls[i].ID == id {
			f.calls[i].Outcome, f.calls[i].Verdict, f.calls[i].OutcomeNote, f.calls[i].OutcomeBy = outcome, verdict, note, by
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeStore) SaveTaste(_ context.Context, t memory.Taste) error {
	f.tastes = append(f.tastes, t)
	return nil
}
func (f *fakeStore) RetireTaste(_ context.Context, id ulid.ULID, at time.Time) (bool, error) {
	for i := range f.tastes {
		if f.tastes[i].ID == id && f.tastes[i].RetiredAt.IsZero() {
			f.tastes[i].RetiredAt = at
			return true, nil
		}
	}
	return false, nil
}

func entry(action string, at time.Time) store.LogEntry {
	return store.LogEntry{Decision: decision.Decision{ID: ulid.Make(), At: at, SetID: "incoming_message@1",
		Tier: decision.TierSystem1, Action: action, Gate: decision.GateAuto}, Source: "telegram", Kind: "message", Text: "<b>hi</b>"}
}

func newServer(t *testing.T, token string) (*httptest.Server, *fakeStore) {
	st := &fakeStore{}
	s := &Server{Store: st, Formats: render.NewSet(pdf.Format{}, midi.Format{}), Token: token, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Info: Info{Name: "Tobi", Zone: time.UTC, Started: time.Now(), System1: "fake/1", System2: "fake/2"}}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, st
}

// post sends body as JSON from the console's own page, plus any extra headers.
func post(t *testing.T, url, body string, headers ...string) int {
	t.Helper()
	req, _ := http.NewRequest("POST", url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestTokenIsRequiredWhenSet(t *testing.T) {
	srv, _ := newServer(t, "a-long-enough-secret")
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	for _, path := range []string{"/", "/api/log", "/?token=wrong"} {
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
	req, _ := http.NewRequest("GET", srv.URL+"/api/log", nil)
	req.AddCookie(cookies[0])
	if resp, err := client.Do(req); err != nil || resp.StatusCode != http.StatusOK {
		t.Errorf("with the cookie: %v, %v", resp, err)
	}
	req, _ = http.NewRequest("GET", srv.URL+"/api/log", nil)
	req.Header.Set("Authorization", "Bearer a-long-enough-secret")
	if resp, err := client.Do(req); err != nil || resp.StatusCode != http.StatusOK {
		t.Errorf("with the bearer header: %v, %v", resp, err)
	}
}

func TestPageAndLog(t *testing.T) {
	srv, st := newServer(t, "")
	st.log = append(st.log, entry("small_talk", time.Now().Add(-time.Minute)))

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), `<canvas id="brain">`) || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "default-src 'self'") {
		t.Errorf("page: %d, CSP %q", resp.StatusCode, resp.Header.Get("Content-Security-Policy"))
	}
	resp, err = http.Get(srv.URL + "/api/log")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(body), `"small_talk"`) {
		t.Errorf("log = %s", body)
	}
	if resp, _ := http.Get(srv.URL + "/api/chain/not-an-id"); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("bad chain id = %d", resp.StatusCode)
	}
}

func TestLabelling(t *testing.T) {
	srv, st := newServer(t, "")
	e := entry("small_talk", time.Now().Add(-time.Minute))
	st.log = append(st.log, e)
	url := srv.URL + "/api/labels"
	good := `{"decision_id":"` + e.ID.String() + `","question":"intent","value":"ask_for_help"}`
	if code := post(t, url, good, "Sec-Fetch-Site", "cross-site"); code != http.StatusForbidden {
		t.Errorf("cross-site = %d", code)
	}
	if code := post(t, url, good, "Content-Type", "text/plain"); code != http.StatusForbidden {
		t.Errorf("not JSON = %d", code)
	}
	if code := post(t, url, `{"decision_id":"x","question":"intent","value":"y"}`); code != http.StatusBadRequest {
		t.Errorf("bad id = %d", code)
	}
	if code := post(t, url, good); code != http.StatusNoContent {
		t.Fatalf("label = %d", code)
	}
	if len(st.labels) != 1 || st.labels[0].Value != "ask_for_help" || st.labels[0].Source != memory.LabelFromConsole {
		t.Errorf("labels = %+v", st.labels)
	}
}

func TestJudgmentReview(t *testing.T) {
	srv, st := newServer(t, "")
	p := memory.Principle{ID: ulid.Make(), Text: "Start small.", Status: memory.JudgmentProposed}
	c := memory.JudgmentCase{ID: ulid.Make(), Key: "tiny", Choice: "the tiniest part", Status: memory.JudgmentProposed}
	st.principles, st.cases = []memory.Principle{p}, []memory.JudgmentCase{c}
	url := srv.URL + "/api/judgment"
	edit := `{"kind":"principle","id":"` + p.ID.String() + `","status":"approved","text":"Start small, then build on it."}`
	if code := post(t, url, edit, "Sec-Fetch-Site", "cross-site"); code != http.StatusForbidden {
		t.Errorf("cross-site = %d", code)
	}
	if code := post(t, url, `{"kind":"principle","id":"`+p.ID.String()+`","status":"maybe"}`); code != http.StatusBadRequest {
		t.Errorf("bad status = %d", code)
	}
	if code := post(t, url, `{"kind":"case","id":"`+ulid.Make().String()+`","status":"approved"}`); code != http.StatusNotFound {
		t.Errorf("unknown case = %d", code)
	}
	if code := post(t, url, edit); code != http.StatusNoContent {
		t.Fatalf("edit = %d", code)
	}
	if code := post(t, url, `{"kind":"case","id":"`+c.ID.String()+`","status":"rejected","text":"I'd go bigger now"}`); code != http.StatusNoContent {
		t.Fatalf("reject = %d", code)
	}
	if got := st.principles[0]; got.Status != memory.JudgmentApproved || got.Text != "Start small, then build on it." {
		t.Errorf("principle = %+v", got)
	}
	if got := st.cases[0]; got.Status != memory.JudgmentRejected || got.Reason != "I'd go bigger now" {
		t.Errorf("case = %+v", got)
	}
}

func TestGradeCalls(t *testing.T) {
	srv, st := newServer(t, "")
	logged := memory.Call{ID: ulid.Make(), Type: memory.CallFocus, Options: []string{"a", "b"}, Pick: 0, Status: memory.CallLogged}
	asked := memory.Call{ID: ulid.Make(), Type: memory.CallFocus, Options: []string{"a", "b"}, Status: memory.CallAsked}
	st.calls = []memory.Call{logged, asked}
	for _, step := range []struct {
		name, body string
		want       int
	}{
		{"out of range", `{"id":"` + logged.ID.String() + `","option":2}`, http.StatusBadRequest},
		{"asked on Telegram", `{"id":"` + asked.ID.String() + `","option":0}`, http.StatusConflict},
		{"graded", `{"id":"` + logged.ID.String() + `","option":1,"reason":"rest first"}`, http.StatusNoContent},
		{"graded again", `{"id":"` + logged.ID.String() + `","option":0,"reason":"x"}`, http.StatusConflict},
	} {
		if code := post(t, srv.URL+"/api/judgment/calls", step.body); code != step.want {
			t.Errorf("%s = %d, want %d", step.name, code, step.want)
		}
	}
	if c := st.calls[0]; c.Agreed == nil || *c.Agreed || c.OwnerPick != 1 || c.OwnerReason != "rest first" || c.AnsweredBy != memory.AnsweredInConsole {
		t.Errorf("call = %+v", c)
	}
}

func TestJudgeOutcome(t *testing.T) {
	srv, st := newServer(t, "")
	done := memory.Call{ID: ulid.Make(), Type: memory.CallFocus, Options: []string{"a", "b"}, Status: memory.CallAnswered,
		Outcome: memory.OutcomeWell, Verdict: memory.VerdictGood, OutcomeBy: memory.OutcomeBySelf}
	asked := memory.Call{ID: ulid.Make(), Type: memory.CallFocus, Options: []string{"a", "b"}, Status: memory.CallAsked}
	st.calls = []memory.Call{done, asked}
	url := srv.URL + "/api/judgment/outcomes"
	if code := post(t, url, `{"id":"`+done.ID.String()+`","outcome":"went_well","verdict":"meh"}`); code != http.StatusBadRequest {
		t.Errorf("bad verdict = %d", code)
	}
	if code := post(t, url, `{"id":"`+asked.ID.String()+`","outcome":"went_well","verdict":"lucky"}`); code != http.StatusNotFound {
		t.Errorf("still asked = %d", code)
	}
	if code := post(t, url, `{"id":"`+done.ID.String()+`","outcome":"went_well","verdict":"lucky","note":"the deadline moved"}`); code != http.StatusNoContent {
		t.Fatalf("judge = %d", code)
	}
	if c := st.calls[0]; c.Verdict != "lucky" || c.OutcomeBy != memory.OutcomeByOwner || c.OutcomeNote != "the deadline moved" {
		t.Errorf("call = %+v", c)
	}
}

func TestTastes(t *testing.T) {
	srv, st := newServer(t, "")
	if code := post(t, srv.URL+"/api/tastes", `{"likes":true,"text":"  "}`); code != http.StatusBadRequest {
		t.Errorf("empty taste = %d", code)
	}
	if code := post(t, srv.URL+"/api/tastes", `{"work_id":"`+ulid.Make().String()+`","likes":false,"text":"long intros"}`); code != http.StatusNoContent {
		t.Fatalf("taste = %d", code)
	}
	if len(st.tastes) != 1 || st.tastes[0].Kind != "lyrics" || st.tastes[0].Likes || st.tastes[0].Source != memory.TasteFromConsole {
		t.Fatalf("tastes = %+v", st.tastes)
	}
	retire := `{"id":"` + st.tastes[0].ID.String() + `"}`
	if code := post(t, srv.URL+"/api/tastes/retire", retire); code != http.StatusNoContent {
		t.Errorf("retire = %d", code)
	}
	if code := post(t, srv.URL+"/api/tastes/retire", retire); code != http.StatusNotFound {
		t.Errorf("retire again = %d", code)
	}
}

func TestWorkFiles(t *testing.T) {
	srv, st := newServer(t, "")
	get := func(path string) (*http.Response, []byte) {
		resp, err := http.Get(srv.URL + "/api/works/" + ulid.Make().String() + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp, body
	}
	if resp, body := get("/pdf"); resp.StatusCode != 200 || !bytes.HasPrefix(body, []byte("%PDF-")) ||
		!strings.Contains(resp.Header.Get("Content-Disposition"), "verse.pdf") {
		t.Errorf("pdf: %d %s", resp.StatusCode, resp.Header)
	}
	if resp, _ := get("/midi"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("midi without a score: %d", resp.StatusCode)
	}
	st.score = json.RawMessage(`{"bpm":82,"beats_per_bar":4,"bars":1,"tracks":[{"name":"d","instrument":"drums","notes":[{"pitch":"kick","bar":1,"beat":1,"length":1,"velocity":100}]}]}`)
	if resp, body := get("/midi"); resp.StatusCode != 200 || !bytes.HasPrefix(body, []byte("MThd")) || resp.Header.Get("Content-Type") != "audio/midi" {
		t.Errorf("midi: %d %s", resp.StatusCode, resp.Header)
	}
}
