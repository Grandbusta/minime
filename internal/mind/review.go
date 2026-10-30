package mind

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"minime/internal/action"
	"minime/internal/decision"
	"minime/internal/memory"
	"minime/internal/providers/system2"
	"minime/internal/self"
)

// The weekly review: once a week (Day.ReviewAt) it looks back at what it did
// and how its skills are going, checks each goal (progress, done, dropped),
// sets new goals for its dreams, links its projects to goals, and may want to
// change a dream (add, reword or drop), which the autonomy gate may make it
// ask its owner first (dream). The review itself is kept as a private work.

const (
	maxActiveGoals  = 3
	maxActiveDreams = 5
	// reviewWindow is how far back a review looks.
	reviewWindow = 7 * 24 * time.Hour
	// reviewJournalChars is how much of each journal entry a review reads.
	reviewJournalChars = 700
	// goalHorizon is the latest a new goal may be due.
	goalHorizon = 365 * 24 * time.Hour
)

var reviewFormat = &system2.Format{Name: "weekly_review", Schema: map[string]any{
	"type": "object", "additionalProperties": false,
	"required": []string{"title", "text", "goals", "new_goals", "projects", "dreams", "retire_lessons", "new_lessons", "plans", "new_resources",
		"principles", "outcomes"},
	"properties": map[string]any{
		"title": map[string]any{"type": "string"},
		"text":  map[string]any{"type": "string"},
		"goals": map[string]any{"type": "array", "items": map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"goal_id", "progress", "status", "skill", "confidence"},
			"properties": map[string]any{
				"goal_id":    map[string]any{"type": "string"},
				"confidence": map[string]any{"type": "number", "description": "dropping it: how sure you are your owner would drop it too, 0 to 1; else 0"},
				"skill":      map[string]any{"type": "string", "description": "the skill it grows, if it's about one and doesn't say yet; else empty"},
				"progress":   map[string]any{"type": "string"},
				"status":     map[string]any{"type": "string", "enum": []string{memory.GoalActive, memory.GoalDone, memory.GoalDropped}},
			},
		}},
		"new_goals": map[string]any{"type": "array", "items": map[string]any{
			"type": "object", "additionalProperties": false,
			"required": []string{"dream_id", "text", "done_when", "due", "project_ids", "skill", "why", "confidence"},
			"properties": map[string]any{
				"why":         map[string]any{"type": "string", "description": "why this goal, now"},
				"confidence":  map[string]any{"type": "number", "description": "how sure you are your owner would set it too, 0 to 1"},
				"skill":       map[string]any{"type": "string", "description": "the skill it grows, if it's about one; else empty"},
				"dream_id":    map[string]any{"type": "string"},
				"project_ids": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "projects that serve it"},
				"text":        map[string]any{"type": "string"},
				"done_when":   map[string]any{"type": "string"},
				"due":         map[string]any{"type": "string", "description": "YYYY-MM-DD"},
			},
		}},
		"projects": map[string]any{"type": "array", "items": map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"project_id", "goal_id"},
			"properties": map[string]any{
				"project_id": map[string]any{"type": "string"},
				"goal_id":    map[string]any{"type": "string"},
			},
		}},
		"plans": map[string]any{"type": "array", "items": map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"plan_id", "stage_done"},
			"properties": map[string]any{
				"plan_id":    map[string]any{"type": "string"},
				"stage_done": map[string]any{"type": "boolean", "description": "its current stage's done-when is met by what you made"},
			},
		}},
		"new_resources": map[string]any{"type": "array", "items": map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"plan_id", "ability", "query", "why"},
			"properties": map[string]any{
				"plan_id": map[string]any{"type": "string"},
				"ability": map[string]any{"type": "string"},
				"query":   map[string]any{"type": "string"},
				"why":     map[string]any{"type": "string"},
			},
		}},
		"retire_lessons": map[string]any{"type": "array", "items": map[string]any{"type": "string"},
			"description": "ids of lessons you've absorbed, or that a newer lesson replaces"},
		"new_lessons": map[string]any{"type": "array", "items": map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"skill", "lesson"},
			"properties": map[string]any{
				"skill":  map[string]any{"type": "string"},
				"lesson": map[string]any{"type": "string"},
			},
		}},
		"outcomes": outcomesFormat,
		"principles": map[string]any{"type": "array", "items": map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"text", "types", "calls"},
			"properties": map[string]any{
				"text":  map[string]any{"type": "string", "description": "a rule of your owner's, in a sentence"},
				"types": map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": self.DecisionTypes}},
				"calls": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "ids of the calls it comes from"},
			},
		}},
		"dreams": map[string]any{"type": "array", "items": map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"action", "dream_id", "text", "why"},
			"properties": map[string]any{
				"action":   map[string]any{"type": "string", "enum": []string{"add", "reword", "drop"}},
				"dream_id": map[string]any{"type": "string", "description": "for reword and drop; empty for add"},
				"text":     map[string]any{"type": "string", "description": "the dream, third person with your name; empty for drop"},
				"why":      map[string]any{"type": "string"},
			},
		}},
	},
}}

