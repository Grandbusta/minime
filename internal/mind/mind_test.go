package mind

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/Grandbusta/minime/internal/decision"
	"github.com/Grandbusta/minime/internal/decision/questions"
	"github.com/Grandbusta/minime/internal/memory"
	"github.com/Grandbusta/minime/internal/perception"
	"github.com/Grandbusta/minime/internal/providers/system1"
	"github.com/Grandbusta/minime/internal/providers/system2"
	"github.com/Grandbusta/minime/internal/render"
	"github.com/Grandbusta/minime/internal/render/midi"
	"github.com/Grandbusta/minime/internal/render/pdf"
	"github.com/Grandbusta/minime/internal/search"
	"github.com/Grandbusta/minime/internal/self"
)

type fakeStore struct {
	seen        map[string]bool
	decisions   []decision.Decision
	exchanges   []memory.Exchange
	facts       []memory.Fact // newest first
	people      map[string]memory.Person
	intentions  []memory.Intention
	projects    map[ulid.ULID]memory.Project
	works       []memory.Work
	spent       float64 // what CostSince returns
	convs       []memory.Conversation
	percepts    []perception.Percept
	asked       map[ulid.ULID]time.Time
	approvals   []memory.Approval
	rules       map[string]string // autonomy rules: class → verdict
	labels      []memory.Label
	lastRead    ulid.ULID // what LastRead returns
	dreams      []memory.Dream
	goals       []memory.Goal
	skills      []memory.Skill
	evidence    []memory.Evidence // oldest first
	lessons     []memory.Lesson
	principles  []memory.Principle
	cases       []memory.JudgmentCase
	calls       []memory.Call
	reviewAsked map[ulid.ULID]time.Time
	plans       []memory.LearningPlan
	resources   []memory.Resource
	public      map[ulid.ULID]string // works set public: "true by owner", ...
}

// keep returns the xs that match, at most limit of them (all, if limit < 0).
func keep[T any](xs []T, limit int, match func(T) bool) []T {
	var out []T
	for _, x := range xs {
		if match(x) && (limit < 0 || len(out) < limit) {
			out = append(out, x)
		}
	}
	return out
}

func find[T any](xs []T, match func(T) bool) (T, bool) {
	if i := slices.IndexFunc(xs, match); i >= 0 {
		return xs[i], true
	}
	var zero T
	return zero, false
}

func reversed[T any](xs []T) []T {
	out := slices.Clone(xs)
	slices.Reverse(out)
	return out
}

// update calls f on each x that matches.
func update[T any](xs []T, match func(T) bool, f func(*T)) {
	for i := range xs {
		if match(xs[i]) {
			f(&xs[i])
		}
	}
}

// upsert replaces the x that matches, or adds it.
func upsert[T any](xs []T, x T, match func(T) bool) []T {
	if i := slices.IndexFunc(xs, match); i >= 0 {
		xs[i] = x
		return xs
	}
	return append(xs, x)
}

func (s *fakeStore) SaveAutonomyRule(_ context.Context, class, verdict, _ string, _ ulid.ULID, _ time.Time) error {
	if s.rules == nil {
		s.rules = map[string]string{}
	}
	s.rules[class] = verdict
	return nil
}

func (s *fakeStore) AutonomyRules(context.Context) (map[string]string, error) { return s.rules, nil }

func (s *fakeStore) DeleteFact(_ context.Context, id ulid.ULID) (bool, error) {
	for n, f := range s.facts {
		if f.ID == id {
			s.facts = append(s.facts[:n], s.facts[n+1:]...)
			return true, nil
		}
	}
	return false, nil
}

func (s *fakeStore) SaveLabel(_ context.Context, l memory.Label) error {
	s.labels = append(s.labels, l)
	return nil
}

func (s *fakeStore) LastRead(context.Context, string, string, string, time.Time) (ulid.ULID, bool, error) {
	return s.lastRead, !s.lastRead.IsZero(), nil
}

func (s *fakeStore) SaveDream(_ context.Context, d memory.Dream) error {
	s.dreams = upsert(s.dreams, d, func(x memory.Dream) bool { return x.ID == d.ID })
	return nil
}

func (s *fakeStore) Dreams(_ context.Context, activeOnly bool) ([]memory.Dream, error) {
	return keep(s.dreams, -1, func(d memory.Dream) bool { return !activeOnly || d.Status == memory.DreamActive }), nil
}

func (s *fakeStore) SaveGoal(_ context.Context, g memory.Goal) error {
	s.goals = upsert(s.goals, g, func(x memory.Goal) bool { return x.ID == g.ID })
	return nil
}

func (s *fakeStore) Goals(_ context.Context, activeOnly bool) ([]memory.Goal, error) {
	return keep(s.goals, -1, func(g memory.Goal) bool { return !activeOnly || g.Status == memory.GoalActive }), nil
}

func (s *fakeStore) AddSkill(_ context.Context, sk memory.Skill) error {
	if !slices.ContainsFunc(s.skills, func(x memory.Skill) bool { return x.Name == sk.Name }) {
		s.skills = append(s.skills, sk)
	}
	return nil
}

func (s *fakeStore) Skills(context.Context) ([]memory.Skill, error) { return s.skills, nil }

func (s *fakeStore) SetSkillLevel(_ context.Context, name string, level float64, at time.Time) error {
	update(s.skills, func(x memory.Skill) bool { return x.Name == name }, func(x *memory.Skill) { x.Level, x.UpdatedAt = level, at })
	return nil
}

func (s *fakeStore) SaveEvidence(_ context.Context, e memory.Evidence) error {
	s.evidence = append(s.evidence, e)
	return nil
}

