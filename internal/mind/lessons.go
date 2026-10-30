package mind

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/Grandbusta/minime/internal/decision"
	"github.com/Grandbusta/minime/internal/memory"
	"github.com/Grandbusta/minime/internal/perception"
	"github.com/Grandbusta/minime/internal/providers/system2"
	"github.com/Grandbusta/minime/internal/render"
)

// Lessons: specific advice about doing a skill well ("check the error after
// every file read"). Active ones go into every practice note until its weekly
// review retires them.

const (
	lessonsPerSkill = 3
	lessonsPerWork  = 2
	askReviewEvery  = 24 * time.Hour
	// reviewAnswerWithin is how long after asking an owner's feedback is tied to that work.
	reviewAnswerWithin = 48 * time.Hour
)

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

// learnFromWork files lessons under skills; with none, under each skill the
// work's kind shows.
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

// askForReview asks at most once a day. Messaging the owner needs no approval.
func (m *Mind) askForReview(ctx context.Context, w memory.Work, question string, now time.Time) {
	owner := m.policy().Owner
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
	s := speech{action: "ask_review", note: note, attachment: titled(w), trim: firstParagraph}
	if f, err := m.makeFile(render.MIDI, w); err != nil { // music, so they can hear it, not only read it
		m.Log.Warn("making its MIDI failed; asking with the words alone", "work", w.Title, "err", err)
	} else {
		s.file = f
	}
	if got := m.speakFirst(ctx, p, person, nil, s); got == sent {
		if err := m.Store.MarkReviewAsked(ctx, w.ID, now.UTC()); err != nil {
			m.Log.Error("mark review asked", "err", err)
		}
	}
}

// takeLessons ties advice to the work it last asked the owner to review, if
// any.
func (m *Mind) takeLessons(ctx context.Context, p perception.Percept, reply *decision.Decision, ls []system2.LessonLearned) []*decision.Decision {
	if len(ls) == 0 || p.Speaker == "" || p.Speaker != m.policy().Owner {
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

const tastesInNote = 12

// takeTastes: only the owner's taste counts; a kind it doesn't make counts
// for any.
func (m *Mind) takeTastes(ctx context.Context, p perception.Percept, reply *decision.Decision, ts []system2.Taste) []*decision.Decision {
	if len(ts) == 0 || p.Speaker == "" || p.Speaker != m.policy().Owner {
		return nil
	}
	asked, _, _ := m.Store.AskedForReview(ctx, p.At.Add(-reviewAnswerWithin))
	var out []*decision.Decision
	for _, t := range ts {
		kind := t.Kind
		if !slices.Contains(m.craftKinds(), kind) {
			kind = ""
		}
		taste := memory.Taste{ID: ulid.Make(), Kind: kind, Likes: t.Likes, Text: t.Text, WorkID: asked.ID, Source: memory.TasteFromTelegram,
			At: time.Now().UTC()}
		if err := m.Store.SaveTaste(ctx, taste); err != nil {
			m.Log.Error("save taste", "err", err)
			continue
		}
		d := m.child(p, reply, "taste")
		d.Outcome.Detail = tasteLine(taste)
		out = append(out, d)
		m.Log.Info("learned its owner's taste", "kind", kind, "likes", t.Likes, "taste", t.Text)
	}
	return out
}

func (m *Mind) tasteNote(ctx context.Context) string {
	ts, err := m.Store.Tastes(ctx, tastesInNote)
	if err != nil || len(ts) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n\nWhat %s likes and doesn't in your work (their taste: weigh it when it applies, beside what makes it good):", m.ownerName(ctx))
	for _, t := range ts {
		fmt.Fprintf(&b, "\n- %s", tasteLine(t))
	}
	return b.String()
}

func tasteLine(t memory.Taste) string {
	line := "likes"
	if !t.Likes {
		line = "doesn't like"
	}
	if t.Kind != "" {
		line = strings.ReplaceAll(t.Kind, "_", " ") + ", " + line
	}
	return line + ": " + t.Text
}
