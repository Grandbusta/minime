package portfolio

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"minime/internal/decision"
	"minime/internal/decision/questions"
	"minime/internal/memory"
	"minime/internal/mind"
	"minime/internal/providers/system1"
	"minime/internal/providers/system2"
	"minime/internal/self"
)

type fakeStore struct {
	facts     []memory.Fact
	public    map[ulid.ULID]bool // decided facts
	page      []byte
	inputs    string
	at        time.Time
	decisions []decision.Decision
}

func (s *fakeStore) UndecidedPublicFacts(_ context.Context, limit int) ([]memory.Fact, error) {
	var out []memory.Fact
	for _, f := range s.facts {
		if _, decided := s.public[f.ID]; !decided && f.Subject == memory.SubjectSelf && len(out) < limit {
			out = append(out, f)
		}
	}
	return out, nil
}

func (s *fakeStore) SetFactPublic(_ context.Context, id ulid.ULID, public bool) error {
	s.public[id] = public
	return nil
}

func (s *fakeStore) PublicFacts(_ context.Context, limit int) ([]memory.Fact, error) {
	var out []memory.Fact
	for _, f := range s.facts {
		if s.public[f.ID] && f.Subject == memory.SubjectSelf {
			out = append(out, f)
		}
	}
	return out, nil
}

func (s *fakeStore) Profile(context.Context) ([]byte, string, time.Time, bool, error) {
	return s.page, s.inputs, s.at, s.page != nil, nil
}

func (s *fakeStore) SaveProfile(_ context.Context, page []byte, inputs string, at time.Time) error {
	s.page, s.inputs, s.at = page, inputs, at
	return nil
}

func (s *fakeStore) SaveDecision(_ context.Context, d decision.Decision) error {
	s.decisions = append(s.decisions, d)
	return nil
}

// fakeS1 answers each question with the probability keyed by the fact text
// it points at; a missing text is refused.
type fakeS1 struct {
	probs map[string]float64
	reqs  []system1.Request
}

func (f *fakeS1) Decide(_ context.Context, req system1.Request) (system1.Response, error) {
	f.reqs = append(f.reqs, req)
	texts := req.State.(map[string]any)["facts"].([]string)
	answers := map[string]decision.Answer{}
	for i, q := range req.Questions {
		p, ok := f.probs[texts[i]]
		answers[q.Name] = decision.Answer{Type: decision.QuestionPredicate, Probability: p, Refused: !ok}
	}
	return system1.Response{Provider: "fake/s1", Answers: answers, CostUSD: 0.001}, nil
}

type fakeS2 struct {
	out  string
	reqs []system2.Request
}

func (f *fakeS2) Reason(_ context.Context, req system2.Request) (system2.Response, error) {
	f.reqs = append(f.reqs, req)
	return system2.Response{Provider: "fake/s2", Text: f.out, CostUSD: 0.01}, nil
}

func fact(subject, text string) memory.Fact {
	return memory.Fact{ID: ulid.Make(), Subject: subject, Text: text, At: time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)}
}