func (s *fakeStore) RecentEvidence(_ context.Context, skill string, limit int) ([]memory.Evidence, error) {
	return keep(reversed(s.evidence), limit, func(e memory.Evidence) bool { return e.Skill == skill }), nil
}

func (s *fakeStore) SaveLesson(_ context.Context, l memory.Lesson) error {
	s.lessons = append(s.lessons, l)
	return nil
}

func (s *fakeStore) Lessons(_ context.Context, skill string, limit int) ([]memory.Lesson, error) {
	return keep(reversed(s.lessons), limit, func(l memory.Lesson) bool { return l.RetiredAt.IsZero() && (skill == "" || l.Skill == skill) }), nil
}

func (s *fakeStore) RetireLesson(_ context.Context, id ulid.ULID, at time.Time) error {
	update(s.lessons, func(x memory.Lesson) bool { return x.ID == id }, func(x *memory.Lesson) { x.RetiredAt = at })
	return nil
}

func (s *fakeStore) MarkReviewAsked(_ context.Context, work ulid.ULID, at time.Time) error {
	if s.reviewAsked == nil {
		s.reviewAsked = map[ulid.ULID]time.Time{}
	}
	s.reviewAsked[work] = at
	return nil
}

func (s *fakeStore) AskedForReview(_ context.Context, since time.Time) (memory.Work, bool, error) {
	var best memory.Work
	var at time.Time
	for _, w := range s.works {
		if t, ok := s.reviewAsked[w.ID]; ok && !t.Before(since) && t.After(at) {
			best, at = w, t
		}
	}
	return best, !at.IsZero(), nil
}

func (s *fakeStore) SavePlan(_ context.Context, p memory.LearningPlan) error {
	s.plans = append(s.plans, p)
	return nil
}

func (s *fakeStore) Plans(_ context.Context, activeOnly bool) ([]memory.LearningPlan, error) {
	return keep(s.plans, -1, func(p memory.LearningPlan) bool { return !activeOnly || p.Status == memory.PlanActive }), nil
}

func (s *fakeStore) AdvancePlan(_ context.Context, plan ulid.ULID, at time.Time) error {
	for n := range s.plans {
		if p := &s.plans[n]; p.ID == plan {
			p.Stages[p.Stage].DoneAt = at
			if p.Stage++; p.Stage >= len(p.Stages) {
				p.Status = memory.PlanDone
			}
		}
	}
	return nil
}

func (s *fakeStore) EndPlan(_ context.Context, plan ulid.ULID, status string, _ time.Time) error {
	update(s.plans, func(x memory.LearningPlan) bool { return x.ID == plan }, func(x *memory.LearningPlan) { x.Status = status })
	return nil
}

func (s *fakeStore) SaveResource(_ context.Context, r memory.Resource) error {
	s.resources = append(s.resources, r)
	return nil
}

func (s *fakeStore) Resources(_ context.Context, plan ulid.ULID) ([]memory.Resource, error) {
	return keep(s.resources, -1, func(r memory.Resource) bool { return r.PlanID == plan }), nil
}

func (s *fakeStore) FinishResource(_ context.Context, id, work ulid.ULID, title string, at time.Time) error {
	update(s.resources, func(x memory.Resource) bool { return x.ID == id },
		func(x *memory.Resource) { x.Status, x.WorkID, x.Title, x.DoneAt = memory.ResourceDone, work, title, at })
	return nil
}

func (s *fakeStore) RecentFound(context.Context, int) ([]search.Result, error) { return nil, nil }

func (s *fakeStore) AwaitIntention(_ context.Context, id ulid.ULID) error {
	update(s.intentions, func(x memory.Intention) bool { return x.ID == id && x.Status == memory.IntentionPending },
		func(x *memory.Intention) { x.Status = memory.IntentionAwaiting })
	return nil
}

func (s *fakeStore) ResumeIntention(_ context.Context, id ulid.ULID, notBefore time.Time) error {
	for n := range s.intentions {
		if i := &s.intentions[n]; i.ID == id && i.Status == memory.IntentionAwaiting {
			i.Status = memory.IntentionPending
			if i.DueAt.Before(notBefore) {
				i.DueAt = notBefore
			}
		}
	}
	return nil
}

func (s *fakeStore) SaveApproval(_ context.Context, a memory.Approval) error {
	s.approvals = append(s.approvals, a)
	return nil
}

func (s *fakeStore) Approval(_ context.Context, id ulid.ULID) (memory.Approval, bool, error) {
	a, ok := find(s.approvals, func(a memory.Approval) bool { return a.ID == id })
	return a, ok, nil
}

func (s *fakeStore) UnsentApprovals(_ context.Context, limit int) ([]memory.Approval, error) {
	return keep(s.approvals, limit, func(a memory.Approval) bool { return a.Status == memory.ApprovalPending && a.SentAt.IsZero() }), nil
}

func (s *fakeStore) MarkApprovalSent(_ context.Context, id ulid.ULID, message []byte, at time.Time) error {
	update(s.approvals, func(x memory.Approval) bool { return x.ID == id },
		func(x *memory.Approval) { x.SentAt, x.Message = at, message })
	return nil
}

func (s *fakeStore) DecideApproval(_ context.Context, id ulid.ULID, status string, at time.Time) (bool, error) {
	for n := range s.approvals {
		if a := &s.approvals[n]; a.ID == id && a.Status == memory.ApprovalPending {
			a.Status, a.DecidedAt = status, at
			return true, nil
		}
	}
	return false, nil
}

func (s *fakeStore) StaleApprovals(_ context.Context, before time.Time, limit int) ([]memory.Approval, error) {
	return keep(s.approvals, limit, func(a memory.Approval) bool { return a.Status == memory.ApprovalPending && a.AskedAt.Before(before) }), nil
}

