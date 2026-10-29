package mind

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"minime/internal/ability"
	"minime/internal/ability/journal"
	"minime/internal/action"
	"minime/internal/decision"
	"minime/internal/memory"
	"minime/internal/perception"
	"minime/internal/providers/system2"
	"minime/internal/self"
)

// A day of its own, like a person: in the morning it plans a few things from
// its interests, its projects and its routine; through the day it does them,
// for real, in writing; at night it writes a journal entry. What it makes is
// kept as works, what it did becomes facts about itself, and projects carry
// over from one day to the next. What it can do beyond writing (read, write
// its journal) are its abilities.

// Day is how the agent lives its own day.
type Day struct {
	PlanAt         time.Duration // when it plans its day, from midnight in its time zone
	JournalAt      time.Duration // when it writes its journal
	MaxActivities  int           // things it plans a day, besides the journal
	BudgetUSD      float64       // most its own day may cost; past it, the rest waits for tomorrow
	PlanPrompt     Prompt        // how System 2 plans the day; zero: no day of its own
	ActivityPrompt Prompt        // how System 2 does an activity
	ReviewDay      time.Weekday  // its weekly review: the day
	ReviewAt       time.Duration // and the time, from midnight in its time zone
	ReviewPrompt   Prompt        // how System 2 reviews the week; zero: no review
	LearnPrompt    Prompt        // how System 2 plans to learn a skill for a goal; zero: no learning plans
}

const (
	// journalText is the journal's intention: it is planned every day.
	journalText = "Write today's journal entry."
	// lifeSource is the source of the percepts of its own day.
	lifeSource = "life"
	// busyFor: a conversation this recent means it is busy; activities wait.
	busyFor = 10 * time.Minute
	// postponeBy is how long a busy activity waits.
	postponeBy = 30 * time.Minute
	// activityGiveUp: an activity this late is missed; the day moved on.
	activityGiveUp = 6 * time.Hour
	projectsInPlan = 8
	factsInPlan    = 20
	worksInContext = 3
)

// lifeActions are the decisions its own day is made of, for the budget:
// promises count, though they are kept even past it.
var lifeActions = []string{"plan_day", "activity", "task", "weekly_review", "plan_learning"}

func (m *Mind) dayOn() bool { return m.Day.PlanPrompt.Text != "" && m.Day.ActivityPrompt.Text != "" }

// startOfDay is midnight of t's day in the agent's time zone.
func (m *Mind) startOfDay(t time.Time) time.Time {
	l := t.In(m.Zone)
	return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, m.Zone)
}

// planDay plans today once, at PlanAt or the first tick after it, unless
// today is already planned or it is too late for a day: past journal time.
func (m *Mind) planDay(ctx context.Context, now time.Time) {
	if !m.dayOn() {
		return
	}
	midnight := m.startOfDay(now)
	today := midnight.Format("2006-01-02")
	if m.planned == today || now.Before(midnight.Add(m.Day.PlanAt)) {
		return
	}
	if !now.Before(midnight.Add(m.Day.JournalAt)) {
		m.planned = today
		return
	}
	existing, err := m.Store.IntentionsDue(ctx, midnight, midnight.AddDate(0, 0, 1))
	if err != nil {
		m.Log.Warn("checking today's plan failed", "err", err)
		return
	}
	for _, i := range existing {
		if i.Subject == memory.SubjectSelf && i.Kind == memory.IntentionActivity {
			m.planned = today // already planned, e.g. before a restart
			return
		}
	}
	m.planned = today
	if m.overBudget(ctx, now) {
		m.Log.Warn("over today's budget; no plan for today")
		return
	}
	m.writePlan(ctx, now, midnight)
}