// weeklyReview runs the review once a week, at the first tick after ReviewAt
// on ReviewDay, unless it already did this week (e.g. before a restart).
func (m *Mind) weeklyReview(ctx context.Context, now time.Time) {
	if m.Day.ReviewPrompt.Text == "" {
		return
	}
	at := m.reviewTime(now)
	if now.Before(at) || m.reviewed.Equal(at) {
		return
	}
	m.reviewed = at
	if last, ok, err := m.Store.LatestWork(ctx, "review"); err == nil && ok && !last.CreatedAt.Before(at) {
		return // already reviewed this week
	}
	if err := m.Review(ctx, now); err != nil {
		m.Log.Error("weekly review failed", "err", err)
	}
}

// reviewTime is this week's review time: the latest ReviewDay at ReviewAt on or before now's day.
func (m *Mind) reviewTime(now time.Time) time.Time {
	midnight := m.startOfDay(now)
	back := (int(now.In(m.Zone).Weekday()) - int(m.Day.ReviewDay) + 7) % 7
	return midnight.AddDate(0, 0, -back).Add(m.Day.ReviewAt)
}

// Review looks back at the week, now: it is what the weekly review does, and
// `minime review` runs it on demand.
func (m *Mind) Review(ctx context.Context, now time.Time) error {
	m.bornWanting(ctx) // so a review run on demand has its dreams and skills even before the agent first started
	start := time.Now()
	d := &decision.Decision{ID: ulid.Make(), At: start.UTC(), Tier: decision.TierSystem2, SetID: m.Day.ReviewPrompt.Ref,
		Action: "weekly_review", Gate: decision.GateAuto}

	dreams, err := m.Store.Dreams(ctx, true)
	if err != nil {
		return err
	}
	goals, err := m.Store.Goals(ctx, true)
	if err != nil {
		return err
	}
	projects, err := m.Store.ActiveProjects(ctx, projectsInPlan)
	if err != nil {
		return err
	}
	var note strings.Builder
	note.WriteString(m.now(now) + " It's time for your weekly review.")
	note.WriteString(m.growthNote(ctx, now))
	note.WriteString(m.plansNote(ctx, true))
	note.WriteString(m.foundNote(ctx))
	if len(projects) > 0 {
		note.WriteString("\n\nWhat you're in the middle of:")
		for _, p := range projects {
			goal := "no goal"
			if !p.GoalID.IsZero() {
				goal = "goal " + p.GoalID.String()
			}
			fmt.Fprintf(&note, "\n- [project %s] %s (%s; %s). Where it stands: %s", p.ID, p.Title, p.Kind, goal, orNone(p.Notes))
		}
	}
	if works, err := m.Store.WorksBetween(ctx, now.Add(-reviewWindow), now); err == nil && len(works) > 0 {
		note.WriteString("\n\nWhat you made this week:")
		for _, w := range works {
			if w.Kind == "journal" {
				text := w.Text
				if r := []rune(text); len(r) > reviewJournalChars {
					text = string(r[:reviewJournalChars]) + "…"
				}
				fmt.Fprintf(&note, "\n- %s, your journal:\n%s", w.CreatedAt.In(m.Zone).Format("Mon 2 Jan"), text)
				continue
			}
			fmt.Fprintf(&note, "\n- %s, %s: %s", w.CreatedAt.In(m.Zone).Format("Mon 2 Jan"), strings.ReplaceAll(w.Kind, "_", " "), w.Title)
		}
	} else {
		note.WriteString("\n\nYou made nothing this week.")
	}
	if ls, err := m.Store.Lessons(ctx, "", 100); err == nil && len(ls) > 0 {
		note.WriteString("\n\nWhat you've learned, by skill (your active lessons):")
		for _, l := range ls {
			fmt.Fprintf(&note, "\n- [lesson %s] %s, from %s: %s", l.ID, l.Skill, lessonSource(l.Source), l.Text)
		}
	}
	fmt.Fprintf(&note, "\n\nWhat you can study with (for new resources):\n%s", m.readingList())
	fmt.Fprintf(&note, "\n\nYou can have at most %d goals at a time, and %d dreams.", maxActiveGoals, maxActiveDreams)
	note.WriteString(m.judgmentNote(ctx, reviewJudgment, reviewAbout(goals, projects)))
	disagreements, callCases := m.disagreementsNote(ctx, now)
	note.WriteString(disagreements)
	lookBack, looked := m.lookBackNote(ctx, now)
	note.WriteString(lookBack)
	for _, c := range looked { // a principle may come from how one of the owner's decisions turned out
		if !c.CaseID.IsZero() {
			if callCases == nil {
				callCases = map[string]string{}
			}
			callCases[c.ID.String()] = c.CaseID.String()
		}
	}

	resp, err := m.System2.Reason(ctx, system2.Request{
		Instructions: m.lifeInstructions(m.Day.ReviewPrompt),
		Note:         note.String(),
		Format:       reviewFormat,
	})
	if err != nil {
		m.finishLife(ctx, d, start, decision.OutcomeFailed, err.Error())
		return err
	}
	d.Provider, d.CostUSD = resp.Provider, resp.CostUSD
	var out struct {
		Title string `json:"title"`
		Text  string `json:"text"`
		Goals []struct {
			GoalID     string  `json:"goal_id"`
			Progress   string  `json:"progress"`
			Status     string  `json:"status"`
			Skill      string  `json:"skill"`
			Confidence float64 `json:"confidence"`
		} `json:"goals"`
		NewGoals []struct {
			DreamID    string   `json:"dream_id"`
			Text       string   `json:"text"`
			DoneWhen   string   `json:"done_when"`
			Due        string   `json:"due"`
			Projects   []string `json:"project_ids"`
			Skill      string   `json:"skill"`
			Why        string   `json:"why"`
			Confidence float64  `json:"confidence"`
		} `json:"new_goals"`
		Outcomes   []callOutcome `json:"outcomes"`
		Principles []struct {
			Text  string   `json:"text"`
			Types []string `json:"types"`
			Calls []string `json:"calls"`
		} `json:"principles"`
		Plans []struct {
			PlanID    string `json:"plan_id"`
			StageDone bool   `json:"stage_done"`
		} `json:"plans"`
		NewResources []struct {
			PlanID  string `json:"plan_id"`
			Ability string `json:"ability"`
			Query   string `json:"query"`
			Why     string `json:"why"`
		} `json:"new_resources"`
		Projects []struct {
			ProjectID string `json:"project_id"`
			GoalID    string `json:"goal_id"`
		} `json:"projects"`
		RetireLessons []string `json:"retire_lessons"`
		NewLessons    []struct {
			Skill  string `json:"skill"`
			Lesson string `json:"lesson"`
		} `json:"new_lessons"`
		Dreams []struct {
			Action  string `json:"action"`
			DreamID string `json:"dream_id"`
			Text    string `json:"text"`
			Why     string `json:"why"`
		} `json:"dreams"`
	}
	if err := json.Unmarshal([]byte(resp.Text), &out); err != nil || strings.TrimSpace(out.Text) == "" {
		m.finishLife(ctx, d, start, decision.OutcomeFailed, fmt.Sprintf("no review in the answer (%v)", err))
		return errors.New("no review in the answer")
	}

	var changes []string
	activeGoals := map[string]memory.Goal{}
	for _, g := range goals {
		activeGoals[g.ID.String()] = g
	}
	for _, u := range out.Goals {
		g, ok := activeGoals[strings.TrimSpace(u.GoalID)]
		if !ok {
			continue
		}
		g.Progress, g.UpdatedAt = orDefault(u.Progress, g.Progress), now.UTC()
		if g.Skill == "" {
			g.Skill = m.skillNamed(ctx, u.Skill)
		}
		if u.Status == memory.GoalDropped && m.goalCall(ctx, memory.CallDropGoal, g, u.Progress, u.Confidence, now) {
			changes = append(changes, "asked to drop goal: "+g.Text) // it stays until its owner answers
		} else if u.Status == memory.GoalDone || u.Status == memory.GoalDropped {
			g.Status = u.Status
			delete(activeGoals, g.ID.String())
			changes = append(changes, fmt.Sprintf("goal %s: %s", u.Status, g.Text))
		}
		if err := m.Store.SaveGoal(ctx, g); err != nil {
			m.Log.Error("save goal", "err", err)
		}
	}
	proposed := map[string]memory.Goal{} // new goals waiting for its owner's answer
	activeDreams := map[string]memory.Dream{}
	for _, dr := range dreams {
		activeDreams[dr.ID.String()] = dr
	}
	for _, ng := range out.NewGoals {
		due, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(ng.Due), m.Zone)
		_, dreamOK := activeDreams[strings.TrimSpace(ng.DreamID)]
		text := strings.TrimSpace(ng.Text)
		if err != nil || !dreamOK || text == "" || !due.After(now) || due.Sub(now) > goalHorizon || len(activeGoals)+len(proposed) >= maxActiveGoals {
			m.Log.Warn("dropped a new goal", "text", ng.Text, "due", ng.Due, "dream", ng.DreamID, "err", err)
			continue
		}
		g := memory.Goal{ID: ulid.Make(), DreamID: ulid.MustParse(strings.TrimSpace(ng.DreamID)), Text: text, Skill: m.skillNamed(ctx, ng.Skill),
			DoneWhen: strings.TrimSpace(ng.DoneWhen), Due: due.Add(23*time.Hour + 59*time.Minute).UTC(), Status: memory.GoalActive,
			CreatedAt: now.UTC(), UpdatedAt: now.UTC()}
		if m.Calls.On {
			g.Status = memory.GoalProposed // set once its owner answers
		}
		if err := m.Store.SaveGoal(ctx, g); err != nil {
			m.Log.Error("save goal", "err", err)
			continue
		}
		switch {
		case g.Status == memory.GoalProposed && m.goalCall(ctx, memory.CallNewGoal, g, ng.Why, ng.Confidence, now):
			proposed[g.ID.String()] = g
			changes = append(changes, "asked to set goal: "+text)
		default:
			if g.Status != memory.GoalActive {
				g.Status = memory.GoalActive
				if err := m.Store.SaveGoal(ctx, g); err != nil {
					m.Log.Error("save goal", "err", err)
					continue
				}
			}
			activeGoals[g.ID.String()] = g
			changes = append(changes, "new goal: "+text)
		}
		for _, pid := range ng.Projects {
			out.Projects = append(out.Projects, struct {
				ProjectID string `json:"project_id"`
				GoalID    string `json:"goal_id"`
			}{ProjectID: pid, GoalID: g.ID.String()})
		}
	}
	byID := map[string]memory.Project{}
	for _, p := range projects {
		byID[p.ID.String()] = p
	}
	for _, link := range out.Projects {
		p, ok := byID[strings.TrimSpace(link.ProjectID)]
		g, gok := activeGoals[strings.TrimSpace(link.GoalID)]
		if !gok {
			g, gok = proposed[strings.TrimSpace(link.GoalID)]
		}
		if !ok || !gok || p.GoalID == g.ID {
			continue
		}
		p.GoalID = g.ID
		if err := m.Store.SaveProject(ctx, p); err != nil {
			m.Log.Error("save project", "err", err)
		}
	}
	// Learning plans: move on a stage whose done-when is met; add what to study.
	if plans, err := m.Store.Plans(ctx, true); err == nil {
		byPlan := map[string]memory.LearningPlan{}
		for _, p := range plans {
			byPlan[p.ID.String()] = p
		}
		for _, u := range out.Plans {
			if p, ok := byPlan[strings.TrimSpace(u.PlanID)]; ok && u.StageDone {
				if err := m.Store.AdvancePlan(ctx, p.ID, now.UTC()); err == nil {
					changes = append(changes, fmt.Sprintf("%s: stage %d of %d done", p.Skill, p.Stage+1, len(p.Stages)))
					p.Stage++
					byPlan[p.ID.String()] = p
				}
			}
		}
		for _, nr := range out.NewResources {
			p, ok := byPlan[strings.TrimSpace(nr.PlanID)]
			if !ok || p.Stage >= len(p.Stages) {
				continue
			}
			if r, ok := m.resource(p.ID, p.Stage, nr.Ability, nr.Query, nr.Why, now); ok {
				if err := m.Store.SaveResource(ctx, r); err == nil {
					changes = append(changes, "to study: "+r.Query)
				}
			}
		}
		for _, p := range plans { // a plan ends with its goal
			if _, still := activeGoals[p.GoalID.String()]; !still {
				status := memory.PlanDone
				for _, u := range out.Goals {
					if strings.TrimSpace(u.GoalID) == p.GoalID.String() && u.Status == memory.GoalDropped {
						status = memory.PlanDropped
					}
				}
				if err := m.Store.EndPlan(ctx, p.ID, status, now.UTC()); err != nil {
					m.Log.Error("end plan", "err", err)
				}
			}
		}
	}

	// Lessons: retire what it has absorbed, keep what the week taught it.
	if ls, err := m.Store.Lessons(ctx, "", 100); err == nil {
		active := map[string]bool{}
		for _, l := range ls {
			active[l.ID.String()] = true
		}
		for _, id := range out.RetireLessons {
			if active[strings.TrimSpace(id)] {
				if err := m.Store.RetireLesson(ctx, ulid.MustParse(strings.TrimSpace(id)), now.UTC()); err == nil {
					changes = append(changes, "retired a lesson")
				}
			}
		}
	}
	if skills, err := m.Store.Skills(ctx); err == nil {
		for _, nl := range out.NewLessons {
			for _, sk := range skills {
				if strings.EqualFold(sk.Name, strings.TrimSpace(nl.Skill)) && strings.TrimSpace(nl.Lesson) != "" &&
					m.saveLesson(ctx, memory.Lesson{Skill: sk.Name, Text: strings.TrimSpace(nl.Lesson), Source: memory.LessonFromSelf}) {
					changes = append(changes, "new lesson: "+strings.TrimSpace(nl.Lesson))
				}
			}
		}
	}
	changes = append(changes, m.lookBack(ctx, looked, out.Outcomes, now)...)
	for _, pr := range out.Principles {
		text := strings.TrimSpace(pr.Text)
		var types, keys []string
		for _, t := range pr.Types {
			if slices.Contains(self.DecisionTypes, t) && !slices.Contains(types, t) {
				types = append(types, t)
			}
		}
		for _, id := range pr.Calls {
			if k, ok := callCases[strings.TrimSpace(id)]; ok {
				keys = append(keys, k)
			}
		}
		if text == "" || len(keys) == 0 { // a principle comes from its owner's own choices
			continue
		}
		if err := m.Store.SavePrinciple(ctx, memory.Principle{ID: ulid.Make(), Text: text, Types: types, Cases: keys,
			Status: memory.JudgmentProposed, Source: memory.JudgmentFromReview, CreatedAt: now.UTC()}); err != nil {
			m.Log.Error("save principle", "err", err)
			continue
		}
		changes = append(changes, "proposed a principle: "+text)
	}
	for _, dc := range out.Dreams {
		if change := m.wantDreamChange(ctx, activeDreams, dc.Action, strings.TrimSpace(dc.DreamID), strings.TrimSpace(dc.Text),
			strings.TrimSpace(dc.Why), now); change != "" {
			changes = append(changes, change)
		}
	}

	w := memory.Work{ID: ulid.Make(), Kind: "review", Title: orDefault(out.Title, "Week of "+now.In(m.Zone).Format("2 Jan")),
		Text: strings.TrimSpace(out.Text), CreatedAt: now.UTC()}
	if err := m.Store.SaveWork(ctx, w); err != nil {
		m.Log.Error("save review", "err", err)
	}
	d.Reply = w.Title + "\n\n" + w.Text
	detail := "reviewed the week"
	if len(changes) > 0 {
		detail += "; " + strings.Join(changes, "; ")
	}
	m.finishLife(ctx, d, start, decision.OutcomeOK, detail)
	m.Log.Info("weekly review", "changes", len(changes), "cost_usd", d.CostUSD)
	m.EnsurePlans(ctx, now) // a learning plan for each new skill goal
	return nil
}

