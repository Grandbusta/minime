package mind

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"minime/internal/decision"
	"minime/internal/memory"
	"minime/internal/perception"
	"minime/internal/providers/system2"
)

// A day of its own, like a person: in the morning it plans a few things from
// its interests, its projects and its routine; through the day it does them,
// for real, in writing; at night it writes a journal entry. What it makes is
// kept as works, what it did becomes facts about itself, and projects carry
// over from one day to the next.

// Day is how the agent lives its own day.
type Day struct {
	PlanAt         time.Duration // when it plans its day, from midnight in its time zone
	JournalAt      time.Duration // when it writes its journal
	MaxActivities  int           // things it plans a day, besides the journal
	BudgetUSD      float64       // most its own day may cost; past it, the rest waits for tomorrow
	PlanPrompt     Prompt        // how System 2 plans the day; zero: no day of its own
	ActivityPrompt Prompt        // how System 2 does an activity
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

// lifeActions are the decisions its own day is made of, for the budget.
var lifeActions = []string{"plan_day", "activity"}

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

var planFormat = &system2.Format{Name: "day_plan", Schema: map[string]any{
	"type": "object", "additionalProperties": false, "required": []string{"activities"},
	"properties": map[string]any{
		"activities": map[string]any{"type": "array", "items": map[string]any{
			"type": "object", "additionalProperties": false,
			"required": []string{"at", "what", "project_id", "new_project_title", "project_kind"},
			"properties": map[string]any{
				"at":                map[string]any{"type": "string", "description": "HH:MM, local time"},
				"what":              map[string]any{"type": "string"},
				"project_id":        map[string]any{"type": "string", "description": "an existing project's id, or empty"},
				"new_project_title": map[string]any{"type": "string", "description": "a new project's title, or empty"},
				"project_kind":      map[string]any{"type": "string", "description": "music, writing or learning; empty if no project"},
			},
		}},
	},
}}

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
			fmt.Fprintf(&note, "\n- id %s: %s (%s), last worked on %s. Where it stands: %s", p.ID, p.Title, p.Kind,
				p.UpdatedAt.In(m.Zone).Format("2 Jan"), orNone(p.Notes))
		}
	}
	if len(facts) > 0 {
		note.WriteString("\n\nWhat you remember about yourself lately:")
		for i := len(facts) - 1; i >= 0; i-- { // oldest first
			fmt.Fprintf(&note, "\n- %s: %s", facts[i].At.In(m.Zone).Format("2 Jan"), facts[i].Text)
		}
	}
	if j, ok, err := m.Store.LatestWork(ctx, "journal"); err == nil && ok {
		fmt.Fprintf(&note, "\n\nYour last journal entry (%s):\n%s", j.CreatedAt.In(m.Zone).Format("Monday 2 Jan"), j.Text)
	}

	resp, err := m.System2.Reason(ctx, system2.Request{
		Instructions: m.lifeInstructions(m.Day.PlanPrompt),
		Note:         note.String(),
		Format:       planFormat,
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
	journal := midnight.Add(m.Day.JournalAt)
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
		if due.Before(now) || !due.Before(journal) {
			continue // in the past, or after the journal: not today
		}
		var project ulid.ULID
		switch id := strings.TrimSpace(a.ProjectID); {
		case known[id]:
			project = ulid.MustParse(id)
		case strings.TrimSpace(a.NewProjectTitle) != "":
			p := memory.Project{ID: ulid.Make(), Title: strings.TrimSpace(a.NewProjectTitle), Kind: orDefault(a.ProjectKind, "writing"),
				Status: memory.ProjectActive, CreatedAt: now.UTC(), UpdatedAt: now.UTC()}
			if err := m.Store.SaveProject(ctx, p); err != nil {
				m.Log.Error("save project", "err", err)
				continue
			}
			known[p.ID.String()], project = true, p.ID
		}
		if m.intend(ctx, what, project, due, now) {
			lines = append(lines, due.Format("15:04")+" "+what)
		}
	}
	if m.intend(ctx, journalText, ulid.ULID{}, journal, now) {
		lines = append(lines, journal.Format("15:04")+" "+journalText)
	}
	d.Reply = strings.Join(lines, "\n")
	m.finishLife(ctx, d, start, decision.OutcomeOK, fmt.Sprintf("planned %d things", len(lines)))
	m.Log.Info("planned the day", "things", len(lines), "cost_usd", resp.CostUSD)
}

// intend saves an activity of its own day.
func (m *Mind) intend(ctx context.Context, what string, project ulid.ULID, due, now time.Time) bool {
	err := m.Store.SaveIntention(ctx, memory.Intention{ID: ulid.Make(), Subject: memory.SubjectSelf, Kind: memory.IntentionActivity,
		ProjectID: project, Text: what, DueAt: due.UTC(), Status: memory.IntentionPending, CreatedAt: now.UTC()})
	if err != nil {
		m.Log.Error("save intention", "err", err)
		return false
	}
	return true
}

