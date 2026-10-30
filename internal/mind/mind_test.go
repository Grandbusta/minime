package mind

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"minime/internal/decision"
	"minime/internal/decision/questions"
	"minime/internal/memory"
	"minime/internal/perception"
	"minime/internal/providers/system1"
	"minime/internal/providers/system2"
	"minime/internal/search"
	"minime/internal/self"
)

type fakeStore struct {
	seen           map[string]bool
	decisions      []decision.Decision
	exchanges      []memory.Exchange // returned by RecentExchanges
	recallErr      error
	recalls        []recallArgs
	facts          []memory.Fact // newest first
	factErr        error
	recallAreas    [][]string // areas each RecallCandidates call was given
	recallSubjects [][]string // and subjects
	people         map[string]memory.Person
	intentions     []memory.Intention
	projects       map[ulid.ULID]memory.Project
	works          []memory.Work
	spent          float64 // what CostSince returns
	convs          []memory.Conversation
	percepts       []perception.Percept // saved, in order
	asked          map[ulid.ULID]time.Time
	approvals      []memory.Approval
	rules          map[string]string // autonomy rules: class → verdict
	labels         []memory.Label
	lastRead       ulid.ULID // what LastRead returns
	dreams         []memory.Dream
	goals          []memory.Goal
	skills         []memory.Skill
	evidence       []memory.Evidence // oldest first
	lessons        []memory.Lesson
	principles     []memory.Principle
	cases          []memory.JudgmentCase
	calls          []memory.Call
	tastes         []memory.Taste
	reviewAsked    map[ulid.ULID]time.Time
	plans          []memory.LearningPlan
	resources      []memory.Resource
	found          []search.Result
	public         map[ulid.ULID]string // works set public: "true by owner", ...
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
	for n := range s.dreams {
		if s.dreams[n].ID == d.ID {
			s.dreams[n] = d
			return nil
		}
	}
	s.dreams = append(s.dreams, d)
	return nil
}

func (s *fakeStore) Dreams(_ context.Context, activeOnly bool) ([]memory.Dream, error) {
	var out []memory.Dream
	for _, d := range s.dreams {
		if !activeOnly || d.Status == memory.DreamActive {
			out = append(out, d)
		}
	}
	return out, nil
}

func (s *fakeStore) SaveGoal(_ context.Context, g memory.Goal) error {
	for n := range s.goals {
		if s.goals[n].ID == g.ID {
			s.goals[n] = g
			return nil
		}
	}
	s.goals = append(s.goals, g)
	return nil
}

func (s *fakeStore) Goals(_ context.Context, activeOnly bool) ([]memory.Goal, error) {
	var out []memory.Goal
	for _, g := range s.goals {
		if !activeOnly || g.Status == memory.GoalActive {
			out = append(out, g)
		}
	}
	return out, nil
}

func (s *fakeStore) AddSkill(_ context.Context, sk memory.Skill) error {
	for _, have := range s.skills {
		if have.Name == sk.Name {
			return nil
		}
	}
	s.skills = append(s.skills, sk)
	return nil
}

func (s *fakeStore) Skills(context.Context) ([]memory.Skill, error) { return s.skills, nil }

func (s *fakeStore) SetSkillLevel(_ context.Context, name string, level float64, at time.Time) error {
	for n := range s.skills {
		if s.skills[n].Name == name {
			s.skills[n].Level, s.skills[n].UpdatedAt = level, at
		}
	}
	return nil
}

func (s *fakeStore) SaveEvidence(_ context.Context, e memory.Evidence) error {
	s.evidence = append(s.evidence, e)
	return nil
}

func (s *fakeStore) RecentEvidence(_ context.Context, skill string, limit int) ([]memory.Evidence, error) {
	var out []memory.Evidence
	for n := len(s.evidence) - 1; n >= 0 && len(out) < limit; n-- {
		if s.evidence[n].Skill == skill {
			out = append(out, s.evidence[n])
		}
	}
	return out, nil
}

func (s *fakeStore) SaveLesson(_ context.Context, l memory.Lesson) error {
	s.lessons = append(s.lessons, l)
	return nil
}

func (s *fakeStore) Lessons(_ context.Context, skill string, limit int) ([]memory.Lesson, error) {
	var out []memory.Lesson
	for n := len(s.lessons) - 1; n >= 0 && len(out) < limit; n-- {
		if l := s.lessons[n]; l.RetiredAt.IsZero() && (skill == "" || l.Skill == skill) {
			out = append(out, l)
		}
	}
	return out, nil
}

func (s *fakeStore) RetireLesson(_ context.Context, id ulid.ULID, at time.Time) error {
	for n := range s.lessons {
		if s.lessons[n].ID == id {
			s.lessons[n].RetiredAt = at
		}
	}
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
	var out []memory.LearningPlan
	for _, p := range s.plans {
		if !activeOnly || p.Status == memory.PlanActive {
			out = append(out, p)
		}
	}
	return out, nil
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
	for n := range s.plans {
		if s.plans[n].ID == plan {
			s.plans[n].Status = status
		}
	}
	return nil
}

func (s *fakeStore) SaveResource(_ context.Context, r memory.Resource) error {
	s.resources = append(s.resources, r)
	return nil
}

func (s *fakeStore) Resources(_ context.Context, plan ulid.ULID) ([]memory.Resource, error) {
	var out []memory.Resource
	for _, r := range s.resources {
		if r.PlanID == plan {
			out = append(out, r)
		}
	}
	return out, nil
}

func (s *fakeStore) FinishResource(_ context.Context, id, work ulid.ULID, title string, at time.Time) error {
	for n := range s.resources {
		if r := &s.resources[n]; r.ID == id {
			r.Status, r.WorkID, r.Title, r.DoneAt = memory.ResourceDone, work, title, at
		}
	}
	return nil
}

func (s *fakeStore) RecentFound(context.Context, int) ([]search.Result, error) { return s.found, nil }