func newWriter(st *fakeStore, s1 *fakeS1, s2 *fakeS2) *Writer {
	return &Writer{
		Life:  self.Life{Name: "Tobi", Story: []string{"Tobi is 29 years old."}, Personality: []string{"Tobi is kind."}},
		Store: st, System1: s1, System2: s2,
		Public: questions.Set{ID: "public_profile", Version: 1, Primary: "public",
			Questions:  []system1.Question{{Name: "public", Type: decision.QuestionPredicate, Instructions: "Is {fact} fine for {agent} to share?"}},
			Thresholds: questions.Thresholds{Act: 0.75, Ask: 0.75}},
		Prompt: mind.Prompt{Ref: "profile@test", Text: "You are {name}.\n{life_story}\n{personality}\nShared:\n{facts}"},
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

const pageJSON = `{"tagline":" Beats and books. ","about":["I'm Tobi.","  "],"interests":["music"],"now":["Reading Things Fall Apart"]}`

func TestRefreshDecidesPublicFactsAndWritesThePage(t *testing.T) {
	green, address, pin := fact(memory.SubjectSelf, "Tobi's favourite colour is green"),
		fact(memory.SubjectSelf, "Tobi lives at 14 Adeola Street"), fact(memory.SubjectSelf, "Tobi's PIN is 4821")
	theirs := fact("telegram:1", "Sam is worried about money")
	st := &fakeStore{facts: []memory.Fact{green, address, pin, theirs}, public: map[ulid.ULID]bool{}}
	s1 := &fakeS1{probs: map[string]float64{green.Text: 0.99, address.Text: 0.1}} // the PIN is refused
	s2 := &fakeS2{out: pageJSON}
	w := newWriter(st, s1, s2)

	w.refresh(context.Background())

	if len(s1.reqs) != 1 || len(s1.reqs[0].Questions) != 3 {
		t.Fatalf("system 1 requests = %+v, want one with the agent's three facts", s1.reqs)
	}
	if got := s1.reqs[0].Questions[1].Instructions; got != "Is `facts[1]` fine for Tobi to share?" {
		t.Errorf("instructions = %q", got)
	}
	if !st.public[green.ID] || st.public[address.ID] || st.public[pin.ID] {
		t.Errorf("public = %v, want only the colour", st.public)
	}
	if _, asked := st.public[theirs.ID]; asked {
		t.Error("a fact about someone else was considered for the page")
	}

	if len(s2.reqs) != 1 {
		t.Fatalf("system 2 requests = %d, want 1", len(s2.reqs))
	}
	in := s2.reqs[0].Instructions
	if !strings.Contains(in, "You are Tobi.") || !strings.Contains(in, "27 Oct 2026: Tobi's favourite colour is green") ||
		strings.Contains(in, "Adeola") || strings.Contains(in, "4821") || strings.Contains(in, "Sam") {
		t.Errorf("instructions = %q", in)
	}
	if s2.reqs[0].Format == nil {
		t.Error("the page was not asked for as structured output")
	}
	var p Page
	if err := json.Unmarshal(st.page, &p); err != nil {
		t.Fatal(err)
	}
	if p.Tagline != "Beats and books." || len(p.About) != 1 || p.Now[0] != "Reading Things Fall Apart" {
		t.Errorf("page = %+v", p)
	}
	if len(st.decisions) != 2 || st.decisions[0].SetID != "public_profile@1" || st.decisions[1].Action != "write_profile" ||
		st.decisions[1].PerceptID != (ulid.ULID{}) {
		t.Errorf("decisions = %+v", st.decisions)
	}

	// Nothing new: nothing is asked or rewritten.
	w.refresh(context.Background())
	if len(s1.reqs) != 1 || len(s2.reqs) != 1 {
		t.Errorf("second refresh asked again: system 1 %d, system 2 %d", len(s1.reqs), len(s2.reqs))
	}

	// A new public fact rewrites the page.
	beat := fact(memory.SubjectSelf, "Tobi is making a beat called Lagos Rain")
	st.facts = append(st.facts, beat)
	s1.probs[beat.Text] = 0.9
	w.refresh(context.Background())
	if len(s2.reqs) != 2 || !strings.Contains(s2.reqs[1].Instructions, "Lagos Rain") {
		t.Errorf("new fact did not rewrite the page: %d requests", len(s2.reqs))
	}
}

func TestBadPageKeepsTheLastOne(t *testing.T) {
	st := &fakeStore{public: map[ulid.ULID]bool{}, page: []byte(`{"about":["old"]}`), inputs: "x"}
	w := newWriter(st, &fakeS1{}, &fakeS2{out: `{"tagline":"t","about":[" "],"interests":[],"now":[]}`})
	w.refresh(context.Background())
	if string(st.page) != `{"about":["old"]}` || len(st.decisions) != 0 {
		t.Errorf("page = %s, decisions = %d", st.page, len(st.decisions))
	}
}

func TestNoPublicFactsMeansNoLately(t *testing.T) {
	st := &fakeStore{public: map[ulid.ULID]bool{}}
	newWriter(st, &fakeS1{}, &fakeS2{out: pageJSON}).refresh(context.Background())
	var p Page
	if err := json.Unmarshal(st.page, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.Now) != 0 {
		t.Errorf("now = %v, want empty without public facts", p.Now)
	}
}

func TestHandlerRendersThePage(t *testing.T) {
	st := &fakeStore{page: []byte(`{"tagline":"<b>hi</b>","about":["I make beats."],"interests":["music"],"now":[]}`),
		at: time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)}
	srv := httptest.NewServer(Handler("tobi", st, slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	html := string(body)
	for _, want := range []string{"<h1>tobi</h1>", ">T<", "I make beats.", "music", "Updated 27 October 2026", "&lt;b&gt;hi&lt;/b&gt;", "AI agent"} {
		if !strings.Contains(html, want) {
			t.Errorf("page is missing %q", want)
		}
	}
	if strings.Contains(html, "Lately") {
		t.Error("empty now section was shown")
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") {
		t.Errorf("CSP = %q", csp)
	}

	if resp, _ := http.Get(srv.URL + "/admin"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("/admin = %d, want 404", resp.StatusCode)
	}
}

func TestHandlerBeforeThePageIsWritten(t *testing.T) {
	srv := httptest.NewServer(Handler("Tobi", &fakeStore{}, slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "hasn't written this page yet") {
		t.Errorf("status %d, body %s", resp.StatusCode, body)
	}
}

func TestNextUpdate(t *testing.T) {
	day := func(d, h, m int) time.Time { return time.Date(2026, 10, d, h, m, 0, 0, time.UTC) }
	for _, c := range []struct {
		now  time.Time
		at   time.Duration
		want time.Time
	}{
		{day(27, 15, 0), 0, day(28, 0, 0)},
		{day(27, 0, 0), 0, day(28, 0, 0)}, // exactly at the time: the next day
		{day(27, 3, 0), 6 * time.Hour, day(27, 6, 0)},
		{day(27, 0, 30).In(time.FixedZone("WAT", 3600)), 0, day(28, 0, 0)}, // the local zone does not matter
	} {
		if got := nextUpdate(c.now, c.at); !got.Equal(c.want) {
			t.Errorf("nextUpdate(%v, %v) = %v, want %v", c.now, c.at, got, c.want)
		}
	}
}

func TestUpdateForceRewritesUnchangedPage(t *testing.T) {
	st := &fakeStore{public: map[ulid.ULID]bool{}}
	s2 := &fakeS2{out: pageJSON}
	w := newWriter(st, &fakeS1{}, s2)
	for i, c := range []struct{ force, want bool }{{false, true}, {false, false}, {true, true}} {
		written, err := w.Update(context.Background(), c.force)
		if err != nil || written != c.want {
			t.Errorf("update %d (force %v) = %v, %v; want %v", i, c.force, written, err, c.want)
		}
	}
	if len(s2.reqs) != 2 {
		t.Errorf("system 2 wrote %d times, want 2", len(s2.reqs))
	}
}