var activityFormat = &system2.Format{Name: "activity", Schema: map[string]any{
	"type": "object", "additionalProperties": false,
	"required": []string{"title", "kind", "text", "project_notes", "project_done", "remember"},
	"properties": map[string]any{
		"title":         map[string]any{"type": "string"},
		"kind":          map[string]any{"type": "string", "enum": []string{"journal", "lyrics", "beat_idea", "notes", "writing"}},
		"text":          map[string]any{"type": "string"},
		"project_notes": map[string]any{"type": "string"},
		"project_done":  map[string]any{"type": "boolean"},
		"remember":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
	},
}}

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

	// What it does is a percept of its own life, so facts and decisions point at it.
	p := perception.Percept{ID: ulid.Make(), Source: lifeSource, ExternalID: "activity:" + i.ID.String(),
		Kind: "activity", Text: i.Text, At: now.UTC()}
	if _, err := m.Store.SavePercept(ctx, p); err != nil {
		log.Error("save percept", "err", err)
		return
	}
	start := time.Now()
	d := newDecision(p, nil, start)
	d.Tier, d.SetID, d.Action = decision.TierSystem2, m.Day.ActivityPrompt.Ref, "activity"

	note, project, ok := m.activityNote(ctx, i, now)
	if !ok {
		m.finish(ctx, i, memory.IntentionCancelled, "project not found", now)
		m.finishLife(ctx, d, start, decision.OutcomeFailed, "project not found")
		return
	}
	resp, err := m.System2.Reason(ctx, system2.Request{
		Instructions: m.lifeInstructions(m.Day.ActivityPrompt),
		Note:         note,
		Format:       activityFormat,
	})
	if err != nil {
		log.Error("activity failed", "err", err)
		m.finishLife(ctx, d, start, decision.OutcomeFailed, err.Error()) // still pending: tried again next tick
		return
	}
	d.Provider, d.CostUSD = resp.Provider, resp.CostUSD
	var out struct {
		Title        string   `json:"title"`
		Kind         string   `json:"kind"`
		Text         string   `json:"text"`
		ProjectNotes string   `json:"project_notes"`
		ProjectDone  bool     `json:"project_done"`
		Remember     []string `json:"remember"`
	}
	if err := json.Unmarshal([]byte(resp.Text), &out); err != nil || strings.TrimSpace(out.Text) == "" {
		m.finish(ctx, i, memory.IntentionMissed, "nothing came of it", now)
		m.finishLife(ctx, d, start, decision.OutcomeFailed, fmt.Sprintf("no work in the answer (%v)", err))
		return
	}

	title := orDefault(out.Title, i.Text)
	w := memory.Work{ID: ulid.Make(), IntentionID: i.ID, ProjectID: i.ProjectID, Kind: orDefault(out.Kind, "writing"),
		Title: title, Text: strings.TrimSpace(out.Text), CreatedAt: now.UTC()}
	if i.Text == journalText {
		w.Kind = "journal"
	}
	if err := m.Store.SaveWork(ctx, w); err != nil {
		log.Error("save work", "err", err)
	}
	if project != nil {
		project.Notes = orDefault(out.ProjectNotes, project.Notes)
		project.UpdatedAt = now.UTC()
		if out.ProjectDone {
			project.Status = memory.ProjectDone
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
	m.finish(ctx, i, memory.IntentionDone, title, now)
	d.Reply = title + "\n\n" + w.Text
	m.finishLife(ctx, d, start, decision.OutcomeOK, fmt.Sprintf("made %s %q", w.Kind, title))
	log.Info("did something of its own", "kind", w.Kind, "title", title, "cost_usd", d.CostUSD)
}

// activityNote tells System 2 what it planned to do and where it got to: the
// project and its latest works, or for the journal, what today held. It
// returns the project (nil if none), and false if the project is gone.
func (m *Mind) activityNote(ctx context.Context, i memory.Intention, now time.Time) (string, *memory.Project, bool) {
	var b strings.Builder
	b.WriteString(m.now(now))
	if i.Text == journalText {
		b.WriteString("\n\nIt's evening: write today's journal entry, a short honest look back at your day, in your own voice. Use title for the date and a few words, and kind journal.")
		m.writeToday(ctx, &b, now)
		return b.String(), nil, true
	}
	fmt.Fprintf(&b, "\n\nWhat you planned to do now: %s", i.Text)
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

// writeToday adds what the day held, for the journal: what it planned and
// what came of it, and what it made.
func (m *Mind) writeToday(ctx context.Context, b *strings.Builder, now time.Time) {
	midnight := m.startOfDay(now)
	if is, err := m.Store.IntentionsDue(ctx, midnight, now); err == nil && len(is) > 0 {
		b.WriteString("\n\nWhat you planned today and how it went:")
		for _, i := range is {
			if i.Subject != memory.SubjectSelf || i.Text == journalText {
				continue
			}
			fmt.Fprintf(b, "\n- %s %s: %s", i.DueAt.In(m.Zone).Format("15:04"), i.Text, i.Status)
		}
	}
	if ws, err := m.Store.WorksBetween(ctx, midnight, now); err == nil && len(ws) > 0 {
		b.WriteString("\n\nWhat you made today:")
		for _, w := range ws {
			fmt.Fprintf(b, "\n- %s: %s", w.Kind, w.Title)
		}
	}
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

// lifeInstructions renders a prompt about its own day: who it is.
func (m *Mind) lifeInstructions(p Prompt) string {
	list := func(lines []string, none string) string {
		if len(lines) == 0 {
			return none
		}
		return "- " + strings.Join(lines, "\n- ")
	}
	return strings.NewReplacer("{name}", m.Life.Name, "{life_story}", list(m.Life.Story, "- (none)"),
		"{personality}", list(m.Life.Personality, "- Be yourself."),
		"{routine}", list(m.Life.Routine, "- No fixed routine: plan as you like."),
		"{max}", fmt.Sprint(m.Day.MaxActivities)).Replace(p.Text)
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
