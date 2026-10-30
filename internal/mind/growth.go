package mind

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"minime/internal/decision"
	"minime/internal/memory"
	"minime/internal/perception"
	"minime/internal/providers/system1"
	"minime/internal/providers/system2"
)

// Growth: who the agent wants to become (its dreams, and the goals that
// serve them) and how good it is getting (its skills). Its creator seeds them
// in its life story; after that they are its own. A skill's level comes from
// evidence, never from what it claims: System 1 scores each work that shows
// the skill (skill_level), and its owner's feedback counts double. Dreams and
// goals change in its weekly review (review.go).

const (
	// evidenceWindow is how many recent scores a skill's level averages.
	evidenceWindow = 10
	// ownerWeight is how much more the owner's feedback counts than a score.
	ownerWeight = 2
	// scoreChars is how much of a work System 1 reads to score it.
	scoreChars = 4000
)

// bornWanting copies the dreams and skills in its life story into memory the
// first time: dreams if it has none yet, and each skill it doesn't have. After
// that memory wins: it grows.
func (m *Mind) bornWanting(ctx context.Context) {
	now := time.Now().UTC()
	if len(m.Life.Dreams) > 0 {
		dreams, err := m.Store.Dreams(ctx, false)
		if err != nil {
			m.Log.Warn("loading dreams failed", "err", err)
		} else if len(dreams) == 0 {
			for _, text := range m.Life.Dreams {
				d := memory.Dream{ID: ulid.Make(), Text: text, Status: memory.DreamActive, Source: memory.DreamFromLife, Since: now}
				if err := m.Store.SaveDream(ctx, d); err != nil {
					m.Log.Warn("storing a dream failed", "err", err)
					continue
				}
				m.Log.Info("born wanting", "dream", text)
			}
		}
	}
	for _, sk := range m.Life.Skills {
		if err := m.Store.AddSkill(ctx, memory.Skill{Name: sk.Name, Level: float64(sk.Level), ShownBy: sk.ShownBy,
			UpdatedAt: now}); err != nil {
			m.Log.Warn("storing a skill failed", "skill", sk.Name, "err", err)
			continue
		}
		m.startingScore(ctx, sk.Name, float64(sk.Level))
	}
}

// startingScore counts the level its life story starts a skill at as its
// first score, once, so the first real score moves its level instead of
// replacing it. It goes before any evidence the skill already has.
func (m *Mind) startingScore(ctx context.Context, skill string, level float64) {
	ev, err := m.Store.RecentEvidence(ctx, skill, 1000)
	if err != nil {
		return
	}
	at := time.Now().UTC()
	for _, e := range ev {
		if e.Source == memory.EvidenceLife {
			return
		}
		if e.At.Before(at) {
			at = e.At.Add(-time.Second)
		}
	}
	if err := m.Store.SaveEvidence(ctx, memory.Evidence{ID: ulid.Make(), Skill: skill, Score: level, Source: memory.EvidenceLife,
		Note: "where its life story starts it", At: at}); err != nil {
		m.Log.Warn("storing a starting score failed", "skill", skill, "err", err)
		return
	}
	if len(ev) > 0 { // it already had scores: work its level out again with this one
		if lvl, err := m.recomputeLevel(ctx, skill, time.Now().UTC()); err == nil {
			m.Log.Info("counted a skill's starting level", "skill", skill, "level", lvl)
		}
	}
}