// wantDreamChange is the review wanting to add, reword or drop a dream: done
// now, asked of the owner first, or not at all, as the autonomy gate says.
// It returns what happened, for the log; "" if nothing.
func (m *Mind) wantDreamChange(ctx context.Context, active map[string]memory.Dream, act, id, text, why string, now time.Time) string {
	old, known := active[id]
	switch {
	case act == "add" && (text == "" || len(active) >= maxActiveDreams):
		return ""
	case (act == "reword" || act == "drop") && !known:
		return ""
	case act == "reword" && (text == "" || text == old.Text):
		return ""
	case act != "add" && act != "reword" && act != "drop":
		return ""
	}
	a := memory.Approval{Class: string(action.Dream), Kind: act, Text: text, What: dreamChangeText(act, old.Text, text, why)}
	if known {
		a.Ref = old.ID
	}
	switch m.Autonomy.Check(action.Dream, "") {
	case action.Forbid:
		return ""
	case action.Ask:
		if err := m.ask(ctx, a); err != nil {
			m.Log.Error("asking the owner failed", "err", err)
			return ""
		}
		return "asked to " + a.What
	}
	m.changeDream(ctx, a, now)
	return a.What
}

func dreamChangeText(act, old, text, why string) string {
	what := map[string]string{
		"add":    fmt.Sprintf("add a dream: %s", text),
		"reword": fmt.Sprintf("change my dream %q to %q", old, text),
		"drop":   fmt.Sprintf("let go of my dream %q", old),
	}[act]
	if why != "" {
		what += ". Why: " + why
	}
	return what
}