func (s *fakeStore) SetWorkPublic(_ context.Context, id ulid.ULID, public bool, by string) (bool, error) {
	if s.public == nil {
		s.public = map[ulid.ULID]string{}
	}
	s.public[id] = fmt.Sprintf("%v by %s", public, by)
	return true, nil
}

func (s *fakeStore) SaveIntention(_ context.Context, i memory.Intention) error {
	s.intentions = append(s.intentions, i)
	return nil
}

func (s *fakeStore) DueIntentions(_ context.Context, now time.Time, limit int) ([]memory.Intention, error) {
	return keep(s.intentions, limit, func(i memory.Intention) bool { return i.Status == memory.IntentionPending && !i.DueAt.After(now) }), nil
}

func (s *fakeStore) IntentionsDue(_ context.Context, from, to time.Time) ([]memory.Intention, error) {
	out := keep(s.intentions, -1, func(i memory.Intention) bool { return !i.DueAt.Before(from) && i.DueAt.Before(to) })
	slices.SortStableFunc(out, func(a, b memory.Intention) int { return a.DueAt.Compare(b.DueAt) })
	return out, nil
}

func (s *fakeStore) FinishIntention(_ context.Context, id ulid.ULID, status, result string, at time.Time) error {
	update(s.intentions, func(x memory.Intention) bool { return x.ID == id },
		func(x *memory.Intention) { x.Status, x.Result, x.DoneAt = status, result, at })
	return nil
}

func (s *fakeStore) PostponeIntention(_ context.Context, id ulid.ULID, due time.Time) error {
	update(s.intentions, func(x memory.Intention) bool { return x.ID == id }, func(x *memory.Intention) { x.DueAt = due })
	return nil
}

func (s *fakeStore) intention(id ulid.ULID) memory.Intention {
	i, _, _ := s.Intention(context.Background(), id)
	return i
}

func (s *fakeStore) SaveProject(_ context.Context, p memory.Project) error {
	s.projects[p.ID] = p
	return nil
}

func (s *fakeStore) Project(_ context.Context, id ulid.ULID) (memory.Project, bool, error) {
	p, ok := s.projects[id]
	return p, ok, nil
}

func (s *fakeStore) ActiveProjects(_ context.Context, limit int) ([]memory.Project, error) {
	out := keep(slices.Collect(maps.Values(s.projects)), limit, func(p memory.Project) bool { return p.Status == memory.ProjectActive })
	slices.SortFunc(out, func(a, b memory.Project) int { return strings.Compare(a.Title, b.Title) })
	return out, nil
}

func (s *fakeStore) SaveWork(_ context.Context, w memory.Work) error {
	s.works = append(s.works, w)
	return nil
}

func (s *fakeStore) WorksBetween(_ context.Context, from, to time.Time) ([]memory.Work, error) {
	return keep(s.works, -1, func(w memory.Work) bool { return !w.CreatedAt.Before(from) && w.CreatedAt.Before(to) }), nil
}

func (s *fakeStore) ProjectWorks(_ context.Context, project ulid.ULID, limit int) ([]memory.Work, error) {
	out := keep(s.works, -1, func(w memory.Work) bool { return w.ProjectID == project })
	return out[max(0, len(out)-limit):], nil
}

func (s *fakeStore) OpenIntentions(_ context.Context, subject, kind string) ([]memory.Intention, error) {
	return keep(s.intentions, -1, func(i memory.Intention) bool {
		return i.Subject == subject && i.Kind == kind && i.Status == memory.IntentionPending
	}), nil
}

func (s *fakeStore) Work(_ context.Context, id ulid.ULID) (memory.Work, bool, error) {
	w, ok := find(s.works, func(w memory.Work) bool { return w.ID == id })
	return w, ok, nil
}

func (s *fakeStore) SetWorkScore(_ context.Context, id ulid.ULID, score json.RawMessage) error {
	for n := range s.works {
		if s.works[n].ID == id {
			s.works[n].Score = score
		}
	}
	return nil
}

func (s *fakeStore) UnscoredWorks(_ context.Context, kinds []string, limit int) ([]memory.Work, error) {
	var out []memory.Work
	for n := len(s.works) - 1; n >= 0 && len(out) < limit; n-- {
		if w := s.works[n]; len(w.Score) == 0 && slices.Contains(kinds, w.Kind) {
			out = append(out, w)
		}
	}
	return out, nil
}

func (s *fakeStore) WorksFor(_ context.Context, person string, limit int) ([]memory.Work, error) {
	var out []memory.Work
	for n := len(s.works) - 1; n >= 0 && len(out) < limit; n-- {
		w := s.works[n]
		if w.Kind == "journal" || w.Kind == "review" {
			continue
		}
		if i, ok, _ := s.Intention(context.Background(), w.IntentionID); ok {
			if i.Subject != memory.SubjectSelf && i.Subject != person {
				continue
			}
			w.MadeFor = i.Subject
		}
		out = append(out, w)
	}
	slices.SortStableFunc(out, func(a, b memory.Work) int { return b.CreatedAt.Compare(a.CreatedAt) })
	return out, nil
}

func (s *fakeStore) SaveWorkEmbedding(_ context.Context, id ulid.ULID, v []float32, model string) error {
	update(s.works, func(x memory.Work) bool { return x.ID == id }, func(x *memory.Work) { x.Embedding, x.EmbeddedWith = v, model })
	return nil
}

func (s *fakeStore) Intention(_ context.Context, id ulid.ULID) (memory.Intention, bool, error) {
	i, ok := find(s.intentions, func(i memory.Intention) bool { return i.ID == id })
	return i, ok, nil
}

func (s *fakeStore) IntentionWork(_ context.Context, id ulid.ULID) (memory.Work, bool, error) {
	w, ok := find(reversed(s.works), func(w memory.Work) bool { return w.IntentionID == id })
	return w, ok, nil
}

