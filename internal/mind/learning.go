package mind

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/Grandbusta/minime/internal/decision"
	"github.com/Grandbusta/minime/internal/memory"
	"github.com/Grandbusta/minime/internal/providers/system2"
)

// Learning plans: its weekly review moves a plan on to the next stage only
// when the stage's "done when" is met by what it actually made.

const (
	minStages         = 3
	maxStages         = 5
	resourcesPerStage = 3
)

var learnFormat = &system2.Format{Name: "learning_plan", Schema: map[string]any{
	"type": "object", "additionalProperties": false, "required": []string{"stages"},
	"properties": map[string]any{
		"stages": map[string]any{"type": "array", "items": map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"learn", "practice", "done_when", "resources"},
			"properties": map[string]any{
				"learn":     map[string]any{"type": "string"},
				"practice":  map[string]any{"type": "string"},
				"done_when": map[string]any{"type": "string"},
				"resources": map[string]any{"type": "array", "items": map[string]any{
					"type": "object", "additionalProperties": false, "required": []string{"ability", "query", "why"},
					"properties": map[string]any{
						"ability": map[string]any{"type": "string"},
						"query":   map[string]any{"type": "string"},
						"why":     map[string]any{"type": "string"},
					},
				}},
			},
		}},
	},
}}

// EnsurePlans drafts a plan for each active skill goal that has none.
func (m *Mind) EnsurePlans(ctx context.Context, now time.Time) {
	if m.Day.LearnPrompt.Text == "" {
		return
	}
	goals, err := m.Store.Goals(ctx, true)
	if err != nil {
		return
	}
	plans, err := m.Store.Plans(ctx, false)
	if err != nil {
		return
	}
	has := map[ulid.ULID]bool{}
	for _, p := range plans {
		has[p.GoalID] = true
	}
	for _, g := range goals {
		if g.Skill != "" && !has[g.ID] {
			if err := m.draftPlan(ctx, g, now); err != nil {
				m.Log.Warn("drafting a learning plan failed; tried again next review", "goal", g.Text, "err", err)
			}
		}
	}
}