// planFormat is the plan's shape; ability is one of the abilities the planner
// may choose, or "" for something it only writes.
func planFormat(abilities []ability.Ability) *system2.Format {
	names := []string{""}
	for _, a := range abilities {
		names = append(names, a.Name())
	}
	return &system2.Format{Name: "day_plan", Schema: map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"activities"},
		"properties": map[string]any{
			"activities": map[string]any{"type": "array", "items": map[string]any{
				"type": "object", "additionalProperties": false,
				"required": []string{"at", "what", "project_id", "new_project_title", "project_kind", "ability", "query", "goal_id", "resource_id"},
				"properties": map[string]any{
					"ability":           map[string]any{"type": "string", "enum": names},
					"query":             map[string]any{"type": "string", "description": "what the ability is to look for; empty if none"},
					"goal_id":           map[string]any{"type": "string", "description": "the goal this moves forward, or empty"},
					"resource_id":       map[string]any{"type": "string", "description": "a learning plan's resource this studies, or empty"},
					"at":                map[string]any{"type": "string", "description": "HH:MM, local time"},
					"what":              map[string]any{"type": "string"},
					"project_id":        map[string]any{"type": "string", "description": "an existing project's id, or empty"},
					"new_project_title": map[string]any{"type": "string", "description": "a new project's title, or empty"},
					"project_kind":      map[string]any{"type": "string", "description": "what kind of project, e.g. reading, learning, or one of the things you make; empty if no project"},
				},
			}},
		},
	}}
}