func (s *fakeStore) LatestWork(_ context.Context, kind string) (memory.Work, bool, error) {
	w, ok := find(reversed(s.works), func(w memory.Work) bool { return w.Kind == kind })
	return w, ok, nil
}

func (s *fakeStore) CostSince(context.Context, time.Time, []string) (float64, error) {
	return s.spent, nil
}

func (s *fakeStore) Conversations(context.Context) ([]memory.Conversation, error) {
	return s.convs, nil
}

func (s *fakeStore) LastSpokeFirst(_ context.Context, person, kind string) (time.Time, bool, error) {
	var last time.Time
	for _, p := range s.percepts {
		if p.Speaker == person && p.Kind == kind && p.At.After(last) {
			last = p.At
		}
	}
	return last, !last.IsZero(), nil
}

func (s *fakeStore) UnaskedFacts(_ context.Context, person string, since time.Time, limit int) ([]memory.Fact, error) {
	return keep(s.facts, limit, func(f memory.Fact) bool {
		_, asked := s.asked[f.ID]
		return !asked && f.Subject == person && !f.At.Before(since)
	}), nil
}

func (s *fakeStore) MarkFactsAsked(_ context.Context, ids []ulid.ULID, at time.Time) error {
	for _, id := range ids {
		s.asked[id] = at
	}
	return nil
}

func (s *fakeStore) SaveFact(_ context.Context, f memory.Fact) error {
	s.facts = append([]memory.Fact{f}, s.facts...)
	return nil
}

// RecallCandidates mimics the store: only facts about subjects; those sharing
// an area first, then the rest, newest first within each group (s.facts is
// kept newest first).
func (s *fakeStore) RecallCandidates(_ context.Context, subjects, areas []string, limit int) ([]memory.Fact, error) {
	var same, rest []memory.Fact
	for _, f := range s.facts {
		if !slices.Contains(subjects, f.Subject) {
			continue
		}
		if slices.ContainsFunc(f.Areas, func(a string) bool { return slices.Contains(areas, a) }) {
			same = append(same, f)
		} else {
			rest = append(rest, f)
		}
	}
	all := append(same, rest...)
	return all[:min(limit, len(all))], nil
}

func (s *fakeStore) Person(_ context.Context, key string) (memory.Person, bool, error) {
	p, ok := s.people[key]
	return p, ok, nil
}

func (s *fakeStore) KnownPeople(_ context.Context, limit int) ([]memory.Person, error) {
	out := keep(slices.Collect(maps.Values(s.people)), limit, func(p memory.Person) bool { return p.Relation != "" })
	slices.SortFunc(out, func(a, b memory.Person) int { return strings.Compare(a.Key, b.Key) })
	return out, nil
}

func (s *fakeStore) SavePerson(_ context.Context, p memory.Person) error {
	s.people[p.Key] = p
	return nil
}

func (s *fakeStore) AddPersonIfNew(_ context.Context, p memory.Person) (bool, error) {
	if _, ok := s.people[p.Key]; ok {
		return false, nil
	}
	s.people[p.Key] = p
	return true, nil
}

func (s *fakeStore) UnlabelledFacts(context.Context, int) ([]memory.Fact, error) { return nil, nil }

func (s *fakeStore) SetFactAreas(context.Context, ulid.ULID, []string) error { return nil }

func (s *fakeStore) RecentExchanges(context.Context, string, string, time.Time, ulid.ULID, int) ([]memory.Exchange, error) {
	return s.exchanges, nil
}

func (s *fakeStore) SavePercept(_ context.Context, p perception.Percept) (bool, error) {
	if s.seen[p.ExternalID] {
		return false, nil
	}
	s.seen[p.ExternalID] = true
	s.percepts = append(s.percepts, p)
	return true, nil
}

func (s *fakeStore) SaveDecision(_ context.Context, d decision.Decision) error {
	s.decisions = append(s.decisions, d)
	return nil
}

type fakeReplier struct {
	sent []string
	err  error
}

func (r *fakeReplier) Reply(_ context.Context, _ perception.Percept, text string) error {
	r.sent = append(r.sent, text)
	return r.err
}

// fakeSystem1 may be called from two goroutines at once.
type fakeSystem1 struct {
	choice      string
	confidence  float64
	refused     bool
	err         error
	relevant    map[string]bool // fact text → relevant
	recallErr   error
	mu          sync.Mutex
	recallCalls []system1.Request
	scores      map[string]float64 // work title → level index (0-9), for skill scoring
	scoreCalls  []system1.Request
	stageMet    map[string]float64 // work title → how sure it meets the stage
	stageCalls  []system1.Request
}