func (m *Mind) draftPlan(ctx context.Context, g memory.Goal, now time.Time) error {
	start := time.Now()
	d := &decision.Decision{ID: ulid.Make(), At: start.UTC(), Tier: decision.TierSystem2, SetID: m.Day.LearnPrompt.Ref,
		Action: "plan_learning", Gate: decision.GateAuto}
	var note strings.Builder
	note.WriteString(m.now(now))
	fmt.Fprintf(&note, "\n\nYour goal: %s (done when: %s; due %s). It grows your skill: %s.", g.Text, g.DoneWhen,
		g.Due.In(m.Zone).Format("Mon 2 Jan"), g.Skill)
	if skills, err := m.Store.Skills(ctx); err == nil {
		for _, sk := range skills {
			if sk.Name == g.Skill {
				fmt.Fprintf(&note, " You're at %.1f out of 10 in it now.", sk.Level)
			}
		}
	}
	if ls, err := m.Store.Lessons(ctx, g.Skill, 10); err == nil && len(ls) > 0 {
		note.WriteString("\n\nWhat you've learned about it so far:")
		for _, l := range ls {
			fmt.Fprintf(&note, "\n- %s", l.Text)
		}
	}
	fmt.Fprintf(&note, "\n\nWhat you can study with (resources must use one of these):\n%s", m.readingList())
	note.WriteString(m.judgmentNote(ctx, learnJudgment, fmt.Sprintf("Planning how to learn %s, for the goal: %s (done when: %s).",
		g.Skill, g.Text, g.DoneWhen)))
	resp, err := m.System2.Reason(ctx, system2.Request{
		Instructions: m.lifeInstructions(m.Day.LearnPrompt),
		Note:         note.String(),
		Format:       learnFormat,
	})
	if err != nil {
		m.finishLife(ctx, d, start, decision.OutcomeFailed, err.Error())
		return err
	}
	d.Provider, d.CostUSD = resp.Provider, resp.CostUSD
	var out struct {
		Stages []struct {
			Learn     string `json:"learn"`
			Practice  string `json:"practice"`
			DoneWhen  string `json:"done_when"`
			Resources []struct {
				Ability string `json:"ability"`
				Query   string `json:"query"`
				Why     string `json:"why"`
			} `json:"resources"`
		} `json:"stages"`
	}
	if err := json.Unmarshal([]byte(resp.Text), &out); err != nil {
		m.finishLife(ctx, d, start, decision.OutcomeFailed, "no plan in the answer: "+err.Error())
		return err
	}
	p := memory.LearningPlan{ID: ulid.Make(), GoalID: g.ID, Skill: g.Skill, Status: memory.PlanActive, CreatedAt: now.UTC(), UpdatedAt: now.UTC()}
	var resources []memory.Resource
	for _, st := range out.Stages {
		learn, practice := strings.TrimSpace(st.Learn), strings.TrimSpace(st.Practice)
		if learn == "" || practice == "" || len(p.Stages) >= maxStages {
			continue
		}
		n := len(p.Stages)
		p.Stages = append(p.Stages, memory.Stage{N: n, Learn: learn, Practice: practice, DoneWhen: strings.TrimSpace(st.DoneWhen)})
		for _, r := range st.Resources {
			if res, ok := m.resource(p.ID, n, r.Ability, r.Query, r.Why, now); ok && countStage(resources, n) < resourcesPerStage {
				resources = append(resources, res)
			}
		}
	}
	if len(p.Stages) < minStages {
		m.finishLife(ctx, d, start, decision.OutcomeFailed, fmt.Sprintf("a plan needs %d to %d stages, got %d", minStages, maxStages, len(p.Stages)))
		return fmt.Errorf("plan has %d stages", len(p.Stages))
	}
	if err := m.Store.SavePlan(ctx, p); err != nil {
		return err
	}
	for _, r := range resources {
		if err := m.Store.SaveResource(ctx, r); err != nil {
			m.Log.Error("save resource", "err", err)
		}
	}
	var lines []string
	for _, st := range p.Stages {
		lines = append(lines, fmt.Sprintf("%d. %s (practice: %s)", st.N+1, st.Learn, st.Practice))
	}
	d.Reply = "Learning plan for " + g.Skill + ":\n" + strings.Join(lines, "\n")
	m.finishLife(ctx, d, start, decision.OutcomeOK, fmt.Sprintf("planned how to learn %s: %d stages, %d resources", g.Skill, len(p.Stages), len(resources)))
	m.Log.Info("planned how to learn", "skill", g.Skill, "goal", g.Text, "stages", len(p.Stages), "resources", len(resources))
	return nil
}

// resource is false unless an ability it has can read it.
func (m *Mind) resource(plan ulid.ULID, stage int, ability, query, why string, now time.Time) (memory.Resource, bool) {
	ability, query = strings.TrimSpace(ability), strings.TrimSpace(query)
	ab, ok := m.Abilities.Get(ability)
	if !ok || query == "" || ab.Describe() == "" || !m.allowed(ab) {
		return memory.Resource{}, false
	}
	return memory.Resource{ID: ulid.Make(), PlanID: plan, Stage: stage, Ability: ab.Name(), Query: query, Why: strings.TrimSpace(why),
		Status: memory.ResourceTodo, AddedAt: now.UTC()}, true
}

func countStage(rs []memory.Resource, stage int) int {
	n := 0
	for _, r := range rs {
		if r.Stage == stage {
			n++
		}
	}
	return n
}

func (m *Mind) readingList() string {
	var lines []string
	for _, a := range m.plannable() {
		lines = append(lines, "- "+a.Name()+": "+a.Describe())
	}
	if len(lines) == 0 {
		return "- (none: you can only practise, and learn from feedback)"
	}
	return strings.Join(lines, "\n")
}

func (m *Mind) plansNote(ctx context.Context, forReview bool) string {
	plans, err := m.Store.Plans(ctx, true)
	if err != nil || len(plans) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nYour learning plans:")
	for _, p := range plans {
		if p.Stage >= len(p.Stages) {
			continue
		}
		st := p.Stages[p.Stage]
		fmt.Fprintf(&b, "\n- [plan %s] %s (goal %s), stage %d of %d: learn %s. Practise: %s. Done when: %s.", p.ID, p.Skill, p.GoalID,
			p.Stage+1, len(p.Stages), st.Learn, st.Practice, orDefault(st.DoneWhen, "you're confident"))
		rs, err := m.Store.Resources(ctx, p.ID)
		if err != nil {
			continue
		}
		for _, r := range rs {
			if r.Stage != p.Stage {
				continue
			}
			if r.Status == memory.ResourceDone {
				fmt.Fprintf(&b, "\n  - studied: %s", orDefault(r.Title, r.Query))
				continue
			}
			fmt.Fprintf(&b, "\n  - to study: [resource %s] %s %q (%s)", r.ID, r.Ability, r.Query, orDefault(r.Why, "for this stage"))
		}
		if forReview && p.Stage+1 < len(p.Stages) {
			fmt.Fprintf(&b, "\n  Next stage: %s.", p.Stages[p.Stage+1].Learn)
		}
	}
	return b.String()
}