func (m *Mind) writePlan(ctx context.Context, now, midnight time.Time) {
	start := time.Now()
	d := &decision.Decision{ID: ulid.Make(), At: start.UTC(), Tier: decision.TierSystem2, SetID: m.Day.PlanPrompt.Ref,
		Action: "plan_day", Gate: decision.GateAuto}
	projects, err := m.Store.ActiveProjects(ctx, projectsInPlan)
	if err != nil {
		m.Log.Warn("loading projects failed; planning without them", "err", err)
	}
	facts, err := m.Store.RecallCandidates(ctx, []string{memory.SubjectSelf}, nil, factsInPlan)
	if err != nil {
		m.Log.Warn("loading facts failed; planning without them", "err", err)
	}

	var note strings.Builder
	note.WriteString(m.now(now))
	fmt.Fprintf(&note, " You write your journal at %s.", midnight.Add(m.Day.JournalAt).Format("15:04"))
	if len(projects) == 0 {
		note.WriteString("\n\nYou're not in the middle of anything yet.")
	} else {
		note.WriteString("\n\nWhat you're in the middle of:")
		for _, p := range projects {
			fmt.Fprintf(&note, "\n- id %s: %s (%s), last worked on %s.", p.ID, p.Title, p.Kind, p.UpdatedAt.In(m.Zone).Format("2 Jan"))
			if !p.GoalID.IsZero() {
				fmt.Fprintf(&note, " Serves goal %s.", p.GoalID)
			}
			if p.Source != nil {
				fmt.Fprintf(&note, " Reading %q by %s, %d%% through.", p.Source.Title, orDefault(p.Source.Author, "an unknown author"),
					p.PercentRead())
			}
			fmt.Fprintf(&note, " Where it stands: %s", orNone(p.Notes))
		}
	}
	if len(facts) > 0 {
		note.WriteString("\n\nWhat you remember about yourself lately:")
		for i := len(facts) - 1; i >= 0; i-- { // oldest first
			fmt.Fprintf(&note, "\n- %s: %s", facts[i].At.In(m.Zone).Format("2 Jan"), facts[i].Text)
		}
	}
	abilities := m.plannable()
	if len(abilities) == 0 {
		note.WriteString("\n\nToday you can only write; you can't read or look up anything new.")
	} else {
		note.WriteString("\n\nWhat you can do besides writing (give its name as ability):")
		for _, a := range abilities {
			fmt.Fprintf(&note, "\n- %s: %s", a.Name(), a.Describe())
		}
	}
	note.WriteString(m.growthNote(ctx, now))
	note.WriteString(m.plansNote(ctx, false))
	note.WriteString(m.foundNote(ctx))
	if j, ok, err := m.Store.LatestWork(ctx, "journal"); err == nil && ok {
		fmt.Fprintf(&note, "\n\nYour last journal entry (%s):\n%s", j.CreatedAt.In(m.Zone).Format("Monday 2 Jan"), j.Text)
	}

	resp, err := m.System2.Reason(ctx, system2.Request{
		Instructions: m.lifeInstructions(m.Day.PlanPrompt),
		Note:         note.String(),
		Format:       planFormat(abilities),
	})
	if err != nil {
		m.Log.Error("planning the day failed", "err", err)
		m.finishLife(ctx, d, start, decision.OutcomeFailed, err.Error())
		return
	}
	d.Provider, d.CostUSD = resp.Provider, resp.CostUSD
	var plan struct {
		Activities []struct {
			At              string `json:"at"`
			What            string `json:"what"`
			ProjectID       string `json:"project_id"`
			NewProjectTitle string `json:"new_project_title"`
			ProjectKind     string `json:"project_kind"`
			Ability         string `json:"ability"`
			Query           string `json:"query"`
			GoalID          string `json:"goal_id"`
			ResourceID      string `json:"resource_id"`
		} `json:"activities"`
	}
	if err := json.Unmarshal([]byte(resp.Text), &plan); err != nil {
		m.finishLife(ctx, d, start, decision.OutcomeFailed, "plan is not the requested JSON: "+err.Error())
		return
	}

	known := map[string]bool{}
	for _, p := range projects {
		known[p.ID.String()] = true
	}
	goals := map[string]bool{}
	if gs, err := m.Store.Goals(ctx, true); err == nil {
		for _, g := range gs {
			goals[g.ID.String()] = true
		}
	}
	journalAt := midnight.Add(m.Day.JournalAt)
	var lines []string
	for _, a := range plan.Activities {
		if len(lines) >= m.Day.MaxActivities {
			break
		}
		at, err := time.ParseInLocation("15:04", strings.TrimSpace(a.At), m.Zone)
		what := strings.TrimSpace(a.What)
		if err != nil || what == "" {
			continue
		}
		due := midnight.Add(time.Duration(at.Hour())*time.Hour + time.Duration(at.Minute())*time.Minute)
		if due.Before(now) || !due.Before(journalAt) {
			continue // in the past, or after the journal: not today
		}
		var project, goal ulid.ULID
		if id := strings.TrimSpace(a.GoalID); goals[id] {
			goal = ulid.MustParse(id)
		}
		switch id := strings.TrimSpace(a.ProjectID); {
		case known[id]:
			project = ulid.MustParse(id)
			m.linkGoal(ctx, project, goal)
		case strings.TrimSpace(a.NewProjectTitle) != "":
			p := memory.Project{ID: ulid.Make(), Title: strings.TrimSpace(a.NewProjectTitle), Kind: orDefault(a.ProjectKind, "writing"),
				Status: memory.ProjectActive, GoalID: goal, CreatedAt: now.UTC(), UpdatedAt: now.UTC()}
			if err := m.Store.SaveProject(ctx, p); err != nil {
				m.Log.Error("save project", "err", err)
				continue
			}
			known[p.ID.String()], project = true, p.ID
		}
		i := memory.Intention{Text: what, ProjectID: project, DueAt: due}
		if ab, ok := m.Abilities.Get(strings.TrimSpace(a.Ability)); ok && ab.Describe() != "" && m.allowed(ab) {
			i.Ability = ab.Name()
			if q := strings.TrimSpace(a.Query); q != "" {
				i.Args = map[string]string{"query": q}
			}
		}
		if r, ok := m.resourceFor(ctx, strings.TrimSpace(a.ResourceID)); ok {
			i.Ability, i.Args = r.Ability, map[string]string{"query": r.Query, "resource": r.ID.String()} // studying it
		}
		if m.intend(ctx, i, now) {
			line := due.Format("15:04") + " " + what
			if i.Ability != "" {
				line += fmt.Sprintf(" [%s: %s]", i.Ability, i.Args["query"])
			}
			lines = append(lines, line)
		}
	}
	if m.intend(ctx, memory.Intention{Text: journalText, Ability: journal.Name, DueAt: journalAt}, now) {
		lines = append(lines, journalAt.Format("15:04")+" "+journalText)
	}
	d.Reply = strings.Join(lines, "\n")
	m.finishLife(ctx, d, start, decision.OutcomeOK, fmt.Sprintf("planned %d things", len(lines)))
	m.Log.Info("planned the day", "things", len(lines), "cost_usd", resp.CostUSD)
}