func (f *fakeSystem1) Decide(_ context.Context, req system1.Request) (system1.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if state, ok := req.State.(map[string]any); ok && state["skill"] != nil {
		f.scoreCalls = append(f.scoreCalls, req)
		title := state["work"].(map[string]string)["title"]
		return system1.Response{Provider: "fake1/1", CostUSD: 0.00002, Answers: map[string]decision.Answer{
			"level": {Type: decision.QuestionScore, Score: f.scores[title]},
		}}, nil
	}
	if state, ok := req.State.(map[string]any); ok && state["stage"] != nil {
		f.stageCalls = append(f.stageCalls, req)
		title := state["work"].(map[string]string)["title"]
		return system1.Response{Provider: "fake1/1", CostUSD: 0.00002, Answers: map[string]decision.Answer{
			"met": {Type: decision.QuestionPredicate, Probability: f.stageMet[title]},
		}}, nil
	}
	if state, ok := req.State.(map[string]any); ok && state["relation"] != nil {
		relation := state["relation"].(string)
		circle := "other"
		if strings.Contains(relation, "father") || strings.Contains(relation, "cousin") {
			circle = "family"
		}
		return system1.Response{Provider: "fake1/1", CostUSD: 0.00003, Answers: map[string]decision.Answer{
			"circle": {Type: decision.QuestionChoice, Choice: circle, Confidence: 0.95},
		}}, nil
	}
	if state, ok := req.State.(map[string]any); ok && state["text"] != nil {
		return system1.Response{Provider: "fake1/1", CostUSD: 0.00004, Answers: map[string]decision.Answer{
			"area":     {Type: decision.QuestionChoice, Probabilities: map[string]float64{"other": 1}},
			"personal": {Type: decision.QuestionPredicate, Probability: 1},
			"broad":    {Type: decision.QuestionPredicate},
		}}, nil
	}
	if state, ok := req.State.(map[string]any); ok && state["facts"] != nil {
		f.recallCalls = append(f.recallCalls, req)
		if f.recallErr != nil {
			return system1.Response{}, f.recallErr
		}
		texts := state["facts"].([]string)
		answers := map[string]decision.Answer{}
		for i, q := range req.Questions {
			p := 0.0
			if f.relevant[texts[i]] {
				p = 0.9
			}
			answers[q.Name] = decision.Answer{Type: decision.QuestionPredicate, Probability: p}
		}
		return system1.Response{Provider: "fake1/1", CostUSD: 0.00003, Answers: answers}, nil
	}
	if req.Questions[0].Name == "answers_previous" {
		return system1.Response{Provider: "fake1/1", CostUSD: 0.00002, Answers: map[string]decision.Answer{
			"answers_previous": {Type: decision.QuestionPredicate},
		}}, nil
	}
	if f.err != nil {
		return system1.Response{}, f.err
	}
	return system1.Response{
		Provider: "fake1/1",
		CostUSD:  0.00001,
		Answers: map[string]decision.Answer{
			"intent": {Type: decision.QuestionChoice, Choice: f.choice, Confidence: f.confidence, Refused: f.refused},
		},
	}, nil
}

type fakeSystem2 struct {
	err        error
	remember   []system2.Memory
	whoTheyAre string
	reminders  []system2.Reminder
	promises   []system2.Promise
	lessons    []system2.LessonLearned
	feedback   []system2.Feedback
	goals      []system2.GoalTaken
	send       system2.Send
	formats    map[string]string // reply text by Format.Name, for structured requests
	requests   []system2.Request
}

func (f *fakeSystem2) Reason(_ context.Context, req system2.Request) (system2.Response, error) {
	f.requests = append(f.requests, req)
	if f.err != nil {
		return system2.Response{}, f.err
	}
	if req.Format != nil {
		return system2.Response{Provider: "fake2/1", Text: f.formats[req.Format.Name], CostUSD: 0.002}, nil
	}
	return system2.Response{Provider: "fake2/1", Text: "a thoughtful reply", Remember: f.remember, WhoTheyAre: f.whoTheyAre,
		Reminders: f.reminders, Promises: f.promises, Lessons: f.lessons, Feedback: f.feedback, Goals: f.goals, Send: f.send, CostUSD: 0.001}, nil
}

var testSet = questions.Set{
	ID: "incoming_message", Version: 1, Primary: "intent",
	Questions: []system1.Question{{Name: "intent", Type: decision.QuestionChoice, Instructions: "?",
		Choices: []system1.Choice{{Value: "remember"}, {Value: "question"}, {Value: "other"}}}},
	Thresholds: questions.Thresholds{Act: 0.85, Ask: 0.5},
}

var testFactRelevance = questions.Set{
	ID: "fact_relevance", Version: 1, Primary: "relevant",
	Questions:  []system1.Question{{Name: "relevant", Type: decision.QuestionPredicate, Instructions: "Would {fact} help with `message`?"}},
	Thresholds: questions.Thresholds{Act: 0.5, Ask: 0.5},
}

var testLifeArea = questions.Set{
	ID: "life_area", Version: 1,
	Questions: []system1.Question{
		{Name: "area", Type: decision.QuestionChoice, Instructions: "?",
			Choices: []system1.Choice{{Value: "identity"}, {Value: "home"}, {Value: "family"}, {Value: "religion"}, {Value: "other"}}},
		{Name: "personal", Type: decision.QuestionPredicate, Instructions: "?"},
		{Name: "broad", Type: decision.QuestionPredicate, Instructions: "?"},
	},
	Thresholds: questions.Thresholds{Act: 0.5, Ask: 0.5, Label: 0.15},
}

var testPersonCircle = questions.Set{
	ID: "person_circle", Version: 1, Primary: "circle",
	Questions: []system1.Question{{Name: "circle", Type: decision.QuestionChoice,
		Instructions: "Which circle of {agent}'s life?",
		Choices:      []system1.Choice{{Value: "family", Description: "{agent}'s relatives"}, {Value: "other"}}}},
	Thresholds: questions.Thresholds{Act: 0.5, Ask: 0.5},
}

var testOpenThreads = questions.Set{
	ID: "open_threads", Version: 1, Primary: "open",
	Questions:  []system1.Question{{Name: "open", Type: decision.QuestionPredicate, Instructions: "Is {fact} going on for {speaker}?"}},
	Thresholds: questions.Thresholds{Act: 0.5, Ask: 0.5},
}

var testFollowUp = questions.Set{
	ID: "follow_up", Version: 1, Primary: "answers_previous",
	Questions:  []system1.Question{{Name: "answers_previous", Type: decision.QuestionPredicate, Instructions: "?"}},
	Thresholds: questions.Thresholds{Act: 0.5, Ask: 0.5},
}

