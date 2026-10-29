// Package journal is the agent writing its journal at night: a short, honest
// look back at its day, from what it planned, how that went and what it made.
package journal

import (
	"context"
	"fmt"
	"strings"
	"time"

	"minime/internal/ability"
	"minime/internal/memory"
)

// Name is the journal's ability; its intention is planned every day.
const Name = "journal"

// Store is what it looks back at.
type Store interface {
	IntentionsDue(ctx context.Context, from, to time.Time) ([]memory.Intention, error)
	WorksBetween(ctx context.Context, from, to time.Time) ([]memory.Work, error)
}

type Journal struct {
	Store Store
}

func (Journal) Name() string { return Name }

// Describe: the planner doesn't choose it; the mind plans it every day.
func (Journal) Describe() string { return "" }

func (Journal) Risk() ability.Risk { return ability.Private }

// Prepare gives what today held: what it planned and how it went, and what it made.
func (j Journal) Prepare(ctx context.Context, a ability.Act) (ability.Material, error) {
	var b strings.Builder
	b.WriteString("\n\nIt's evening: write today's journal entry, a short honest look back at your day, in your own voice. Use title for the date and a few words.")
	l := a.Now.In(a.Zone)
	midnight := time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, a.Zone)
	if is, err := j.Store.IntentionsDue(ctx, midnight, a.Now); err == nil && len(is) > 0 {
		b.WriteString("\n\nWhat you planned today and how it went:")
		for _, i := range is {
			if i.Subject != memory.SubjectSelf || i.Ability == Name {
				continue
			}
			fmt.Fprintf(&b, "\n- %s %s: %s", i.DueAt.In(a.Zone).Format("15:04"), i.Text, i.Status)
		}
	}
	if ws, err := j.Store.WorksBetween(ctx, midnight, a.Now); err == nil && len(ws) > 0 {
		b.WriteString("\n\nWhat you made today:")
		for _, w := range ws {
			fmt.Fprintf(&b, "\n- %s: %s", w.Kind, w.Title)
		}
	}
	return ability.Material{Note: b.String(), Project: a.Project, WorkKind: "journal"}, nil
}