// scoreWork has System 1 score a work it just made as evidence of each skill
// it shows (by its kind), and updates those skills' levels. parent is the
// decision that made the work.
func (m *Mind) scoreWork(ctx context.Context, w memory.Work, percept ulid.ULID, parent *ulid.ULID) {
	if m.SkillLevel.ID == "" || len(m.SkillLevel.Questions) == 0 {
		return
	}
	skills, err := m.Store.Skills(ctx)
	if err != nil {
		m.Log.Warn("loading skills failed; the work isn't scored", "err", err)
		return
	}
	text := w.Text
	if r := []rune(text); len(r) > scoreChars {
		text = string(r[:scoreChars])
	}
	for _, sk := range skills {
		if !slices.Contains(sk.ShownBy, w.Kind) {
			continue
		}
		start := time.Now()
		d := &decision.Decision{ID: ulid.Make(), ParentID: parent, At: start.UTC(), PerceptID: percept, Tier: decision.TierSystem1,
			SetID: m.SkillLevel.Ref(), Action: "score_skill", Gate: decision.GateAuto}
		resp, err := m.System1.Decide(ctx, system1.Request{
			State:     map[string]any{"skill": sk.Name, "work": map[string]string{"title": w.Title, "kind": w.Kind, "text": text}},
			Questions: m.SkillLevel.Questions,
		})
		if err != nil {
			m.Log.Warn("scoring a work failed", "skill", sk.Name, "err", err)
			continue
		}
		d.Provider, d.Answers, d.CostUSD = resp.Provider, resp.Answers, resp.CostUSD
		a, ok := resp.Answers[m.SkillLevel.Questions[0].Name]
		if !ok || a.Refused {
			continue
		}
		score := math.Round((a.Score+1)*10) / 10 // level index 0-9 → 1-10
		level, err := m.addEvidence(ctx, memory.Evidence{Skill: sk.Name, WorkID: w.ID, Score: score, Source: memory.EvidenceSystem1,
			Note: w.Title})
		if err != nil {
			m.Log.Error("save evidence", "err", err)
			continue
		}
		d.Latency = time.Since(start)
		d.Outcome = &decision.Outcome{Status: decision.OutcomeOK, At: time.Now().UTC(),
			Detail: fmt.Sprintf("%s: %q scored %.1f; level %.1f → %.1f", sk.Name, w.Title, score, sk.Level, level)}
		if err := m.Store.SaveDecision(ctx, *d); err != nil {
			m.Log.Error("save decision", "err", err)
		}
		m.Log.Info("scored a work", "skill", sk.Name, "work", w.Title, "score", score, "level", level)
	}
}

// addEvidence saves a score of a skill and returns the skill's new level: the
// weighted average of its recent scores, the owner's counting double.
func (m *Mind) addEvidence(ctx context.Context, e memory.Evidence) (float64, error) {
	e.ID, e.At = ulid.Make(), time.Now().UTC()
	if err := m.Store.SaveEvidence(ctx, e); err != nil {
		return 0, err
	}
	return m.recomputeLevel(ctx, e.Skill, e.At)
}

// recomputeLevel works a skill's level out from its recent scores and saves it.
func (m *Mind) recomputeLevel(ctx context.Context, skill string, at time.Time) (float64, error) {
	recent, err := m.Store.RecentEvidence(ctx, skill, evidenceWindow)
	if err != nil {
		return 0, err
	}
	sum, weight := 0.0, 0.0
	for _, r := range recent {
		w := 1.0
		if r.Source == memory.EvidenceOwner {
			w = ownerWeight
		}
		sum, weight = sum+r.Score*w, weight+w
	}
	if weight == 0 {
		return 0, nil
	}
	level := math.Round(sum/weight*10) / 10
	return level, m.Store.SetSkillLevel(ctx, skill, level, at)
}

// takeFeedback counts its owner's judgement of something it made toward the
// skill it shows. Only the owner's counts, and only for a skill it has.
func (m *Mind) takeFeedback(ctx context.Context, p perception.Percept, reply *decision.Decision, fs []system2.Feedback) []*decision.Decision {
	if len(fs) == 0 || p.Speaker == "" || p.Speaker != m.Autonomy.Owner {
		return nil
	}
	skills, err := m.Store.Skills(ctx)
	if err != nil {
		return nil
	}
	asked, _, _ := m.Store.AskedForReview(ctx, p.At.Add(-reviewAnswerWithin)) // what they're likely judging
	var out []*decision.Decision
	for _, f := range fs {
		name := ""
		for _, sk := range skills {
			if strings.EqualFold(sk.Name, f.Skill) {
				name = sk.Name
			}
		}
		if name == "" {
			m.Log.Warn("feedback on a skill it doesn't have", "skill", f.Skill)
			continue
		}
		level, err := m.addEvidence(ctx, memory.Evidence{Skill: name, WorkID: asked.ID, Score: float64(f.Level), Source: memory.EvidenceOwner,
			Note: f.About})
		if err != nil {
			m.Log.Error("save feedback", "err", err)
			continue
		}
		d := m.child(p, reply, "skill_feedback")
		d.Outcome.Detail = fmt.Sprintf("%s: the owner said %d (%s); level now %.1f", name, f.Level, f.About, level)
		out = append(out, d)
	}
	return out
}