var testLife = self.Life{Name: "Tobi", Story: []string{"Tobi is 29 years old.", "Tobi is a Catholic."}}

func newMind(r Replier, s1 system1.DecisionModel, s2 system2.Reasoner) (*Mind, *fakeStore) {
	s := &fakeStore{seen: map[string]bool{}, people: map[string]memory.Person{}, asked: map[ulid.ULID]time.Time{},
		projects: map[ulid.ULID]memory.Project{}}
	return New(Config{
		Life:    testLife,
		Formats: render.NewSet(pdf.Format{}, midi.Format{}),
		Store:   s, Replier: r, System1: s1, System2: s2, Incoming: testSet, FollowUp: testFollowUp, LifeArea: testLifeArea,
		FactRelevance: testFactRelevance, RecallCandidates: 30, PersonCircle: testPersonCircle, OpenThreads: testOpenThreads,
		ReplyPrompt: Prompt{Ref: "reply@test", Text: "You are {name}, talking with {speaker}. {who_they_are}\n{life_story}"},
		Working:     WorkingMemory{Turns: 8, Window: 2 * time.Hour},
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	}), s
}

var now = time.Date(2026, 10, 27, 13, 16, 0, 0, time.UTC)

const sam = "test:sam"

func percept(externalID, text string) perception.Percept {
	return perception.Percept{ID: ulid.Make(), Source: "test", ExternalID: externalID, Thread: "chat-1",
		Speaker: sam, SpeakerName: "Sam", Kind: "message", Text: text, At: now}
}

func TestRouting(t *testing.T) {
	cases := []struct {
		name        string
		s1          *fakeSystem1
		s2          *fakeSystem2
		wantActions []string // root first, then the System 2 child if any
		wantReply   string
	}{
		{"confident share is answered in his own words", &fakeSystem1{choice: "share", confidence: 0.93}, &fakeSystem2{},
			[]string{"share", "reply"}, "a thoughtful reply"},
		{"middling goes to System 2", &fakeSystem1{choice: "remember", confidence: 0.6}, &fakeSystem2{},
			[]string{"ask", "reply"}, "a thoughtful reply"},
		{"unsure escalates to System 2", &fakeSystem1{choice: "remember", confidence: 0.2}, &fakeSystem2{},
			[]string{"escalate", "reply"}, "a thoughtful reply"},
		{"refusal escalates to System 2", &fakeSystem1{refused: true}, &fakeSystem2{},
			[]string{"escalate", "reply"}, "a thoughtful reply"},
		{"System 1 error is recorded", &fakeSystem1{err: errors.New("boom")}, &fakeSystem2{},
			[]string{"error"}, "couldn't think"},
		{"System 2 error is recorded", &fakeSystem1{choice: "question", confidence: 0.9}, &fakeSystem2{err: errors.New("boom")},
			[]string{"question", "error"}, "couldn't think"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &fakeReplier{}
			m, s := newMind(r, tc.s1, tc.s2)
			p := percept("1", "what is the capital of France?")

			m.handle(context.Background(), p)

			if len(r.sent) != 1 || !strings.Contains(r.sent[0], tc.wantReply) {
				t.Fatalf("replies = %q, want one containing %q", r.sent, tc.wantReply)
			}
			last := s.decisions[len(s.decisions)-1]
			if last.Reply != r.sent[0] {
				t.Errorf("acting decision's reply = %q, want what was sent, %q", last.Reply, r.sent[0])
			}
			if len(s.decisions) != len(tc.wantActions) {
				t.Fatalf("decisions = %d, want %d", len(s.decisions), len(tc.wantActions))
			}
			for i, want := range tc.wantActions {
				if s.decisions[i].Action != want {
					t.Errorf("decision %d action = %q, want %q", i, s.decisions[i].Action, want)
				}
			}
			root := s.decisions[0]
			if root.Tier != decision.TierSystem1 || root.SetID != "incoming_message@1,life_area@1" || root.PerceptID != p.ID || root.ParentID != nil {
				t.Errorf("root = %+v", root)
			}
			if len(s.decisions) == 2 {
				child := s.decisions[1]
				if child.ParentID == nil || *child.ParentID != root.ID || child.Tier != decision.TierSystem2 ||
					child.SetID != "reply@test" || child.PerceptID != p.ID {
					t.Errorf("child = %+v", child)
				}
			}
		})
	}
}

func TestPeople(t *testing.T) {
	t.Run("known by account, not by name", func(t *testing.T) {
		s2 := &fakeSystem2{}
		m, s := newMind(&fakeReplier{}, &fakeSystem1{choice: "question", confidence: 0.9}, s2)
		m.Life.Family = []self.Relative{{Name: "Sam", Relation: "father", Account: sam}}
		m.bornKnowing(context.Background())

		m.handle(context.Background(), percept("1", "Who am I to you?"))
		impostor := percept("2", "Who am I to you?")
		impostor.Speaker = "test:another-sam" // same name, different account
		m.handle(context.Background(), impostor)

		if got := s2.requests[0].Instructions; !strings.Contains(got, "This is certain: Sam is Tobi's father.") {
			t.Errorf("father not known: %q", got)
		}
		if got := s2.requests[1].Instructions; strings.Contains(got, "This is certain") {
			t.Errorf("a shared name was taken for the father: %q", got)
		}
		if met := s.people["test:another-sam"]; met.Source != memory.KnownByMeeting || met.Circle != memory.CircleStranger {
			t.Errorf("new person not remembered as a stranger met: %+v", met)
		}
	})

	t.Run("a claim never overwrites what it was born knowing", func(t *testing.T) {
		s2 := &fakeSystem2{whoTheyAre: "Sam is Tobi's cousin."}
		m, s := newMind(&fakeReplier{}, &fakeSystem1{choice: "chat", confidence: 0.9}, s2)
		m.Life.Family = []self.Relative{{Name: "Sam", Relation: "father", Account: sam}}
		m.bornKnowing(context.Background())

		m.handle(context.Background(), percept("1", "Actually I'm your cousin"))

		if s.people[sam].Relation != "Sam is Tobi's father." {
			t.Errorf("born knowledge overwritten: %+v", s.people[sam])
		}
	})
}