func (s *fakeStore) AwaitIntention(_ context.Context, id ulid.ULID) error {
	for n := range s.intentions {
		if s.intentions[n].ID == id && s.intentions[n].Status == memory.IntentionPending {
			s.intentions[n].Status = memory.IntentionAwaiting
		}
	}
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
	for _, a := range s.approvals {
		if a.ID == id {
			return a, true, nil
		}
	}
	return memory.Approval{}, false, nil
}

func (s *fakeStore) UnsentApprovals(_ context.Context, limit int) ([]memory.Approval, error) {
	var out []memory.Approval
	for _, a := range s.approvals {
		if a.Status == memory.ApprovalPending && a.SentAt.IsZero() && len(out) < limit {
			out = append(out, a)
		}
	}
	return out, nil
}

func (s *fakeStore) MarkApprovalSent(_ context.Context, id ulid.ULID, message []byte, at time.Time) error {
	for n := range s.approvals {
		if s.approvals[n].ID == id {
			s.approvals[n].SentAt, s.approvals[n].Message = at, message
		}
	}
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
	var out []memory.Approval
	for _, a := range s.approvals {
		if a.Status == memory.ApprovalPending && a.AskedAt.Before(before) && len(out) < limit {
			out = append(out, a)
		}
	}
	return out, nil
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
	var out []memory.Intention
	for _, i := range s.intentions {
		if i.Status == memory.IntentionPending && !i.DueAt.After(now) && len(out) < limit {
			out = append(out, i)
		}
	}
	return out, nil
}

func (s *fakeStore) IntentionsDue(_ context.Context, from, to time.Time) ([]memory.Intention, error) {
	var out []memory.Intention
	for _, i := range s.intentions {
		if !i.DueAt.Before(from) && i.DueAt.Before(to) {
			out = append(out, i)
		}
	}
	slices.SortStableFunc(out, func(a, b memory.Intention) int { return a.DueAt.Compare(b.DueAt) })
	return out, nil
}

func (s *fakeStore) FinishIntention(_ context.Context, id ulid.ULID, status, result string, at time.Time) error {
	for n := range s.intentions {
		if s.intentions[n].ID == id {
			s.intentions[n].Status, s.intentions[n].Result, s.intentions[n].DoneAt = status, result, at
		}
	}
	return nil
}

func (s *fakeStore) PostponeIntention(_ context.Context, id ulid.ULID, due time.Time) error {
	for n := range s.intentions {
		if s.intentions[n].ID == id {
			s.intentions[n].DueAt = due
		}
	}
	return nil
}

func (s *fakeStore) intention(id ulid.ULID) memory.Intention {
	for _, i := range s.intentions {
		if i.ID == id {
			return i
		}
	}
	return memory.Intention{}
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
	var out []memory.Project
	for _, p := range s.projects {
		if p.Status == memory.ProjectActive && len(out) < limit {
			out = append(out, p)
		}
	}
	slices.SortFunc(out, func(a, b memory.Project) int { return strings.Compare(a.Title, b.Title) })
	return out, nil
}

func (s *fakeStore) SaveWork(_ context.Context, w memory.Work) error {
	s.works = append(s.works, w)
	return nil
}

func (s *fakeStore) WorksBetween(_ context.Context, from, to time.Time) ([]memory.Work, error) {
	var out []memory.Work
	for _, w := range s.works {
		if !w.CreatedAt.Before(from) && w.CreatedAt.Before(to) {
			out = append(out, w)
		}
	}
	return out, nil
}

func (s *fakeStore) ProjectWorks(_ context.Context, project ulid.ULID, limit int) ([]memory.Work, error) {
	var out []memory.Work
	for _, w := range s.works {
		if w.ProjectID == project {
			out = append(out, w)
		}
	}
	return out[max(0, len(out)-limit):], nil
}

func (s *fakeStore) OpenIntentions(_ context.Context, subject, kind string) ([]memory.Intention, error) {
	var out []memory.Intention
	for _, i := range s.intentions {
		if i.Subject == subject && i.Kind == kind && i.Status == memory.IntentionPending {
			out = append(out, i)
		}
	}
	return out, nil
}

func (s *fakeStore) IntentionWork(_ context.Context, id ulid.ULID) (memory.Work, bool, error) {
	for n := len(s.works) - 1; n >= 0; n-- {
		if s.works[n].IntentionID == id {
			return s.works[n], true, nil
		}
	}
	return memory.Work{}, false, nil
}

func (s *fakeStore) LatestWork(_ context.Context, kind string) (memory.Work, bool, error) {
	for n := len(s.works) - 1; n >= 0; n-- {
		if s.works[n].Kind == kind {
			return s.works[n], true, nil
		}
	}
	return memory.Work{}, false, nil
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
	var out []memory.Fact
	for _, f := range s.facts {
		if _, asked := s.asked[f.ID]; !asked && f.Subject == person && !f.At.Before(since) && len(out) < limit {
			out = append(out, f)
		}
	}
	return out, nil
}

func (s *fakeStore) MarkFactsAsked(_ context.Context, ids []ulid.ULID, at time.Time) error {
	for _, id := range ids {
		s.asked[id] = at
	}
	return nil
}

func (s *fakeStore) SaveFact(_ context.Context, f memory.Fact) error {
	if s.factErr != nil {
		return s.factErr
	}
	s.facts = append([]memory.Fact{f}, s.facts...)
	return nil
}