// changeDream adds, rewords or drops a dream, as approved.
func (m *Mind) changeDream(ctx context.Context, a memory.Approval, now time.Time) string {
	dreams, err := m.Store.Dreams(ctx, true)
	if err != nil {
		return "changing the dream failed: " + err.Error()
	}
	var old *memory.Dream
	for i := range dreams {
		if dreams[i].ID == a.Ref {
			old = &dreams[i]
		}
	}
	switch a.Kind {
	case "add":
		if len(dreams) >= maxActiveDreams {
			return "too many dreams to add one"
		}
		err = m.Store.SaveDream(ctx, memory.Dream{ID: ulid.Make(), Text: a.Text, Status: memory.DreamActive, Source: memory.DreamFromSelf, Since: now.UTC()})
	case "reword":
		if old == nil {
			return "that dream is gone"
		}
		old.Text = a.Text
		err = m.Store.SaveDream(ctx, *old)
	case "drop":
		if old == nil {
			return "that dream is gone"
		}
		old.Status, old.EndedAt = memory.DreamDropped, now.UTC()
		err = m.Store.SaveDream(ctx, *old)
	}
	if err != nil {
		return "changing the dream failed: " + err.Error()
	}
	m.Log.Info("dream changed", "change", a.Kind, "text", a.Text)
	return "dream: " + a.Kind
}