// linkGoal links a project to the goal it serves, if it serves none yet.
func (m *Mind) linkGoal(ctx context.Context, project, goal ulid.ULID) {
	if goal.IsZero() {
		return
	}
	p, ok, err := m.Store.Project(ctx, project)
	if err != nil || !ok || !p.GoalID.IsZero() {
		return
	}
	p.GoalID = goal
	if err := m.Store.SaveProject(ctx, p); err != nil {
		m.Log.Error("save project", "err", err)
	}
}

// intend saves an activity of its own day: i's text, time, project and ability.
func (m *Mind) intend(ctx context.Context, i memory.Intention, now time.Time) bool {
	i.ID, i.Subject, i.Kind, i.Status = ulid.Make(), memory.SubjectSelf, memory.IntentionActivity, memory.IntentionPending
	i.DueAt, i.CreatedAt = i.DueAt.UTC(), now.UTC()
	err := m.Store.SaveIntention(ctx, i)
	if err != nil {
		m.Log.Error("save intention", "err", err)
		return false
	}
	return true
}

// activityFormat is what an activity makes: kind is one of the things it
// makes (its crafts); an ability may file the work under a kind of its own.
func activityFormat(crafts []self.Craft) *system2.Format {
	kinds := make([]string, len(crafts))
	for i, c := range crafts {
		kinds[i] = c.Kind
	}
	return &system2.Format{Name: "activity", Schema: map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []string{"title", "kind", "text", "project_notes", "project_done", "remember", "lessons", "ask_for_review", "review_question"},
		"properties": map[string]any{
			"title":           map[string]any{"type": "string"},
			"kind":            map[string]any{"type": "string", "enum": kinds},
			"text":            map[string]any{"type": "string"},
			"project_notes":   map[string]any{"type": "string"},
			"project_done":    map[string]any{"type": "boolean"},
			"remember":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"lessons":         map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"ask_for_review":  map[string]any{"type": "boolean"},
			"review_question": map[string]any{"type": "string"},
		},
	}}
}

// doActivity does one thing of its own day, unless it is busy talking with
// someone (it waits), too late (missed), or over today's budget (missed).
func (m *Mind) doActivity(ctx context.Context, i memory.Intention, now time.Time) {
	log := m.Log.With("intention", i.ID.String())
	if now.Sub(i.DueAt) > activityGiveUp {
		m.finish(ctx, i, memory.IntentionMissed, "too late", now)
		return
	}
	if m.busy(ctx, now) {
		if err := m.Store.PostponeIntention(ctx, i.ID, now.Add(postponeBy).UTC()); err != nil {
			log.Error("postpone intention", "err", err)
		}
		log.Info("busy talking; activity postponed", "until", now.Add(postponeBy).In(m.Zone).Format("15:04"))
		return
	}
	if m.overBudget(ctx, now) {
		log.Warn("over today's budget; activity missed")
		m.finish(ctx, i, memory.IntentionMissed, "over budget", now)
		return
	}
	if w, ok := m.makeWork(ctx, i, "activity", "What you planned to do now: "+i.Text, now); ok {
		m.finish(ctx, i, memory.IntentionDone, w.Title, now)
	}
}

