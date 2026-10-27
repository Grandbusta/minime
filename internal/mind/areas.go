package mind

import (
	"context"
	"slices"

	"github.com/oklog/ulid/v2"

	"minime/internal/memory"
	"minime/internal/perception"
	"minime/internal/providers/system1"
)

// noArea marks a fact that was labelled but fits no life area, so it is not
// labelled again on every start.
const noArea = "other"

// lifeLabels is how System 1 filed a text, the way a person files what they
// hear: the life areas it is about and, for a message, whether it concerns
// the agent's or the speaker's own life, or one of them in general ("Tell me
// about yourself", "What do you know about me?").
type lifeLabels struct {
	Areas    []string // most likely first; never contains noArea
	Personal bool
	Broad    bool
}

// labelText asks System 1 which life areas text is about. text must already
// name people (see names); the set's {agent} and {speaker} are filled from n.
// It needs a configured life_area set; check m.LifeArea.ID first.
func (m *Mind) labelText(ctx context.Context, text string, n names) (lifeLabels, system1.Response, error) {
	qs := slices.Clone(m.LifeArea.Questions)
	for i := range qs {
		qs[i].Instructions = n.fill(qs[i].Instructions)
	}
	resp, err := m.System1.Decide(ctx, system1.Request{State: map[string]any{"text": text}, Questions: qs})
	if err != nil {
		return lifeLabels{}, resp, err
	}
	l := lifeLabels{
		Personal: isYes(m.LifeArea, resp.Answers, "personal"),
		Broad:    isYes(m.LifeArea, resp.Answers, "broad"),
	}
	for _, a := range m.LifeArea.Labels(resp.Answers, "area") {
		if a != noArea {
			l.Areas = append(l.Areas, a)
		}
	}
	return l, resp, nil
}

// factAreas is what gets stored on a fact: its areas, or noArea if none.
func factAreas(l lifeLabels) []string {
	if len(l.Areas) == 0 {
		return []string{noArea}
	}
	return slices.Clone(l.Areas)
}

// saveFact labels text with its life areas and stores it as a fact about
// subject (memory.SubjectSelf or a person's key), heard in p. If labelling
// fails the fact is still saved, unlabelled, and labelled on the next start.
// It returns what labelling cost.
func (m *Mind) saveFact(ctx context.Context, p perception.Percept, subject, text string, n names) (float64, error) {
	f := memory.Fact{ID: ulid.Make(), PerceptID: p.ID, Subject: subject, Text: text, At: p.At}
	var cost float64
	if m.LifeArea.ID != "" {
		l, resp, err := m.labelText(ctx, n.fact(f), n)
		if err != nil {
			m.Log.Warn("labelling fact failed; saving it unlabelled", "percept", p.ID.String(), "err", err)
		} else {
			f.Areas, cost = factAreas(l), resp.CostUSD
		}
	}
	if err := m.Store.SaveFact(ctx, f); err != nil {
		return cost, err
	}
	m.Log.Info("remembered", "percept", p.ID.String(), "fact", f.ID.String(), "subject", subject, "areas", f.Areas)
	return cost, nil
}

// labelUnlabelledFacts files facts saved before life areas existed, or whose
// labelling failed, so recall can find them by area. Speakers' names are not
// known at start, so a person's facts are labelled with fallbackName.
func (m *Mind) labelUnlabelledFacts(ctx context.Context) {
	if m.LifeArea.ID == "" {
		return
	}
	facts, err := m.Store.UnlabelledFacts(ctx, 100)
	if err != nil {
		m.Log.Warn("loading unlabelled facts failed", "err", err)
		return
	}
	n := names{agent: m.Life.Name, speaker: fallbackName}
	for _, f := range facts {
		l, _, err := m.labelText(ctx, n.fact(f), n)
		if err != nil {
			m.Log.Warn("labelling fact failed", "fact", f.ID.String(), "err", err)
			continue
		}
		if err := m.Store.SetFactAreas(ctx, f.ID, factAreas(l)); err != nil {
			m.Log.Warn("saving fact areas failed", "fact", f.ID.String(), "err", err)
			continue
		}
		m.Log.Info("labelled fact", "fact", f.ID.String(), "areas", factAreas(l))
	}
}