func TestSystem2SavesFactsAboutBothSides(t *testing.T) {
	s2 := &fakeSystem2{remember: []system2.Memory{
		{About: system2.AboutSpeaker, Text: "Sam's passport expires in 2027"},
		{About: system2.AboutSelf, Text: "Tobi's favourite colour is forest green"},
	}}
	m, s := newMind(&fakeReplier{}, &fakeSystem1{choice: "remember", confidence: 0.22}, s2)
	p := percept("1", "It is 2027. And what's your favourite colour?")

	m.handle(context.Background(), p)

	if !s2.requests[0].CollectFacts {
		t.Error("System 2 was not asked for facts")
	}
	subjects := map[string]string{}
	for _, f := range s.facts {
		subjects[f.Text] = f.Subject
		if f.PerceptID != p.ID || !f.At.Equal(p.At) || f.ID.IsZero() {
			t.Errorf("fact = %+v", f)
		}
	}
	if subjects["Sam's passport expires in 2027"] != sam || subjects["Tobi's favourite colour is forest green"] != memory.SubjectSelf {
		t.Errorf("subjects = %v", subjects)
	}
}

func TestRecallPassesRelevantFactsToSystem2(t *testing.T) {
	s1 := &fakeSystem1{choice: "question", confidence: 0.93, relevant: map[string]bool{"Sam's passport expires in March 2027": true}}
	s2 := &fakeSystem2{}
	m, s := newMind(&fakeReplier{}, s1, s2)
	day := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	s.facts = []memory.Fact{ // newest first
		{ID: ulid.Make(), Subject: sam, Text: "My car is a blue Golf", At: day.Add(48 * time.Hour)},
		{ID: ulid.Make(), Subject: sam, Text: "My passport expires in March 2027", At: day},
	}

	m.handle(context.Background(), percept("1", "When does my passport expire?"))

	if len(s1.recallCalls) != 1 {
		t.Fatalf("recall checks = %d", len(s1.recallCalls))
	}
	note := s2.requests[0].Note
	if !strings.Contains(note, "Sam's passport expires in March 2027") || strings.Contains(note, "Golf") {
		t.Errorf("note = %q", note)
	}
	if len(s.decisions) != 3 || s.decisions[1].Action != "recall" || *s.decisions[1].ParentID != s.decisions[0].ID {
		t.Errorf("decisions = %+v", s.decisions)
	}
}

func TestOwnFactsAndTheirsAreKeptApart(t *testing.T) {
	mine := memory.Fact{ID: ulid.Make(), Subject: memory.SubjectSelf, Text: "Tobi's favourite food is jollof rice", At: now.Add(-time.Hour)}
	theirs := memory.Fact{ID: ulid.Make(), Subject: sam, Text: "I go to Grace Chapel", At: now.Add(-2 * time.Hour)}
	stranger := memory.Fact{ID: ulid.Make(), Subject: "test:someone-else", Text: "I go to St. Mary's", At: now}
	s1 := &fakeSystem1{choice: "question", confidence: 0.93, relevant: map[string]bool{
		"Tobi's favourite food is jollof rice": true, "Sam go to Grace Chapel": true, "Sam go to St. Mary's": true,
	}}
	s2 := &fakeSystem2{}
	m, s := newMind(&fakeReplier{}, s1, s2)
	s.facts = []memory.Fact{stranger, mine, theirs}

	m.handle(context.Background(), percept("1", "Tell me something"))

	note := s2.requests[0].Note
	wantMine := "About yourself:\n- [f1] 27 Oct 2026: Tobi's favourite food is jollof rice"
	wantTheirs := "About Sam:\n- [f2] 27 Oct 2026: Sam go to Grace Chapel"
	if !strings.Contains(note, wantMine) || !strings.Contains(note, wantTheirs) || strings.Contains(note, "St. Mary") {
		t.Errorf("note = %q", note)
	}
}

func TestFailedRecallStillReplies(t *testing.T) {
	s1 := &fakeSystem1{choice: "question", confidence: 0.93, recallErr: errors.New("boom")}
	r := &fakeReplier{}
	m, s := newMind(r, s1, &fakeSystem2{})
	s.facts = []memory.Fact{{ID: ulid.Make(), Subject: sam, Text: "x", At: now}}

	m.handle(context.Background(), percept("1", "hi?"))

	if len(r.sent) != 1 || r.sent[0] != "a thoughtful reply" {
		t.Fatalf("sent %q", r.sent)
	}
	if d := s.decisions[1]; d.Action != "error" || d.Outcome.Status != decision.OutcomeFailed {
		t.Errorf("recall decision = %+v", d)
	}
}

func TestDuplicatePerceptIsSkipped(t *testing.T) {
	r := &fakeReplier{}
	m, s := newMind(r, &fakeSystem1{choice: "small_talk", confidence: 0.9}, &fakeSystem2{})

	m.handle(context.Background(), percept("1", "hello"))
	afterFirst := len(s.decisions)
	m.handle(context.Background(), percept("1", "hello"))

	if len(r.sent) != 1 || len(s.decisions) != afterFirst {
		t.Fatalf("the second copy sent %d replies and added %d decisions, want nothing", len(r.sent)-1, len(s.decisions)-afterFirst)
	}
}