// makeWork does an intention for real: its ability (if any) gets what it
// works from, System 2 writes it, and the work, its project and what it
// learned are kept. what tells System 2 what it is doing. If it can't be done
// the intention is finished as missed or cancelled; if System 2 fails, it
// stays pending and is tried again on the next tick. It reports whether a
// work was made.
func (m *Mind) makeWork(ctx context.Context, i memory.Intention, action, what string, now time.Time) (memory.Work, bool) {
	log := m.Log.With("intention", i.ID.String())

	// What it does is a percept of its own life, so facts and decisions point
	// at it: a new one for each attempt, since a failed attempt is tried again.
	pid := ulid.Make()
	p := perception.Percept{ID: pid, Source: lifeSource, ExternalID: action + ":" + i.ID.String() + ":" + pid.String(),
		Kind: action, Text: i.Text, At: now.UTC()}
	if _, err := m.Store.SavePercept(ctx, p); err != nil {
		log.Error("save percept", "err", err)
		return memory.Work{}, false
	}
	start := time.Now()
	d := newDecision(p, nil, start)
	d.Tier, d.SetID, d.Action = decision.TierSystem2, m.Day.ActivityPrompt.Ref, action

	note, project, ok := m.activityNote(ctx, i, what, now)
	if !ok {
		m.finish(ctx, i, memory.IntentionCancelled, "project not found", now)
		m.finishLife(ctx, d, start, decision.OutcomeFailed, "project not found")
		return memory.Work{}, false
	}
	// Its ability, if it uses one, gets what it works from: a passage, an article, its day.
	var material ability.Material
	ab, ok := m.Abilities.For(i, project)
	if !ok {
		log.Warn("needs an ability it doesn't have; missed", "ability", i.Ability)
		m.finish(ctx, i, memory.IntentionMissed, "can't "+i.Ability+" now", now)
		m.finishLife(ctx, d, start, decision.OutcomeFailed, "no ability "+i.Ability)
		return memory.Work{}, false
	}
	if ab != nil && !m.allowed(ab) {
		log.Warn("ability not allowed; missed", "ability", ab.Name())
		m.finish(ctx, i, memory.IntentionMissed, "not allowed to "+ab.Name(), now)
		m.finishLife(ctx, d, start, decision.OutcomeFailed, ab.Name()+": not allowed")
		return memory.Work{}, false
	}
	if ab != nil {
		var err error
		material, err = ab.Prepare(ctx, ability.Act{Intention: i, Project: project, Now: now, Zone: m.Zone})
		if err != nil {
			log.Warn("ability failed; missed", "ability", ab.Name(), "query", i.Args["query"], "err", err)
			m.finish(ctx, i, memory.IntentionMissed, "couldn't get "+orDefault(i.Args["query"], "what it needed"), now)
			m.finishLife(ctx, d, start, decision.OutcomeFailed, ab.Name()+": "+err.Error())
			return memory.Work{}, false
		}
		note += material.Note
		project = material.Project
	}
	resp, err := m.System2.Reason(ctx, system2.Request{
		Instructions: m.lifeInstructions(m.Day.ActivityPrompt),
		Note:         note,
		Format:       activityFormat(m.Life.Crafts()),
	})
	if err != nil {
		log.Error(action+" failed", "err", err)
		m.finishLife(ctx, d, start, decision.OutcomeFailed, err.Error()) // still pending: tried again next tick
		return memory.Work{}, false
	}
	d.Provider, d.CostUSD = resp.Provider, resp.CostUSD
	var out struct {
		Title          string   `json:"title"`
		Kind           string   `json:"kind"`
		Text           string   `json:"text"`
		ProjectNotes   string   `json:"project_notes"`
		ProjectDone    bool     `json:"project_done"`
		Remember       []string `json:"remember"`
		Lessons        []string `json:"lessons"`
		AskForReview   bool     `json:"ask_for_review"`
		ReviewQuestion string   `json:"review_question"`
	}
	if err := json.Unmarshal([]byte(resp.Text), &out); err != nil || strings.TrimSpace(out.Text) == "" {
		m.finish(ctx, i, memory.IntentionMissed, "nothing came of it", now)
		m.finishLife(ctx, d, start, decision.OutcomeFailed, fmt.Sprintf("no work in the answer (%v)", err))
		return memory.Work{}, false
	}

	title := orDefault(out.Title, i.Text)
	w := memory.Work{ID: ulid.Make(), IntentionID: i.ID, ProjectID: i.ProjectID, Kind: orDefault(material.WorkKind, orDefault(out.Kind, "writing")),
		Title: title, Text: strings.TrimSpace(out.Text), CreatedAt: now.UTC()}
	if project != nil {
		w.ProjectID = project.ID // a book it just started has a new project
	}
	if err := m.Store.SaveWork(ctx, w); err != nil {
		log.Error("save work", "err", err)
	}
	if project != nil {
		project.Notes = orDefault(out.ProjectNotes, project.Notes)
		project.UpdatedAt = now.UTC()
		if out.ProjectDone || material.Done {
			project.Status = memory.ProjectDone // finished, or e.g. read to the end
		}
		if err := m.Store.SaveProject(ctx, *project); err != nil {
			log.Error("save project", "err", err)
		}
	}
	n := names{agent: m.Life.Name, speaker: fallbackName}
	for _, f := range out.Remember {
		if f = strings.TrimSpace(f); f == "" {
			continue
		}
		cost, err := m.saveFact(ctx, p, memory.SubjectSelf, f, n)
		d.CostUSD += cost
		if err != nil {
			log.Error("save fact", "err", err)
		}
	}
	d.Reply = title + "\n\n" + w.Text
	m.finishLife(ctx, d, start, decision.OutcomeOK, fmt.Sprintf("made %s %q", w.Kind, title))
	m.scoreWork(ctx, w, p.ID, &d.ID)
	source := memory.LessonFromSelf
	if ab != nil && ab.Risk() == ability.ReadsWeb {
		source = memory.LessonFromOthers // it read how others do it
	}
	var skills []string
	if sk := m.workingOn(ctx, i, project); sk != "" {
		skills = []string{sk}
	}
	m.learnFromWork(ctx, w, out.Lessons, source, skills)
	m.studied(ctx, i, w, now)
	if out.AskForReview && action == "activity" { // not what it made for someone else
		m.askForReview(ctx, w, strings.TrimSpace(out.ReviewQuestion), now)
	}
	log.Info("made something", "action", action, "ability", i.Ability, "kind", w.Kind, "title", title, "cost_usd", d.CostUSD)
	return w, true
}