// RecallCandidates mimics the store: only facts about subjects; those sharing
// an area first, then the rest, newest first within each group (s.facts is
// kept newest first).
func (s *fakeStore) RecallCandidates(_ context.Context, subjects, areas []string, limit int) ([]memory.Fact, error) {
	s.recallAreas = append(s.recallAreas, areas)
	s.recallSubjects = append(s.recallSubjects, subjects)
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
	var out []memory.Person
	for _, p := range s.people {
		if p.Relation != "" && len(out) < limit {
			out = append(out, p)
		}
	}
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

func (s *fakeStore) UnlabelledFacts(_ context.Context, limit int) ([]memory.Fact, error) {
	var out []memory.Fact
	for _, f := range s.facts {
		if len(f.Areas) == 0 && len(out) < limit {
			out = append(out, f)
		}
	}
	return out, nil
}

func (s *fakeStore) SetFactAreas(_ context.Context, id ulid.ULID, areas []string) error {
	for i := range s.facts {
		if s.facts[i].ID == id {
			s.facts[i].Areas = areas
		}
	}
	return nil
}

type recallArgs struct {
	source, thread string
	since          time.Time
	exclude        ulid.ULID
	limit          int
}

func (s *fakeStore) RecentExchanges(_ context.Context, source, thread string, since time.Time,
	exclude ulid.ULID, limit int) ([]memory.Exchange, error) {
	s.recalls = append(s.recalls, recallArgs{source, thread, since, exclude, limit})
	return s.exchanges, s.recallErr
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

// fakeSystem1 answers the intent question with a fixed choice and confidence,
// and the follow-up question with a fixed probability. The mind may call it
// from two goroutines at once.
type fakeSystem1 struct {
	choice      string
	confidence  float64
	refused     bool
	err         error
	followProb  float64
	followErr   error
	relevant    map[string]bool // fact text → relevant, for recall checks
	recallErr   error
	life        map[string]lifeAnswer // text → life-area answer; default: personal, no area
	lifeErr     error
	mu          sync.Mutex
	requests    []system1.Request
	followCalls []system1.Request
	recallCalls []system1.Request
	lifeCalls   []string // texts labelled
	circleCalls []system1.Request
	scores      map[string]float64 // work title → level index (0-9), for skill scoring
	scoreCalls  []system1.Request
}

type lifeAnswer struct {
	areas           map[string]float64
	personal, broad float64
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
	if state, ok := req.State.(map[string]any); ok && state["relation"] != nil {
		relation := state["relation"].(string)
		f.circleCalls = append(f.circleCalls, req)
		circle := "other"
		if strings.Contains(relation, "father") || strings.Contains(relation, "cousin") {
			circle = "family"
		}
		return system1.Response{Provider: "fake1/1", CostUSD: 0.00003, Answers: map[string]decision.Answer{
			"circle": {Type: decision.QuestionChoice, Choice: circle, Confidence: 0.95},
		}}, nil
	}
	if state, ok := req.State.(map[string]any); ok && state["text"] != nil {
		text := state["text"].(string)
		f.lifeCalls = append(f.lifeCalls, text)
		if f.lifeErr != nil {
			return system1.Response{}, f.lifeErr
		}
		la, ok := f.life[text]
		if !ok {
			la = lifeAnswer{areas: map[string]float64{"other": 1}, personal: 1}
		}
		return system1.Response{Provider: "fake1/1", CostUSD: 0.00004, Answers: map[string]decision.Answer{
			"area":     {Type: decision.QuestionChoice, Probabilities: la.areas},
			"personal": {Type: decision.QuestionPredicate, Probability: la.personal},
			"broad":    {Type: decision.QuestionPredicate, Probability: la.broad},
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
		f.followCalls = append(f.followCalls, req)
		if f.followErr != nil {
			return system1.Response{}, f.followErr
		}
		return system1.Response{Provider: "fake1/1", CostUSD: 0.00002, Answers: map[string]decision.Answer{
			"answers_previous": {Type: decision.QuestionPredicate, Probability: f.followProb},
		}}, nil
	}
	f.requests = append(f.requests, req)
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
	perms      []system2.Permission
	lessons    []system2.LessonLearned
	feedback   []system2.Feedback
	tastes     []system2.Taste
	goals      []system2.GoalTaken
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
	return system2.Response{Provider: "fake2/1", Text: "a thoughtful reply", Remember: f.remember, WhoTheyAre: f.whoTheyAre, Reminders: f.reminders, Promises: f.promises, Permissions: f.perms, Lessons: f.lessons, Feedback: f.feedback, Tastes: f.tastes, Goals: f.goals, CostUSD: 0.001}, nil
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
		Life:  testLife,
		Store: s, Replier: r, System1: s1, System2: s2, Incoming: testSet, FollowUp: testFollowUp, LifeArea: testLifeArea,
		FactRelevance: testFactRelevance, RecallCandidates: 30, PersonCircle: testPersonCircle, OpenThreads: testOpenThreads,
		ReplyPrompt: Prompt{Ref: "reply@test", Text: "You are {name}, talking with {speaker}. {who_they_are}\n{life_story}"},
		Working:     WorkingMemory{Turns: 8, Window: 2 * time.Hour},
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	}), s
}

var now = time.Date(2026, 10, 27, 13, 16, 0, 0, time.UTC)

// sam is the person every test percept comes from.
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
		{"confident remind is answered in his own words", &fakeSystem1{choice: "remind", confidence: 0.93}, &fakeSystem2{},
			[]string{"remind", "reply"}, "a thoughtful reply"},
		{"confident question goes to System 2", &fakeSystem1{choice: "question", confidence: 0.93}, &fakeSystem2{},
			[]string{"question", "reply"}, "a thoughtful reply"},
		{"middling goes to System 2", &fakeSystem1{choice: "remember", confidence: 0.6}, &fakeSystem2{},
			[]string{"ask", "reply"}, "a thoughtful reply"},
		{"confident other goes to System 2", &fakeSystem1{choice: "other", confidence: 0.9}, &fakeSystem2{},
			[]string{"other", "reply"}, "a thoughtful reply"},
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
			if len(s.decisions) == 2 && s.decisions[0].Reply != "" {
				t.Errorf("the handing-over decision should not carry the reply: %q", s.decisions[0].Reply)
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
				if root.Outcome.Detail != "handed to System 2" || child.Outcome.Status != decision.OutcomeOK {
					t.Errorf("outcomes: root %+v, child %+v", root.Outcome, child.Outcome)
				}
			}
		})
	}
}