func (s *fakeStore) SeedJudgment(_ context.Context, cases []memory.JudgmentCase, ps []memory.Principle) (int, int, error) {
	nc, np := 0, 0
	for _, c := range cases {
		if !slices.ContainsFunc(s.cases, func(x memory.JudgmentCase) bool { return x.Key == c.Key }) {
			s.cases, nc = append(s.cases, c), nc+1
		}
	}
	for _, p := range ps {
		if !slices.ContainsFunc(s.principles, func(x memory.Principle) bool { return x.Seed == p.Seed }) {
			s.principles, np = append(s.principles, p), np+1
		}
	}
	return nc, np, nil
}

func (s *fakeStore) Principles(_ context.Context, status string) ([]memory.Principle, error) {
	return keep(s.principles, -1, func(p memory.Principle) bool { return status == "" || p.Status == status }), nil
}

func (s *fakeStore) JudgmentCases(_ context.Context, status string, types []string) ([]memory.JudgmentCase, error) {
	return keep(s.cases, -1, func(c memory.JudgmentCase) bool {
		return (status == "" || c.Status == status) && (len(types) == 0 || slices.Contains(types, c.Type))
	}), nil
}

func (s *fakeStore) SavePrinciple(_ context.Context, p memory.Principle) error {
	s.principles = append(s.principles, p)
	return nil
}

func (s *fakeStore) SaveCall(_ context.Context, c memory.Call) error {
	s.calls = append(s.calls, c)
	return nil
}

func (s *fakeStore) callAt(id ulid.ULID) *memory.Call {
	for i := range s.calls {
		if s.calls[i].ID == id {
			return &s.calls[i]
		}
	}
	return nil
}

func (s *fakeStore) Call(_ context.Context, id ulid.ULID) (memory.Call, bool, error) {
	if c := s.callAt(id); c != nil {
		return *c, true, nil
	}
	return memory.Call{}, false, nil
}

func (s *fakeStore) RecentCalls(_ context.Context, status string, limit int) ([]memory.Call, error) {
	return keep(reversed(s.calls), limit, func(c memory.Call) bool { return status == "" || c.Status == status }), nil
}

func (s *fakeStore) CallsAskedSince(_ context.Context, mode string, since time.Time) (int, error) {
	return len(keep(s.calls, -1, func(c memory.Call) bool { return c.Mode == mode && !c.AskedAt.IsZero() && !c.AskedAt.Before(since) })), nil
}

func (s *fakeStore) MarkCallAsked(_ context.Context, id ulid.ULID, where json.RawMessage, at time.Time) error {
	if c := s.callAt(id); c != nil && c.Status == memory.CallLogged {
		c.Status, c.Message, c.AskedAt = memory.CallAsked, where, at
	}
	return nil
}

func (s *fakeStore) StaleCalls(_ context.Context, before time.Time, limit int) ([]memory.Call, error) {
	return keep(s.calls, limit, func(c memory.Call) bool { return c.Status == memory.CallAsked && c.AskedAt.Before(before) }), nil
}

func (s *fakeStore) ExpireCall(_ context.Context, id ulid.ULID) (bool, error) {
	if c := s.callAt(id); c != nil && c.Status == memory.CallAsked {
		c.Status, c.Waiting = memory.CallExpired, ""
		return true, nil
	}
	return false, nil
}

func (s *fakeStore) WaitForOwner(_ context.Context, id ulid.ULID, waiting string, at time.Time) error {
	if c := s.callAt(id); c != nil {
		c.Waiting, c.WaitingAt = waiting, at
	}
	return nil
}

func (s *fakeStore) WaitingCall(_ context.Context, since time.Time) (memory.Call, bool, error) {
	c, ok := find(reversed(s.calls), func(c memory.Call) bool { return c.Waiting != "" && !c.WaitingAt.Before(since) })
	return c, ok, nil
}

func (s *fakeStore) AnswerCall(_ context.Context, c memory.Call, asCase bool) (bool, error) {
	old := s.callAt(c.ID)
	if old == nil || old.Status == memory.CallAnswered {
		return false, nil
	}
	agreed := c.OwnerPick == c.Pick
	c.Status, c.Agreed, c.WaitingAt = memory.CallAnswered, &agreed, c.AnsweredAt
	if asCase {
		choice := c.OwnerChoice
		if c.OwnerPick >= 0 {
			choice = c.Options[c.OwnerPick]
		}
		c.CaseID = ulid.Make()
		s.cases = append(s.cases, memory.JudgmentCase{ID: c.CaseID, Key: c.CaseID.String(), Type: memory.CallCaseType(c.Type),
			Situation: c.Situation, Options: c.Options, Choice: choice, Reason: c.OwnerReason, Status: memory.JudgmentApproved, Source: c.AnsweredBy})
	}
	*old = c
	return true, nil
}

func (s *fakeStore) GiveCallReason(_ context.Context, id ulid.ULID, reason string, _ time.Time) error {
	c := s.callAt(id)
	c.OwnerReason, c.Waiting = reason, ""
	for i := range s.cases {
		if s.cases[i].ID == c.CaseID {
			s.cases[i].Reason = reason
		}
	}
	return nil
}

func (s *fakeStore) CallsToLookBack(context.Context, []string, time.Time, time.Time, int) ([]memory.Call, error) {
	return nil, nil
}

func (s *fakeStore) SetOutcome(context.Context, ulid.ULID, string, string, string, string, time.Time) (bool, error) {
	return false, nil
}

func (s *fakeStore) SaveCaseEmbedding(context.Context, ulid.ULID, []float32, string) error {
	return nil
}

func (s *fakeStore) SaveTaste(context.Context, memory.Taste) error { return nil }

func (s *fakeStore) Tastes(context.Context, int) ([]memory.Taste, error) { return nil, nil }