// activityNote tells System 2 what it is doing (what) and where it got to:
// the project and its latest works. It returns the project (nil if none), and
// false if the project is gone.
func (m *Mind) activityNote(ctx context.Context, i memory.Intention, what string, now time.Time) (string, *memory.Project, bool) {
	var b strings.Builder
	b.WriteString(m.now(now))
	b.WriteString("\n\n" + what)
	b.WriteString(m.lessonsNote(ctx))
	if i.ProjectID.IsZero() {
		return b.String(), nil, true
	}
	p, ok, err := m.Store.Project(ctx, i.ProjectID)
	if err != nil || !ok {
		return "", nil, false
	}
	fmt.Fprintf(&b, "\n\nIt's part of your project %q (%s), started %s. Where it stands: %s", p.Title, p.Kind,
		p.CreatedAt.In(m.Zone).Format("2 Jan"), orNone(p.Notes))
	works, err := m.Store.ProjectWorks(ctx, p.ID, worksInContext)
	if err == nil && len(works) > 0 {
		b.WriteString("\n\nWhat you made for it last:")
		for _, w := range works {
			fmt.Fprintf(&b, "\n\n[%s, %s: %s]\n%s", w.CreatedAt.In(m.Zone).Format("2 Jan"), w.Kind, w.Title, w.Text)
		}
	}
	return b.String(), &p, true
}