func TestSystem2GetsConversationNoteAndMessage(t *testing.T) {
	s2 := &fakeSystem2{}
	m, s := newMind(&fakeReplier{}, &fakeSystem1{choice: "remember", confidence: 0.2}, s2)
	s.exchanges = []memory.Exchange{
		{Message: "I want help finding a dentist", Reply: "Which city are you in?"},
		{Message: "hmm"}, // no reply recorded
	}
	p := percept("1", "Let's say Lagos")

	m.handle(context.Background(), p)

	if len(s.recalls) != 1 {
		t.Fatalf("recalls = %d", len(s.recalls))
	}
	got := s.recalls[0]
	if got.source != "test" || got.thread != "chat-1" || got.exclude != p.ID || got.limit != 8 ||
		!got.since.Equal(now.Add(-2*time.Hour)) {
		t.Errorf("recall args = %+v", got)
	}

	req := s2.requests[0]
	wantHistory := []system2.Turn{
		{Role: system2.RolePerson, Text: "I want help finding a dentist"},
		{Role: system2.RoleAgent, Text: "Which city are you in?"},
		{Role: system2.RolePerson, Text: "hmm"},
	}
	if !slices.Equal(req.History, wantHistory) {
		t.Errorf("history = %+v", req.History)
	}
	want := "You are Tobi, talking with Sam. You first met Sam on 27 Oct 2026 and don't know yet who they are to you. Don't assume more than they tell you.\n" +
		"- Tobi is 29 years old.\n- Tobi is a Catholic."
	if req.Instructions != want {
		t.Errorf("instructions = %q", req.Instructions)
	}
	if req.Message != "Let's say Lagos" || !strings.Contains(req.Note, "Sam's latest message") ||
		!strings.Contains(req.Note, "unsure") || !strings.Contains(req.Note, `"remember" (20% sure)`) {
		t.Errorf("request = %+v", req)
	}
}

func TestPersonalityIsInEveryReply(t *testing.T) {
	s2 := &fakeSystem2{}
	m, _ := newMind(&fakeReplier{}, &fakeSystem1{choice: "ask_about_reader", confidence: 0.9}, s2)
	m.ReplyPrompt.Text = "You are {name}.\n{personality}"
	m.Life.Personality = []string{"Tobi is warm.", "Tobi asks follow-up questions."}

	m.handle(context.Background(), percept("1", "How are you?"))

	if got := s2.requests[0].Instructions; got != "You are Tobi.\n- Tobi is warm.\n- Tobi asks follow-up questions." {
		t.Errorf("instructions = %q", got)
	}
}

func TestSmallTalkBringsUpWhatsGoingOn(t *testing.T) {
	interview := memory.Fact{ID: ulid.Make(), Subject: sam, Text: "I have a job interview on Friday", At: now.Add(-24 * time.Hour)}
	male := memory.Fact{ID: ulid.Make(), Subject: sam, Text: "I am a male", At: now.Add(-48 * time.Hour)}
	oldCold := memory.Fact{ID: ulid.Make(), Subject: sam, Text: "I have a bad cold", At: now.Add(-60 * 24 * time.Hour)}
	mine := memory.Fact{ID: ulid.Make(), Subject: memory.SubjectSelf, Text: "Tobi is making a beat", At: now.Add(-time.Hour)}
	s1 := &fakeSystem1{choice: "small_talk", confidence: 0.95, relevant: map[string]bool{
		"Sam have a job interview on Friday": true, "Sam have a bad cold": true, // the reflex does not fix verb agreement
	}}
	s2 := &fakeSystem2{}
	m, s := newMind(&fakeReplier{}, s1, s2)
	s.facts = []memory.Fact{mine, interview, male, oldCold}

	m.handle(context.Background(), percept("1", "Hi"))

	if len(s1.recallCalls) != 1 {
		t.Fatalf("checks = %d", len(s1.recallCalls))
	}
	call := s1.recallCalls[0]
	checked := call.State.(map[string]any)["facts"].([]string)
	if !slices.Equal(checked, []string{"Sam have a job interview on Friday", "Sam is a male"}) {
		t.Errorf("checked %v: want only Sam's facts from the last 30 days", checked)
	}
	if call.State.(map[string]any)["message"] != nil || call.Questions[0].Instructions != "Is `facts[0]` going on for Sam?" {
		t.Errorf("call = %+v", call)
	}
	if recall := s.decisions[1]; recall.SetID != "open_threads@1" || recall.Action != "recall_open" {
		t.Errorf("recall decision = %+v", recall)
	}
	note := s2.requests[0].Note
	if !strings.Contains(note, "job interview on Friday") || strings.Contains(note, "male") || strings.Contains(note, "cold") {
		t.Errorf("note = %q", note)
	}
}

func TestNoteSaysTheFirstReadInPlainWords(t *testing.T) {
	s2 := &fakeSystem2{}
	m, _ := newMind(&fakeReplier{}, &fakeSystem1{choice: "ask_about_reader", confidence: 0.82}, s2)

	m.handle(context.Background(), percept("1", "How old are you?"))

	if note := s2.requests[0].Note; !strings.Contains(note, "took it as Sam asking about you (82% sure)") {
		t.Errorf("note = %q", note)
	}
}