// feedbackHelp tells System 2, talking with its owner, how to pass on what
// they think of its work; "" for anyone else, or with no skills.
func (m *Mind) feedbackHelp(ctx context.Context, person memory.Person) string {
	if person.Key == "" || person.Key != m.Autonomy.Owner {
		return ""
	}
	skills, err := m.Store.Skills(ctx)
	if err != nil || len(skills) == 0 {
		return ""
	}
	names := make([]string, len(skills))
	for i, sk := range skills {
		names[i] = sk.Name
	}
	return "\nWhat they think of your work:\n- If they judge something you made (\"that verse was weak\", \"this beat idea is fire\"), put it in `feedback`: " +
		"the skill it shows (one of: " + strings.Join(names, ", ") + "), the level 1-10 their words mean (weak 2-3, okay 4-5, good 6, " +
		"great 7-8, amazing 9), and what they judged. Any comment on your work counts, even one that's only advice: \"too wordy, cut it\" " +
		"says it was about a 3. Only when they're judging your work. Take it like a person would.\n" +
		"- If they give you specific advice about doing it better (\"cut the hook to six words\", \"handle the error\"), also put each piece in " +
		"`lessons`: the skill, and the lesson as a short rule for next time that will still help on your next piece (\"keep hooks to about " +
		"six punchy words\", not \"cut this one\"), in your words. Thank them like a learner would.\n" +
		"- If they say what they like or don't in work like yours, beyond how good it is (\"I love when a hook repeats one word\", " +
		"\"I'm not into long intros\"), put each in `tastes`: the kind of work it's about (one of: " + strings.Join(m.craftKinds(), ", ") +
		"; empty for any), likes true or false, and the taste in general terms, so it still applies to your next piece (\"likes hooks " +
		"built on one repeated word\"). Their taste is theirs: keep it apart from how good the work was.\n"
}

// craftKinds are the kinds of work it makes.
func (m *Mind) craftKinds() []string {
	var kinds []string
	for _, c := range m.Life.Crafts() {
		kinds = append(kinds, c.Kind)
	}
	return kinds
}

// growthNote is who it wants to become, for a plan or a review: its dreams,
// its active goals and its skills.
func (m *Mind) growthNote(ctx context.Context, now time.Time) string {
	var b strings.Builder
	if dreams, err := m.Store.Dreams(ctx, true); err == nil && len(dreams) > 0 {
		b.WriteString("\n\nWho you want to become (your dreams):")
		for _, d := range dreams {
			fmt.Fprintf(&b, "\n- [dream %s] %s", d.ID, d.Text)
		}
	}
	if goals, err := m.Store.Goals(ctx, true); err == nil && len(goals) > 0 {
		b.WriteString("\n\nYour goals now:")
		for _, g := range goals {
			fmt.Fprintf(&b, "\n- [goal %s] %s (done when: %s; due %s; skill: %s). Where it stands: %s", g.ID, g.Text, g.DoneWhen,
				g.Due.In(m.Zone).Format("Mon 2 Jan"), orDefault(g.Skill, "none named yet"), orDefault(g.Progress, "just started"))
		}
	} else if err == nil {
		b.WriteString("\n\nYou have no goals yet.")
	}
	if skills, err := m.Store.Skills(ctx); err == nil && len(skills) > 0 {
		b.WriteString("\n\nYour skills (1-10, from what you've made and feedback):")
		for _, sk := range skills {
			fmt.Fprintf(&b, "\n- %s: %.1f", sk.Name, sk.Level)
			if ev, err := m.Store.RecentEvidence(ctx, sk.Name, 3); err == nil && len(ev) > 0 {
				var last []string
				for _, e := range ev {
					last = append(last, fmt.Sprintf("%.1f", e.Score))
				}
				fmt.Fprintf(&b, " (latest scores, newest first: %s)", strings.Join(last, ", "))
			}
		}
	}
	return b.String()
}