// busy reports whether it is in the middle of a conversation.
func (m *Mind) busy(ctx context.Context, now time.Time) bool {
	convs, err := m.Store.Conversations(ctx)
	if err != nil {
		return false
	}
	for _, c := range convs {
		if now.Sub(c.LastActivity) < busyFor {
			return true
		}
	}
	return false
}

// overBudget reports whether its own day has cost BudgetUSD or more today.
func (m *Mind) overBudget(ctx context.Context, now time.Time) bool {
	if m.Day.BudgetUSD <= 0 {
		return false
	}
	spent, err := m.Store.CostSince(ctx, m.startOfDay(now), lifeActions)
	if err != nil {
		m.Log.Warn("checking today's spending failed", "err", err)
		return false
	}
	return spent >= m.Day.BudgetUSD
}

// allowed reports whether the autonomy policy lets it use an ability: one
// that reads the web needs read_web.
func (m *Mind) allowed(a ability.Ability) bool {
	return a.Risk() != ability.ReadsWeb || m.Autonomy.Check(action.ReadWeb, "") != action.Forbid
}

// plannable is the abilities it may plan with: offered by the set, and allowed.
func (m *Mind) plannable() []ability.Ability {
	var out []ability.Ability
	for _, a := range m.Abilities.Planned() {
		if m.allowed(a) {
			out = append(out, a)
		}
	}
	return out
}

// makesList is what it makes, for a prompt: one "- kind: about" line each.
func (m *Mind) makesList() string {
	var lines []string
	for _, c := range m.Life.Crafts() {
		lines = append(lines, "- "+c.Kind+": "+c.About)
	}
	return strings.Join(lines, "\n")
}

// abilitiesList is what it can do beyond writing, for a prompt.
func (m *Mind) abilitiesList() string {
	var lines []string
	for _, a := range m.plannable() {
		lines = append(lines, "- "+a.Name()+": "+a.Describe())
	}
	if len(lines) == 0 {
		return "- (none: you can only write)"
	}
	return strings.Join(lines, "\n")
}

// lifeInstructions renders a prompt about its own day: who it is and what it makes.
func (m *Mind) lifeInstructions(p Prompt) string {
	var kinds []string
	for _, c := range m.Life.Crafts() {
		kinds = append(kinds, c.Kind)
	}
	list := func(lines []string, none string) string {
		if len(lines) == 0 {
			return none
		}
		return "- " + strings.Join(lines, "\n- ")
	}
	return strings.NewReplacer("{name}", m.Life.Name, "{life_story}", list(m.Life.Story, "- (none)"),
		"{personality}", list(m.Life.Personality, "- Be yourself."),
		"{routine}", list(m.Life.Routine, "- No fixed routine: plan as you like."),
		"{max}", fmt.Sprint(m.Day.MaxActivities), "{makes}", m.makesList(), "{kinds}", strings.Join(kinds, ", ")).Replace(p.Text)
}

func (m *Mind) finishLife(ctx context.Context, d *decision.Decision, start time.Time, status decision.OutcomeStatus, detail string) {
	d.Latency = time.Since(start)
	d.Outcome = &decision.Outcome{Status: status, Detail: detail, At: time.Now().UTC()}
	if err := m.Store.SaveDecision(ctx, *d); err != nil {
		m.Log.Error("save decision", "err", err)
	}
}

func orNone(s string) string { return orDefault(s, "(no notes yet)") }

func orDefault(s, def string) string {
	if s = strings.TrimSpace(s); s != "" {
		return s
	}
	return def
}