func TestPeople(t *testing.T) {
	t.Run("born knowing the whole family, in memory once, then memory wins", func(t *testing.T) {
		s1 := &fakeSystem1{}
		m, s := newMind(&fakeReplier{}, s1, &fakeSystem2{})
		m.Life.Family = []self.Relative{
			{Name: "Sam", Relation: "father", Account: sam},
			{Name: "Tolu", Relation: "mother"}, // never talks to him, still family
			{Name: "Ada", Relation: "cousin"},
		}

		m.bornKnowing(context.Background())

		dad := s.people[sam]
		if dad.Name != "Sam" || dad.Relation != "Sam is Tobi's father." || dad.Circle != "family" || dad.Source != memory.KnownFromBirth {
			t.Fatalf("father = %+v", dad)
		}
		mum, cousin := s.people["family:mother:tolu"], s.people["family:cousin:ada"]
		if mum.Relation != "Tolu is Tobi's mother." || mum.Circle != "family" || cousin.Relation != "Ada is Tobi's cousin." {
			t.Errorf("relatives without an account: %+v, %+v", mum, cousin)
		}
		if len(s1.circleCalls) != 0 {
			t.Error("the family list needs no model call to know they are family")
		}

		m.Life.Family[0].Relation = "uncle" // the file changed after birth
		m.bornKnowing(context.Background())
		if s.people[sam].Relation != "Sam is Tobi's father." {
			t.Errorf("memory was overwritten by the file: %+v", s.people[sam])
		}
	})

	t.Run("known by account, not by name", func(t *testing.T) {
		s2 := &fakeSystem2{}
		m, s := newMind(&fakeReplier{}, &fakeSystem1{choice: "question", confidence: 0.9}, s2)
		m.Life.Family = []self.Relative{{Name: "Sam", Relation: "father", Account: sam}, {Name: "Tolu", Relation: "mother"}}
		m.bornKnowing(context.Background())

		m.handle(context.Background(), percept("1", "Who am I to you?"))
		impostor := percept("2", "Who am I to you?")
		impostor.Speaker = "test:another-sam" // same name, different account
		m.handle(context.Background(), impostor)

		if got := s2.requests[0].Instructions; !strings.Contains(got, "This is certain: Sam is Tobi's father.") {
			t.Errorf("father not known: %q", got)
		}
		got := s2.requests[1].Instructions
		if strings.Contains(got, "This is certain") || !strings.Contains(got, "don't know yet who they are") {
			t.Errorf("a shared name was taken for the father: %q", got)
		}
		if !strings.Contains(got, "People you know, by circle:\n- Tolu, family: Tolu is Tobi's mother.\n- Sam, family: Sam is Tobi's father.") {
			t.Errorf("known people not listed: %q", got)
		}
		met := s.people["test:another-sam"]
		if met.Source != memory.KnownByMeeting || met.Circle != memory.CircleStranger || met.Name != "Sam" {
			t.Errorf("new person not remembered as met: %+v", met)
		}
	})

	t.Run("learns who someone is, classified, without a restart", func(t *testing.T) {
		s1 := &fakeSystem1{choice: "chat", confidence: 0.9}
		s2 := &fakeSystem2{whoTheyAre: "Sam is Tobi's cousin."}
		m, s := newMind(&fakeReplier{}, s1, s2)

		m.handle(context.Background(), percept("1", "I'm your cousin!"))

		cousin := s.people[sam]
		if cousin.Relation != "Sam is Tobi's cousin." || cousin.Circle != "family" || cousin.Source != memory.KnownByTelling {
			t.Errorf("person = %+v", cousin)
		}
		q := s1.circleCalls[0].Questions[0]
		if q.Instructions != "Which circle of Tobi's life?" || q.Choices[0].Description != "Tobi's relatives" {
			t.Errorf("circle question not filled in: %+v", q)
		}
		if testPersonCircle.Questions[0].Choices[0].Description != "{agent}'s relatives" {
			t.Error("filling in names changed the shared question set")
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

func TestFollowUp(t *testing.T) {
	asked := []memory.Exchange{{Message: "I need a dentist", Reply: "Which area of Lagos are you in?"}}
	cases := []struct {
		name        string
		recent      []memory.Exchange
		s1          *fakeSystem1
		wantCalls   int // follow-up checks made
		wantActions []string
		wantSetID   string
	}{
		{"answer to minime's question continues to System 2", asked,
			&fakeSystem1{choice: "share", confidence: 0.95, followProb: 0.97}, 1,
			[]string{"continue", "reply"}, "incoming_message@1,follow_up@1,life_area@1"},
		{"new request after a question is routed on intent", asked,
			&fakeSystem1{choice: "remind", confidence: 0.99, followProb: 0.0}, 1,
			[]string{"remind", "reply"}, "incoming_message@1,follow_up@1,life_area@1"},
		{"failed follow-up check routes on intent alone", asked,
			&fakeSystem1{choice: "share", confidence: 0.95, followErr: errors.New("boom")}, 1,
			[]string{"share", "reply"}, "incoming_message@1,follow_up@1,life_area@1"},
		{"no recent reply, no check", []memory.Exchange{{Message: "hi"}},
			&fakeSystem1{choice: "share", confidence: 0.95, followProb: 1}, 0,
			[]string{"share", "reply"}, "incoming_message@1,life_area@1"},
		{"no conversation, no check", nil,
			&fakeSystem1{choice: "share", confidence: 0.95, followProb: 1}, 0,
			[]string{"share", "reply"}, "incoming_message@1,life_area@1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, s := newMind(&fakeReplier{}, tc.s1, &fakeSystem2{})
			s.exchanges = tc.recent

			m.handle(context.Background(), percept("1", "I am in Ikoyi"))

			if len(tc.s1.followCalls) != tc.wantCalls {
				t.Fatalf("follow-up checks = %d, want %d", len(tc.s1.followCalls), tc.wantCalls)
			}
			if tc.wantCalls == 1 {
				state := tc.s1.followCalls[0].State.(map[string]any)
				if state["message"] != "I am in Ikoyi" || state["previous_reply"] != "Which area of Lagos are you in?" {
					t.Errorf("follow-up state = %+v", state)
				}
			}
			if intent := tc.s1.requests[0].State.(map[string]any); len(intent) != 1 {
				t.Errorf("intent call got extra state: %+v", intent)
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
			if root.SetID != tc.wantSetID {
				t.Errorf("set = %q, want %q", root.SetID, tc.wantSetID)
			}
			if tc.wantCalls == 1 && tc.s1.followErr == nil {
				if _, ok := root.Answers["answers_previous"]; !ok || root.CostUSD < 0.00003-1e-12 {
					t.Errorf("follow-up answer and cost not recorded: %+v", root)
				}
			}
		})
	}
}

func TestSharingIsAnsweredAndRememberedBySystem2(t *testing.T) {
	// No canned "Got it": the agent replies as itself, and System 2 decides
	// what to remember, so nothing is saved verbatim by System 1.
	s2 := &fakeSystem2{remember: []system2.Memory{{About: system2.AboutSpeaker, Text: "Sam's passport expires in March 2027"}}}
	r := &fakeReplier{}
	m, s := newMind(r, &fakeSystem1{choice: "share", confidence: 0.95}, s2)
	p := percept("1", "My passport expires in March 2027")

	m.handle(context.Background(), p)

	if len(r.sent) != 1 || r.sent[0] != "a thoughtful reply" {
		t.Errorf("sent %q", r.sent)
	}
	if len(s.facts) != 1 || s.facts[0].Text != "Sam's passport expires in March 2027" || s.facts[0].Subject != sam {
		t.Fatalf("facts = %+v", s.facts)
	}
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
	// Facts and the message reach System 1 with names, not pronouns.
	s1 := &fakeSystem1{choice: "question", confidence: 0.93, relevant: map[string]bool{"Sam's passport expires in March 2027": true}}
	s2 := &fakeSystem2{}
	r := &fakeReplier{}
	m, s := newMind(r, s1, s2)
	day := time.Date(2026, 10, 20, 9, 0, 0, 0, time.UTC)
	s.facts = []memory.Fact{ // newest first
		{ID: ulid.Make(), Subject: sam, Text: "My car is a blue Golf", At: day.Add(48 * time.Hour)},
		{ID: ulid.Make(), Subject: sam, Text: "My passport expires in March 2027", At: day},
	}

	m.handle(context.Background(), percept("1", "When does my passport expire?"))

	if len(s1.recallCalls) != 1 {
		t.Fatalf("recall checks = %d", len(s1.recallCalls))
	}
	call := s1.recallCalls[0]
	state := call.State.(map[string]any)
	if len(call.Questions) != 2 || call.Questions[0].Name != s.facts[0].ID.String() ||
		call.Questions[1].Instructions != "Would `facts[1]` help with `message`?" ||
		state["message"] != "When does Sam's passport expire?" ||
		!slices.Equal(state["facts"].([]string), []string{"Sam's car is a blue Golf", "Sam's passport expires in March 2027"}) {
		t.Errorf("recall call = %+v", call)
	}
	if !slices.Equal(s.recallSubjects[0], []string{memory.SubjectSelf, sam}) {
		t.Errorf("recall subjects = %v", s.recallSubjects)
	}

	note := s2.requests[0].Note
	if !strings.Contains(note, "About Sam:\n- [f1] 20 Oct 2026: Sam's passport expires in March 2027") || strings.Contains(note, "Golf") {
		t.Errorf("note = %q", note)
	}

	// Chain: System 1 read → recall check → System 2 reply, both children of the read.
	if len(s.decisions) != 3 {
		t.Fatalf("decisions = %d", len(s.decisions))
	}
	root, recall, reply := s.decisions[0], s.decisions[1], s.decisions[2]
	if recall.SetID != "fact_relevance@1" || recall.Action != "recall" || *recall.ParentID != root.ID ||
		recall.Outcome.Detail != "kept 1 of 2 facts" || recall.CostUSD == 0 || len(recall.Answers) != 2 {
		t.Errorf("recall decision = %+v", recall)
	}
	if reply.Tier != decision.TierSystem2 || *reply.ParentID != root.ID || reply.Reply != r.sent[0] {
		t.Errorf("reply decision = %+v", reply)
	}
}

func TestNoFactsMeansNoRecallCheck(t *testing.T) {
	s1 := &fakeSystem1{choice: "question", confidence: 0.93}
	s2 := &fakeSystem2{}
	m, s := newMind(&fakeReplier{}, s1, s2)

	m.handle(context.Background(), percept("1", "What is my passport expiry?"))

	if len(s1.recallCalls) != 0 || len(s.decisions) != 2 {
		t.Fatalf("recall checks %d, decisions %d", len(s1.recallCalls), len(s.decisions))
	}
	if !strings.Contains(s2.requests[0].Note, "nothing you remember about yourself or Sam is relevant") {
		t.Errorf("note = %q", s2.requests[0].Note)
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

func TestLifeAreas(t *testing.T) {
	church := memory.Fact{ID: ulid.Make(), Subject: sam, Text: "I go to Grace Chapel", At: now.Add(-2 * time.Hour), Areas: []string{"religion"}}
	parents := memory.Fact{ID: ulid.Make(), Subject: sam, Text: "I live with my parents", At: now.Add(-time.Hour), Areas: []string{"home", "family"}}
	facts := []memory.Fact{parents, church} // newest first
	// What System 1 sees, with names in place of pronouns.
	const churchSeen, parentsSeen = "Sam go to Grace Chapel", "Sam live with Sam's parents"

	t.Run("message areas are recorded and steer recall", func(t *testing.T) {
		s1 := &fakeSystem1{choice: "question", confidence: 0.9,
			relevant: map[string]bool{churchSeen: true},
			life:     map[string]lifeAnswer{"What is Sam's church?": {areas: map[string]float64{"religion": 0.47, "other": 0.3}, personal: 1}}}
		s2 := &fakeSystem2{}
		m, s := newMind(&fakeReplier{}, s1, s2)
		s.facts = slices.Clone(facts)

		m.handle(context.Background(), percept("1", "What is my church?"))

		if !slices.Equal(s.recallAreas[0], []string{"religion"}) {
			t.Errorf("recall areas = %v (other must not count)", s.recallAreas)
		}
		check := s1.recallCalls[0].State.(map[string]any)["facts"].([]string)
		if check[0] != churchSeen {
			t.Errorf("same-area fact should be checked first: %v", check)
		}
		root := s.decisions[0]
		if _, ok := root.Answers["area"]; !ok || !strings.HasSuffix(root.SetID, ",life_area@1") {
			t.Errorf("life-area answers not on the read: %+v", root)
		}
		if !strings.Contains(s2.requests[0].Note, "Grace Chapel") || strings.Contains(s2.requests[0].Note, "parents") {
			t.Errorf("note = %q", s2.requests[0].Note)
		}
	})

	t.Run("not personal: memory is not searched", func(t *testing.T) {
		s1 := &fakeSystem1{choice: "question", confidence: 0.9,
			life: map[string]lifeAnswer{"Capital of France?": {areas: map[string]float64{"other": 1}, personal: 0}}}
		m, s := newMind(&fakeReplier{}, s1, &fakeSystem2{})
		s.facts = slices.Clone(facts)

		m.handle(context.Background(), percept("1", "Capital of France?"))

		if len(s.recallAreas) != 0 || len(s1.recallCalls) != 0 || len(s.decisions) != 2 {
			t.Errorf("searched memory: areas %v, checks %d, decisions %d", s.recallAreas, len(s1.recallCalls), len(s.decisions))
		}
	})

	t.Run("broad: every fact goes through without a relevance check", func(t *testing.T) {
		s1 := &fakeSystem1{choice: "question", confidence: 0.9,
			life: map[string]lifeAnswer{"What do Tobi know about Sam?": {areas: map[string]float64{"identity": 0.4, "other": 0.5}, personal: 0.25, broad: 1}}}
		s2 := &fakeSystem2{}
		m, s := newMind(&fakeReplier{}, s1, s2)
		s.facts = slices.Clone(facts)

		m.handle(context.Background(), percept("1", "What do you know about me?"))

		if len(s1.recallCalls) != 0 {
			t.Error("broad question still ran a relevance check")
		}
		recall := s.decisions[1]
		if recall.Action != "recall_all" || recall.Tier != decision.TierReflex || recall.Outcome.Detail != "broad question: passed all 2 facts" {
			t.Errorf("recall decision = %+v", recall)
		}
		note := s2.requests[0].Note
		if strings.Index(note, "Grace Chapel") > strings.Index(note, "parents") || !strings.Contains(note, "parents") {
			t.Errorf("all facts, oldest first: %q", note)
		}
	})

	t.Run("labelling failure searches all of memory", func(t *testing.T) {
		s1 := &fakeSystem1{choice: "question", confidence: 0.9, lifeErr: errors.New("boom"), relevant: map[string]bool{churchSeen: true}}
		r := &fakeReplier{}
		m, s := newMind(r, s1, &fakeSystem2{})
		s.facts = slices.Clone(facts)

		m.handle(context.Background(), percept("1", "What is my church?"))

		if len(s1.recallCalls) != 1 || s.recallAreas[0] != nil || r.sent[0] != "a thoughtful reply" {
			t.Errorf("checks %d, areas %v, sent %q", len(s1.recallCalls), s.recallAreas, r.sent)
		}
	})

	t.Run("facts are saved with their areas", func(t *testing.T) {
		s1 := &fakeSystem1{choice: "share", confidence: 0.95,
			life: map[string]lifeAnswer{parentsSeen: {areas: map[string]float64{"home": 0.71, "family": 0.21}, personal: 1}}}
		s2 := &fakeSystem2{remember: []system2.Memory{{About: system2.AboutSpeaker, Text: "I live with my parents"}}}
		m, s := newMind(&fakeReplier{}, s1, s2)

		m.handle(context.Background(), percept("1", "I live with my parents"))

		if len(s.facts) != 1 || !slices.Equal(s.facts[0].Areas, []string{"home", "family"}) {
			t.Fatalf("facts = %+v", s.facts)
		}
	})

	t.Run("a fact in no area is marked other, so it is not relabelled", func(t *testing.T) {
		s1 := &fakeSystem1{choice: "share", confidence: 0.95}
		s2 := &fakeSystem2{remember: []system2.Memory{{About: system2.AboutSpeaker, Text: "Blue is nice"}}}
		m, s := newMind(&fakeReplier{}, s1, s2)

		m.handle(context.Background(), percept("1", "Blue is nice"))

		if !slices.Equal(s.facts[0].Areas, []string{"other"}) {
			t.Fatalf("areas = %v", s.facts[0].Areas)
		}
	})

	t.Run("unlabelled facts are labelled at start", func(t *testing.T) {
		old := memory.Fact{ID: ulid.Make(), Subject: sam, Text: "I am 40 years old", At: now}
		const oldSeen = "Friend is 40 years old" // names are not known at start
		s1 := &fakeSystem1{life: map[string]lifeAnswer{oldSeen: {areas: map[string]float64{"identity": 0.98}, personal: 1}}}
		m, s := newMind(&fakeReplier{}, s1, &fakeSystem2{})
		s.facts = []memory.Fact{old, church}

		m.labelUnlabelledFacts(context.Background())

		if !slices.Equal(s.facts[0].Areas, []string{"identity"}) || !slices.Equal(s1.lifeCalls, []string{oldSeen}) {
			t.Errorf("facts %+v, labelled %v", s.facts, s1.lifeCalls)
		}
	})
}

func TestRecallFailureStillReplies(t *testing.T) {
	s2 := &fakeSystem2{}
	r := &fakeReplier{}
	m, s := newMind(r, &fakeSystem1{choice: "question", confidence: 0.9}, s2)
	s.recallErr = errors.New("db down")

	m.handle(context.Background(), percept("1", "hello"))

	if len(s2.requests) != 1 || s2.requests[0].History != nil || len(r.sent) != 1 || r.sent[0] != "a thoughtful reply" {
		t.Fatalf("requests %+v, sent %q", s2.requests, r.sent)
	}
}

func TestNoThreadMeansNoRecall(t *testing.T) {
	m, s := newMind(&fakeReplier{}, &fakeSystem1{choice: "question", confidence: 0.9}, &fakeSystem2{})
	p := percept("1", "hello")
	p.Thread = ""

	m.handle(context.Background(), p)

	if len(s.recalls) != 0 {
		t.Errorf("recalled without a thread: %+v", s.recalls)
	}
}

func TestSystem1GetsOnlyTheMessage(t *testing.T) {
	s1 := &fakeSystem1{choice: "remember", confidence: 0.9}
	m, _ := newMind(&fakeReplier{}, s1, &fakeSystem2{})

	m.handle(context.Background(), percept("1", "hello"))

	state := s1.requests[0].State.(map[string]any)
	if state["message"] != "hello" || len(state) != 1 {
		t.Errorf("state = %+v", state)
	}
}

func TestNonTextIsAReflex(t *testing.T) {
	s1 := &fakeSystem1{}
	m, s := newMind(&fakeReplier{}, s1, &fakeSystem2{})

	m.handle(context.Background(), percept("1", ""))

	if len(s1.requests) != 0 {
		t.Error("System 1 was called for a message without text")
	}
	if d := s.decisions[0]; d.Tier != decision.TierReflex || d.SetID != nonTextSetID {
		t.Errorf("decision = %+v", d)
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

func TestFailedReplyIsRecordedOnTheActingDecision(t *testing.T) {
	m, s := newMind(&fakeReplier{err: errors.New("network down")},
		&fakeSystem1{choice: "question", confidence: 0.9}, &fakeSystem2{})

	m.handle(context.Background(), percept("1", "hello"))

	if o := s.decisions[1].Outcome; o.Status != decision.OutcomeFailed || o.Detail != "network down" {
		t.Fatalf("child outcome = %+v", o)
	}
	if o := s.decisions[0].Outcome; o.Status != decision.OutcomeOK {
		t.Fatalf("root outcome = %+v", o)
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
	var out []memory.Principle
	for _, p := range s.principles {
		if status == "" || p.Status == status {
			out = append(out, p)
		}
	}
	return out, nil
}

func (s *fakeStore) JudgmentCases(_ context.Context, status string, types []string) ([]memory.JudgmentCase, error) {
	var out []memory.JudgmentCase
	for _, c := range s.cases {
		if (status == "" || c.Status == status) && (len(types) == 0 || slices.Contains(types, c.Type)) {
			out = append(out, c)
		}
	}
	return out, nil
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
	var out []memory.Call
	for i := len(s.calls) - 1; i >= 0 && len(out) < limit; i-- {
		if status == "" || s.calls[i].Status == status {
			out = append(out, s.calls[i])
		}
	}
	return out, nil
}

func (s *fakeStore) CallsAskedSince(_ context.Context, mode string, since time.Time) (int, error) {
	n := 0
	for _, c := range s.calls {
		if c.Mode == mode && !c.AskedAt.IsZero() && !c.AskedAt.Before(since) {
			n++
		}
	}
	return n, nil
}

func (s *fakeStore) MarkCallAsked(_ context.Context, id ulid.ULID, where json.RawMessage, at time.Time) error {
	if c := s.callAt(id); c != nil && c.Status == memory.CallLogged {
		c.Status, c.Message, c.AskedAt = memory.CallAsked, where, at
	}
	return nil
}

func (s *fakeStore) StaleCalls(_ context.Context, before time.Time, limit int) ([]memory.Call, error) {
	var out []memory.Call
	for _, c := range s.calls {
		if c.Status == memory.CallAsked && c.AskedAt.Before(before) && len(out) < limit {
			out = append(out, c)
		}
	}
	return out, nil
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
	for i := len(s.calls) - 1; i >= 0; i-- {
		if c := s.calls[i]; c.Waiting != "" && !c.WaitingAt.Before(since) {
			return c, true, nil
		}
	}
	return memory.Call{}, false, nil
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

func (s *fakeStore) CallsToLookBack(_ context.Context, types []string, from, to time.Time, limit int) ([]memory.Call, error) {
	var out []memory.Call
	for _, c := range s.calls {
		if c.Outcome == "" && slices.Contains(types, c.Type) && !c.CreatedAt.Before(from) && c.CreatedAt.Before(to) &&
			c.Status != memory.CallAsked && len(out) < limit {
			out = append(out, c)
		}
	}
	return out, nil
}

func (s *fakeStore) SetOutcome(_ context.Context, id ulid.ULID, outcome, verdict, note, by string, at time.Time) (bool, error) {
	c := s.callAt(id)
	if c == nil || (c.OutcomeBy == memory.OutcomeByOwner && by != memory.OutcomeByOwner) {
		return false, nil
	}
	c.Outcome, c.Verdict, c.OutcomeNote, c.OutcomeBy, c.OutcomeAt = outcome, verdict, note, by, at
	for i := range s.cases {
		if s.cases[i].ID == c.CaseID {
			s.cases[i].Outcome = outcome + ", " + verdict + ": " + note
		}
	}
	return true, nil
}

func (s *fakeStore) SaveCaseEmbedding(_ context.Context, id ulid.ULID, v []float32, model string) error {
	for i := range s.cases {
		if s.cases[i].ID == id {
			s.cases[i].Embedding, s.cases[i].EmbeddedWith = v, model
		}
	}
	return nil
}

func (s *fakeStore) SaveTaste(_ context.Context, t memory.Taste) error {
	s.tastes = append(s.tastes, t)
	return nil
}

func (s *fakeStore) Tastes(_ context.Context, limit int) ([]memory.Taste, error) {
	var out []memory.Taste
	for i := len(s.tastes) - 1; i >= 0 && len(out) < limit; i-- {
		if s.tastes[i].RetiredAt.IsZero() {
			out = append(out, s.tastes[i])
		}
	}
	return out, nil
}