// goalsHelp tells System 2 how to take on a goal its owner asks for: its
// dreams and skills by name, and how many more goals it has room for.
func (m *Mind) goalsHelp(ctx context.Context, person memory.Person) string {
	if person.Key == "" || person.Key != m.Autonomy.Owner {
		return ""
	}
	var b strings.Builder
	b.WriteString("\nGoals they ask you to take on:\n- If they ask you to learn something or take on a goal (\"add that to your goals\", " +
		"\"you should learn sales\") and you agree, put it in `goals`, so it really becomes yours: you'll plan how to learn it and " +
		"practise it in your days. The skill it grows (one of yours by its exact name, or a new one, with level your honest starting " +
		"level 1-10: a beginner is 1-2), the goal, done_when (a concrete test from what you'll make), due (YYYY-MM-DD, " + m.goalDue() + "), and " +
		"the dream it serves (dream_id), or new_dream (third person with your name) if none fits. Only say you've added it if you " +
		"put it in `goals`. The goal is to learn it yourself, not to make them a plan.\n")
	if dreams, err := m.Store.Dreams(ctx, true); err == nil && len(dreams) > 0 {
		b.WriteString("  Your dreams:")
		for _, d := range dreams {
			fmt.Fprintf(&b, " [%s] %s;", d.ID, d.Text)
		}
		b.WriteString("\n")
	}
	if skills, err := m.Store.Skills(ctx); err == nil && len(skills) > 0 {
		names := make([]string, len(skills))
		for i, sk := range skills {
			names[i] = fmt.Sprintf("%s (%.1f)", sk.Name, sk.Level)
		}
		fmt.Fprintf(&b, "  Your skills: %s.\n", strings.Join(names, ", "))
	}
	if goals, err := m.Store.Goals(ctx, true); err == nil {
		if len(goals) > 0 {
			b.WriteString("  Your goals now (already yours: never add one again):")
			for _, g := range goals {
				fmt.Fprintf(&b, " %s (%s);", g.Text, orDefault(g.Skill, "no skill"))
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}

// takeGoals takes on goals its owner asked for: the skill (new ones start
// where it honestly is), the dream it serves (a new one if none fits), and
// the goal itself, set now. Its learning plan follows on the next tick.
func (m *Mind) takeGoals(ctx context.Context, p perception.Percept, reply *decision.Decision, gs []system2.GoalTaken) []*decision.Decision {
	if len(gs) == 0 || p.Speaker == "" || p.Speaker != m.Autonomy.Owner {
		return nil
	}
	now := time.Now().UTC()
	var out []*decision.Decision
	for _, gt := range gs {
		d := m.child(p, reply, "take_goal")
		d.Outcome.Detail = m.takeGoal(ctx, gt, now)
		out = append(out, d)
		m.Log.Info("took on a goal its owner asked for", "goal", gt.Text, "skill", gt.Skill, "detail", d.Outcome.Detail)
	}
	return out
}

// takeGoal takes on one goal, and returns what it did, for the log.
func (m *Mind) takeGoal(ctx context.Context, gt system2.GoalTaken, now time.Time) string {
	goals, err := m.Store.Goals(ctx, true)
	if err != nil {
		return "loading goals failed: " + err.Error()
	}
	for _, g := range goals {
		if strings.EqualFold(strings.TrimSpace(g.Text), strings.TrimSpace(gt.Text)) {
			return "already a goal: " + g.Text
		}
	}
	due, err := time.ParseInLocation("2006-01-02", gt.Due, m.Zone)
	if err != nil || !due.After(now) || due.Sub(now) > goalHorizon {
		due = now.Add(m.paced(28 * 24 * time.Hour)).In(m.Zone) // four person-weeks, when the date it gave isn't one
	}
	dreams, err := m.Store.Dreams(ctx, true)
	if err != nil {
		return "loading dreams failed: " + err.Error()
	}
	var dream ulid.ULID
	for _, d := range dreams {
		if d.ID.String() == gt.DreamID {
			dream = d.ID
		}
	}
	if dream.IsZero() {
		text := gt.NewDream
		if text == "" {
			text = fmt.Sprintf("%s wants to get good at %s.", m.Life.Name, gt.Skill)
		}
		if len(dreams) >= maxActiveDreams {
			dream = dreams[len(dreams)-1].ID // no room for another: the newest it has
		} else {
			d := memory.Dream{ID: ulid.Make(), Text: text, Status: memory.DreamActive, Source: memory.DreamFromOwner, Since: now}
			if err := m.Store.SaveDream(ctx, d); err != nil {
				return "saving the dream failed: " + err.Error()
			}
			dream = d.ID
		}
	}
	skill := m.skillNamed(ctx, gt.Skill)
	if skill == "" {
		level := max(1, min(10, gt.Level))
		skill = gt.Skill
		if err := m.Store.AddSkill(ctx, memory.Skill{Name: skill, Level: float64(level), ShownBy: []string{"notes", "writing", "reading_notes"}, UpdatedAt: now}); err != nil {
			return "adding the skill failed: " + err.Error()
		}
		m.startingScore(ctx, skill, float64(level))
	}
	g := memory.Goal{ID: ulid.Make(), DreamID: dream, Text: gt.Text, Skill: skill, DoneWhen: gt.DoneWhen,
		Due: time.Date(due.Year(), due.Month(), due.Day(), 23, 59, 0, 0, m.Zone).UTC(), Status: memory.GoalActive, CreatedAt: now, UpdatedAt: now}
	if err := m.Store.SaveGoal(ctx, g); err != nil {
		return "saving the goal failed: " + err.Error()
	}
	m.toPlan = true
	return fmt.Sprintf("goal set (%s): %s", skill, g.Text)
}
