package mind

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Grandbusta/minime/internal/memory"
	"github.com/Grandbusta/minime/internal/perception"
	"github.com/Grandbusta/minime/internal/providers/system1"
	"github.com/Grandbusta/minime/internal/self"
)

// classifyRelation is "" if the person_circle set is not configured.
func (m *Mind) classifyRelation(ctx context.Context, relation string) (string, float64, error) {
	if m.PersonCircle.ID == "" {
		return "", 0, nil
	}
	n := names{agent: m.Life.Name}
	qs := slices.Clone(m.PersonCircle.Questions)
	for i := range qs {
		qs[i].Instructions = n.fill(qs[i].Instructions)
		qs[i].Choices = slices.Clone(qs[i].Choices)
		for j := range qs[i].Choices {
			qs[i].Choices[j].Description = n.fill(qs[i].Choices[j].Description)
		}
	}
	resp, err := m.System1.Decide(ctx, system1.Request{State: map[string]any{"relation": relation}, Questions: qs})
	if err != nil {
		return "", 0, err
	}
	a, ok := resp.Answers[m.PersonCircle.Primary]
	if !ok || a.Refused {
		return "other", resp.CostUSD, nil
	}
	return a.Choice, resp.CostUSD, nil
}

// circleFamily is given to everyone in the life story's family list, with no
// model call.
const circleFamily = "family"

// bornKnowing adds each relative once: a relative already known is never
// changed by the file again.
func (m *Mind) bornKnowing(ctx context.Context) {
	for _, r := range m.Life.Family {
		key := r.Account
		if key == "" {
			key = familyKey(r)
		}
		now := time.Now().UTC()
		added, err := m.Store.AddPersonIfNew(ctx, memory.Person{
			Key: key, Name: r.Name, Relation: fmt.Sprintf("%s is %s's %s.", r.Name, m.Life.Name, r.Relation),
			Circle: orDefault(r.Circle, circleFamily), Source: memory.KnownFromBirth, FirstSeen: now, UpdatedAt: now,
		})
		if err != nil {
			m.Log.Warn("storing someone it was born knowing failed", "person", key, "err", err)
			continue
		}
		if added {
			m.Log.Info("born knowing", "person", key, "name", r.Name, "relation", r.Relation)
		}
	}
}

// familyKey is for a relative who never talks to the agent.
func familyKey(r self.Relative) string {
	return "family:" + strings.ToLower(r.Relation) + ":" + strings.ToLower(r.Name)
}

func (m *Mind) meet(ctx context.Context, p perception.Percept) memory.Person {
	stranger := memory.Person{Key: p.Speaker, Name: p.SpeakerName, Circle: memory.CircleStranger,
		Source: memory.KnownByMeeting, FirstSeen: p.At, UpdatedAt: p.At}
	if p.Speaker == "" {
		return stranger
	}
	person, known, err := m.Store.Person(ctx, p.Speaker)
	if err != nil {
		m.Log.Warn("loading person failed; treating them as new", "person", p.Speaker, "err", err)
		return stranger
	}
	switch {
	case !known:
		person = stranger
		m.Log.Info("met someone new", "person", p.Speaker, "name", p.SpeakerName)
	case person.Name == "" && p.SpeakerName != "":
		person.Name, person.UpdatedAt = p.SpeakerName, p.At // born knowing them, now they have a name
	default:
		return person
	}
	if err := m.Store.SavePerson(ctx, person); err != nil {
		m.Log.Warn("saving person failed", "person", p.Speaker, "err", err)
	}
	return person
}

// learnRelation never overwrites what the agent was born knowing. It returns
// what classifying cost.
func (m *Mind) learnRelation(ctx context.Context, p perception.Percept, person memory.Person, relation string) float64 {
	if relation == "" || person.Key == "" || relation == person.Relation {
		return 0
	}
	if person.Source == memory.KnownFromBirth {
		m.Log.Info("ignored a claim against what is known for certain", "person", person.Key, "claim", relation)
		return 0
	}
	circle, cost, err := m.classifyRelation(ctx, relation)
	if err != nil {
		m.Log.Warn("classifying person failed; storing without a circle", "person", person.Key, "err", err)
	}
	person.Relation, person.Circle, person.Source, person.UpdatedAt = relation, circle, memory.KnownByTelling, p.At
	if err := m.Store.SavePerson(ctx, person); err != nil {
		m.Log.Error("saving person failed", "person", person.Key, "err", err)
		return cost
	}
	m.Log.Info("learned who someone is", "person", person.Key, "relation", relation, "circle", circle)
	return cost
}

// whoTheyAre states only what the agent was born knowing as certain.
func (m *Mind) whoTheyAre(ctx context.Context, person memory.Person, n names) string {
	var b strings.Builder
	switch {
	case person.Relation != "" && person.Source == memory.KnownFromBirth:
		fmt.Fprintf(&b, "This is certain: %s", person.Relation)
	case person.Relation != "":
		fmt.Fprintf(&b, "%s told you who they are: %s", n.speaker, person.Relation)
	default:
		fmt.Fprintf(&b, "You first met %s on %s and don't know yet who they are to you. Don't assume more than they tell you.",
			n.speaker, person.FirstSeen.Format("2 Jan 2006"))
	}
	known, err := m.Store.KnownPeople(ctx, 50)
	if err != nil {
		m.Log.Warn("loading known people failed", "err", err)
		return b.String()
	}
	if len(known) > 0 {
		b.WriteString("\n\nPeople you know, by circle:")
		for _, k := range known {
			name := k.Name
			if name == "" {
				name = "(name not known yet)"
			}
			fmt.Fprintf(&b, "\n- %s, %s: %s", name, orUnknown(k.Circle), k.Relation)
		}
	}
	return b.String()
}

func orUnknown(s string) string {
	if s == "" {
		return "circle unknown"
	}
	return s
}
