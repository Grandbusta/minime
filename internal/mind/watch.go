package mind

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/Grandbusta/minime/internal/ability"
	"github.com/Grandbusta/minime/internal/action"
	"github.com/Grandbusta/minime/internal/decision"
	"github.com/Grandbusta/minime/internal/memory"
	"github.com/Grandbusta/minime/internal/perception"
	"github.com/Grandbusta/minime/internal/providers/system2"
)

// Watching: a reading tool looked at on a schedule (an inbox, a feed). Each
// item not seen before is something it perceives and decides about: most
// need nothing; it may remember something, tell its owner, or propose an
// action, which always asks the owner, since what it saw is untrusted.

// Watcher is a tool looked at on a schedule (tools.Watcher).
type Watcher interface {
	Name() string
	Every() time.Duration
	Look(ctx context.Context, at time.Time) ([]memory.Watched, error)
}

const (
	observationsPerTick = 5 // more wait for the next look
	observedChars       = 6000
)

// watch looks with each watcher whose time has come. The first look at a
// watcher only records what is there already, so it doesn't start by
// noticing a whole inbox.
func (m *Mind) watch(ctx context.Context, now time.Time) {
	if m.policy().Check(action.UseTools, "") == action.Forbid {
		return
	}
	if m.looked == nil {
		m.looked = map[string]time.Time{}
	}
	noticed := 0
	for _, w := range m.Watchers {
		if last, ok := m.looked[w.Name()]; ok && now.Sub(last) < w.Every() {
			continue
		}
		m.looked[w.Name()] = now
		log := m.Log.With("watch", w.Name())
		items, err := w.Look(ctx, now)
		if err != nil {
			log.Warn("looking failed; trying again at its next time", "err", err)
			continue
		}
		source := "watch:" + w.Name()
		seen, err := m.Store.SeenFrom(ctx, source)
		if err != nil {
			log.Error("checking what it has seen failed", "err", err)
			continue
		}
		for _, it := range items {
			if seen && noticed >= observationsPerTick {
				break // the rest are still new at the next look
			}
			var before string
			if it.Whole {
				if before, _, err = m.Store.LastFrom(ctx, source); err != nil {
					log.Error("loading the last look failed", "err", err)
				}
			}
			p := perception.Percept{ID: ulid.Make(), Source: source, ExternalID: it.ID, Kind: perception.KindObservation,
				Text: cutRunes(it.Text, observedChars), At: now.UTC()}
			isNew, err := m.Store.SavePercept(ctx, p)
			if err != nil {
				log.Error("save percept", "err", err)
				return
			}
			if isNew && seen {
				m.observe(ctx, w.Name(), p, changed(before, p.Text), now)
				noticed++
			}
		}
		if !seen {
			log.Info("first look: what's there now is taken as seen", "items", len(items))
		}
	}
}

// observe decides about p; changed, if not "", is how p differs from what
// the last look saw.
func (m *Mind) observe(ctx context.Context, from string, p perception.Percept, changed string, now time.Time) {
	if m.Day.ObservePrompt.Text == "" {
		return
	}
	start := time.Now()
	d := newDecision(p, nil, start)
	d.Tier, d.SetID, d.Action = decision.TierSystem2, m.Day.ObservePrompt.Ref, "observe"
	owner := orDefault(m.person(ctx, m.policy().Owner).Name, "your owner")
	note := fmt.Sprintf("%s\n\nSomething new from %s, which you watch.%s", m.now(now), from, ability.Fence(p.Text))
	if changed != "" {
		note = fmt.Sprintf("%s\n\n%s changed since you last looked. What changed (+ new lines, - lines gone):%s\n\nAll of it now:%s",
			m.now(now), from, ability.Fence(changed), ability.Fence(p.Text))
	}
	acts := len(m.doers()) > 0
	if acts {
		note += "\n\n" + m.actionsHelp(owner)
	}
	resp, err := m.System2.Reason(ctx, system2.Request{
		Instructions: m.lifeInstructions(m.Day.ObservePrompt),
		Note:         note,
		Format:       observeFormat(acts),
	})
	if err != nil {
		m.finishLife(ctx, d, start, decision.OutcomeFailed, err.Error())
		return
	}
	d.Provider, d.CostUSD = resp.Provider, resp.CostUSD
	var out struct {
		Remember []string      `json:"remember"`
		Tell     string        `json:"tell"`
		Act      []system2.Act `json:"act"`
	}
	if err := json.Unmarshal([]byte(resp.Text), &out); err != nil {
		m.finishLife(ctx, d, start, decision.OutcomeFailed, "no answer: "+err.Error())
		return
	}
	n := names{agent: m.Life.Name, speaker: fallbackName}
	kept := 0
	for _, f := range out.Remember {
		if f = strings.TrimSpace(f); f == "" {
			continue
		}
		cost, err := m.saveFact(ctx, p, memory.SubjectSelf, f, n)
		d.CostUSD += cost
		if err != nil {
			m.Log.Error("save fact", "err", err)
			continue
		}
		kept++
	}
	if t := strings.TrimSpace(out.Tell); t != "" {
		m.tellOwner(ctx, perception.KindObservation, "watch:"+from, t, true, start)
	}
	m.propose(ctx, p, out.Act, now, false)
	m.finishLife(ctx, d, start, decision.OutcomeOK, fmt.Sprintf("remembered %d, told the owner: %v, proposed %d",
		kept, strings.TrimSpace(out.Tell) != "", len(out.Act)))
}

func observeFormat(acts bool) *system2.Format {
	props := map[string]any{
		"remember": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"tell":     map[string]any{"type": "string", "description": "a short message to your owner, or empty"},
	}
	required := []string{"remember", "tell"}
	if acts {
		props["act"] = map[string]any{"type": "array", "items": system2.ActSchema}
		required = append(required, "act")
	}
	return &system2.Format{Name: "observe", Schema: map[string]any{"type": "object", "additionalProperties": false,
		"required": required, "properties": props}}
}

// changed is the lines of now not in before (+) and of before not in now
// (-); "" if there was no before.
func changed(before, now string) string {
	if before == "" {
		return ""
	}
	count := func(s string) map[string]int {
		c := map[string]int{}
		for _, l := range strings.Split(s, "\n") {
			if l = strings.TrimSpace(l); l != "" {
				c[l]++
			}
		}
		return c
	}
	was, is := count(before), count(now)
	var out []string
	for _, l := range strings.Split(now, "\n") {
		if l = strings.TrimSpace(l); l != "" && is[l] > was[l] {
			out = append(out, "+ "+l)
			was[l]++
		}
	}
	for _, l := range strings.Split(before, "\n") {
		if l = strings.TrimSpace(l); l != "" && was[l] > is[l] {
			out = append(out, "- "+l)
			is[l]++
		}
	}
	return strings.Join(out, "\n")
}
