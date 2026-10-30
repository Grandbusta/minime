package mind

import (
	"context"
	"slices"

	"github.com/oklog/ulid/v2"

	"github.com/Grandbusta/minime/internal/memory"
	"github.com/Grandbusta/minime/internal/perception"
	"github.com/Grandbusta/minime/internal/providers/system1"
)

// noArea marks a fact that was labelled but fits no life area, so it is not
// labelled again on every start.
const noArea = "other"

// lifeLabels: Personal is whether a message concerns the agent's or the
// speaker's own life; Broad, one of them in general ("Tell me about yourself").
type lifeLabels struct {
	Areas    []string // most likely first; never contains noArea
	Personal bool
	Broad    bool
}

// labelText needs text that already names people (see names) and a
// configured life_area set; check m.LifeArea.ID first.
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

func factAreas(l lifeLabels) []string {
	if len(l.Areas) == 0 {
		return []string{noArea}
	}
	return slices.Clone(l.Areas)
}

// saveFact still saves the fact if labelling fails; it is labelled on the
// next start. It returns what labelling cost.
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

// labelUnlabelledFacts labels a person's facts with fallbackName, since
// speakers' names are not known at start.
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
