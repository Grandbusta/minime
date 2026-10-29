package mind

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"minime/internal/decision"
	"minime/internal/memory"
	"minime/internal/perception"
	"minime/internal/providers/system2"
)

// Lessons: learning from correction. A lesson is specific advice about doing
// a skill well ("check the error after every file read"), from its owner's
// correction, its own failures, or studying others. Active lessons go into
// every practice note, so the next attempt is made with them in mind; its
// weekly review retires the ones it has absorbed. It may also ask its owner
// to review what it made, at most once a day.

const (
	// lessonsPerSkill is how many of a skill's lessons go into a practice note.
	lessonsPerSkill = 3
	// lessonsPerWork is the most lessons it takes from one work of its own.
	lessonsPerWork = 2
	// askReviewEvery is how often it may ask its owner to review its work.
	askReviewEvery = 24 * time.Hour
	// reviewAnswerWithin is how long after asking an owner's feedback is tied to that work.
	reviewAnswerWithin = 48 * time.Hour
)

// lessonsNote is what it has learned the hard way, for a practice note:
// each skill's newest active lessons.
func (m *Mind) lessonsNote(ctx context.Context) string {
	skills, err := m.Store.Skills(ctx)
	if err != nil || len(skills) == 0 {
		return ""
	}
	var b strings.Builder
	for _, sk := range skills {
		ls, err := m.Store.Lessons(ctx, sk.Name, lessonsPerSkill)
		if err != nil || len(ls) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n%s:", sk.Name)
		for _, l := range ls {
			fmt.Fprintf(&b, "\n- %s", l.Text)
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return "\n\nThings you've learned the hard way (use them, when they apply):" + b.String()
}

// learnFromWork keeps the lessons it took from a work of its own: from its
// own mistakes, or from studying others when the work is notes on what it
// read (source). They go under the skill it was working on (skills); with
// none, under each skill the work's kind shows.
func (m *Mind) learnFromWork(ctx context.Context, w memory.Work, lessons []string, source string, skills []string) {
	if len(skills) == 0 {
		all, err := m.Store.Skills(ctx)
		if err != nil {
			return
		}
		for _, sk := range all {
			if slices.Contains(sk.ShownBy, w.Kind) {
				skills = append(skills, sk.Name)
			}
		}
	}
	n := 0
	for _, text := range lessons {
		if text = strings.TrimSpace(text); text == "" || n >= lessonsPerWork {
			continue
		}
		n++
		for _, sk := range skills {
			m.saveLesson(ctx, memory.Lesson{Skill: sk, Text: text, Source: source, WorkID: w.ID})
		}
	}
}

// workingOn is the skill an activity works on: its learning plan's (the
// resource it studies), or its project's goal's; "" if neither says.
func (m *Mind) workingOn(ctx context.Context, i memory.Intention, project *memory.Project) string {
	if id := i.Args["resource"]; id != "" {
		if plans, err := m.Store.Plans(ctx, false); err == nil {
			for _, p := range plans {
				if rs, err := m.Store.Resources(ctx, p.ID); err == nil {
					for _, r := range rs {
						if r.ID.String() == id {
							return p.Skill
						}
					}
				}
			}
		}
	}
	if project != nil && !project.GoalID.IsZero() {
		if goals, err := m.Store.Goals(ctx, false); err == nil {
			for _, g := range goals {
				if g.ID == project.GoalID {
					return g.Skill
				}
			}
		}
	}
	return ""
}

func (m *Mind) saveLesson(ctx context.Context, l memory.Lesson) bool {
	l.ID, l.At = ulid.Make(), time.Now().UTC()
	if err := m.Store.SaveLesson(ctx, l); err != nil {
		m.Log.Error("save lesson", "err", err)
		return false
	}
	m.Log.Info("learned a lesson", "skill", l.Skill, "source", l.Source, "lesson", l.Text)
	return true
}

// askForReview sends its owner a work it just made, with its question, at
// most once a day. Messaging the owner needs no approval.
func (m *Mind) askForReview(ctx context.Context, w memory.Work, question string, now time.Time) {
	owner := m.Autonomy.Owner
	if owner == "" {
		return
	}
	if last, ok, err := m.Store.LastSpokeFirst(ctx, owner, perception.KindReviewRequest); err != nil || (ok && now.Sub(last) < askReviewEvery) {
		return
	}
	conv, ok := m.conversationWith(ctx, owner)
	if !ok {
		return
	}
	person := m.person(ctx, owner)
	p := outgoing(conv, person, perception.KindReviewRequest, now)
	n := m.namesFor(p)
	note := fmt.Sprintf("You are messaging %s first: you just made this, and you'd like their honest eyes on it, the way a learner "+
		"asks a teacher. It goes right after your message, word for word:\n\n%s\n\nWrite only the one or two short sentences you "+
		"send with it: what it is, and your question: %s", n.speaker, titled(w), orDefault(question, "what would you do differently?"))
	if got := m.speakFirst(ctx, p, person, nil, speech{action: "ask_review", note: note, attachment: titled(w), trim: firstParagraph}); got == sent {
		if err := m.Store.MarkReviewAsked(ctx, w.ID, now.UTC()); err != nil {
			m.Log.Error("mark review asked", "err", err)
		}
	}
}

// takeLessons keeps the specific advice its owner gave about its work, tied
// to the work it last asked them to review, if any.
func (m *Mind) takeLessons(ctx context.Context, p perception.Percept, reply *decision.Decision, ls []system2.LessonLearned) []*decision.Decision {
	if len(ls) == 0 || p.Speaker == "" || p.Speaker != m.Autonomy.Owner {
		return nil
	}
	skills, err := m.Store.Skills(ctx)
	if err != nil {
		return nil
	}
	asked, _, _ := m.Store.AskedForReview(ctx, p.At.Add(-reviewAnswerWithin))
	var out []*decision.Decision
	for _, l := range ls {
		name := ""
		for _, sk := range skills {
			if strings.EqualFold(sk.Name, l.Skill) {
				name = sk.Name
			}
		}
		if name == "" || !m.saveLesson(ctx, memory.Lesson{Skill: name, Text: l.Text, Source: memory.LessonFromOwner, WorkID: asked.ID}) {
			continue
		}
		d := m.child(p, reply, "lesson")
		d.Outcome.Detail = fmt.Sprintf("%s: %s", name, l.Text)
		out = append(out, d)
	}
	return out
}

func lessonSource(s string) string {
	switch s {
	case memory.LessonFromOwner:
		return "your owner's correction"
	case memory.LessonFromOthers:
		return "studying others"
	}
	return "your own mistakes"
}