func (m *Mind) studied(ctx context.Context, i memory.Intention, w memory.Work, now time.Time) {
	raw := i.Args["resource"]
	if raw == "" {
		return
	}
	id, err := ulid.Parse(raw)
	if err != nil {
		return
	}
	if err := m.Store.FinishResource(ctx, id, w.ID, w.Title, now.UTC()); err != nil {
		m.Log.Error("finish resource", "err", err)
	}
}

func (m *Mind) resourceFor(ctx context.Context, id string) (memory.Resource, bool) {
	plans, err := m.Store.Plans(ctx, true)
	if err != nil {
		return memory.Resource{}, false
	}
	for _, p := range plans {
		rs, err := m.Store.Resources(ctx, p.ID)
		if err != nil {
			continue
		}
		for _, r := range rs {
			if r.ID.String() == id && r.Status == memory.ResourceTodo {
				return r, true
			}
		}
	}
	return memory.Resource{}, false
}

func (m *Mind) skillNamed(ctx context.Context, name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	skills, err := m.Store.Skills(ctx)
	if err != nil {
		return ""
	}
	for _, sk := range skills {
		if strings.EqualFold(sk.Name, name) {
			return sk.Name
		}
	}
	return ""
}

func (m *Mind) foundNote(ctx context.Context) string {
	ab, ok := m.Abilities.Get("read_page")
	if !ok || !m.allowed(ab) {
		return ""
	}
	found, err := m.Store.RecentFound(ctx, 8)
	if err != nil || len(found) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\nPages your web searches found, which you can read with read_page (their address as query):")
	for _, r := range found {
		fmt.Fprintf(&b, "\n- %s: %s", r.Title, r.URL)
	}
	return b.String()
}

func (m *Mind) stagesNote(ctx context.Context) string {
	plans, err := m.Store.Plans(ctx, true)
	if err != nil {
		return ""
	}
	var b strings.Builder
	for _, p := range plans {
		if p.Stage >= len(p.Stages) {
			continue
		}
		st := p.Stages[p.Stage]
		fmt.Fprintf(&b, "\n- [plan %s] %s, stage %d of %d: practise %s Done when: %s.", p.ID, p.Skill, p.Stage+1, len(p.Stages),
			strings.TrimSuffix(st.Practice, ".")+".", strings.TrimSuffix(orDefault(st.DoneWhen, "you're confident"), "."))
	}
	if b.Len() == 0 {
		return ""
	}
	return "\n\nWhere your learning plans are. If what you make now really meets a stage's \"done when\", give that plan's id as " +
		"stage_done and you move on to the next stage; be strict, a start isn't done. Otherwise leave it empty:" + b.String()
}

func (m *Mind) stageDone(ctx context.Context, planID string, now time.Time) string {
	if planID == "" {
		return ""
	}
	plans, err := m.Store.Plans(ctx, true)
	if err != nil {
		return ""
	}
	for _, p := range plans {
		if p.ID.String() != planID || p.Stage >= len(p.Stages) {
			continue
		}
		if err := m.Store.AdvancePlan(ctx, p.ID, now.UTC()); err != nil {
			m.Log.Error("advance plan", "err", err)
			return ""
		}
		m.Log.Info("finished a learning stage", "skill", p.Skill, "stage", p.Stage+1, "of", len(p.Stages))
		if p.Stage+1 >= len(p.Stages) {
			return fmt.Sprintf("finished the last stage of learning %s", p.Skill)
		}
		return fmt.Sprintf("finished stage %d of %d of learning %s; next: %s", p.Stage+1, len(p.Stages), p.Skill, p.Stages[p.Stage+1].Learn)
	}
	return ""
}
