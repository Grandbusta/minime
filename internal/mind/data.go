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
	"github.com/Grandbusta/minime/internal/render"
)

// dataOf is, for each kind of work it makes that carries data beside its
// words, the format that defines that data.
func (m *Mind) dataOf() map[string]render.DataFormat {
	m.dataOnce.Do(func() {
		m.dataFormats = map[string]render.DataFormat{}
		for _, c := range m.Life.Crafts() {
			if f, ok := m.Formats.Data(c.Data); ok && c.Data != "" {
				m.dataFormats[c.Kind] = f
			}
		}
	})
	return m.dataFormats
}

// dataKinds is the data its works carry, in the formats' order, with the
// kinds of work that carry each.
func (m *Mind) dataKinds() ([]render.DataFormat, map[string][]string) {
	var fs []render.DataFormat
	carriers := map[string][]string{}
	for _, kind := range m.Formats.DataKinds() {
		f, _ := m.Formats.Data(kind)
		for _, c := range m.Life.Crafts() {
			if m.dataOf()[c.Kind] == f {
				carriers[kind] = append(carriers[kind], c.Kind)
			}
		}
		if len(carriers[kind]) > 0 {
			fs = append(fs, f)
		}
	}
	return fs, carriers
}

// dataSchemas is each kind of data its works carry, for the activity format.
func (m *Mind) dataSchemas() map[string]map[string]any {
	fs, _ := m.dataKinds()
	out := map[string]map[string]any{}
	for _, f := range fs {
		out[f.Needs()] = f.DataSchema()
	}
	return out
}

func (m *Mind) dataHelp() string {
	fs, carriers := m.dataKinds()
	var b strings.Builder
	for _, f := range fs {
		fmt.Fprintf(&b, "\n\nIf what you make is a %s, also write its %s: %s Its text still says, in a line or two, what it is. "+
			"If it isn't one, leave the %s with nothing in it.", strings.Join(carriers[f.Needs()], " or "), f.Needs(), f.DataHelp(), f.Needs())
	}
	return b.String()
}

// keptData is the data a work of kind carries, cleaned, from what System 2
// answered; nil if kind carries none or nothing in it can be used.
func (m *Mind) keptData(kind, answer string) (json.RawMessage, string) {
	f, ok := m.dataOf()[kind]
	if !ok {
		return nil, ""
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal([]byte(answer), &all); err != nil || len(all[f.Needs()]) == 0 {
		return nil, ""
	}
	data, err := f.CleanData(all[f.Needs()])
	if err != nil {
		m.Log.Warn("what it made came without data that can be used", "kind", kind, "data", f.Needs(), "err", err)
		return nil, ""
	}
	return data, f.Needs()
}

// fillOldData writes, each tick, the data of one work made before its kind
// carried any (music made before scores), so it can be made into its file
// without keeping a reply waiting on System 2.
func (m *Mind) fillOldData(ctx context.Context) {
	if m.Day.DataPrompt.Text == "" || len(m.dataOf()) == 0 {
		return
	}
	kinds := make([]string, 0, len(m.dataOf()))
	for k := range m.dataOf() {
		kinds = append(kinds, k)
	}
	ws, err := m.Store.WorksMissingData(ctx, kinds, 20)
	if err != nil {
		m.Log.Warn("loading works to fill in failed", "err", err)
		return
	}
	for _, w := range ws {
		if m.unfillable[w.ID] {
			continue
		}
		if _, err := m.writeData(ctx, w); err != nil {
			if m.unfillable == nil {
				m.unfillable = map[ulid.ULID]bool{}
			}
			m.unfillable[w.ID] = true
		}
		return
	}
}

// writeData writes, from its words, the data w's kind carries, and keeps it
// on the work.
func (m *Mind) writeData(ctx context.Context, w memory.Work) (json.RawMessage, error) {
	f, ok := m.dataOf()[w.Kind]
	if !ok {
		return nil, fmt.Errorf("a %s carries no data", w.Kind)
	}
	start := time.Now()
	d := &decision.Decision{ID: ulid.Make(), At: start.UTC(), Tier: decision.TierSystem2, SetID: m.Day.DataPrompt.Ref,
		Action: "write_data", Gate: decision.GateAuto}
	resp, err := m.System2.Reason(ctx, system2.Request{
		Instructions: m.lifeInstructions(m.Day.DataPrompt),
		Note: fmt.Sprintf("What you made, a %s, on %s:\n\n%s\n\nWrite its %s: %s", strings.ReplaceAll(w.Kind, "_", " "),
			w.CreatedAt.In(m.Zone).Format("Monday 2 January"), titled(w), f.Needs(), f.DataHelp()),
		Format: &system2.Format{Name: "data", Schema: map[string]any{"type": "object", "additionalProperties": false,
			"required": []string{f.Needs()}, "properties": map[string]any{f.Needs(): f.DataSchema()}}},
	})
	if err != nil {
		m.finishLife(ctx, d, start, decision.OutcomeFailed, err.Error())
		return nil, err
	}
	d.Provider, d.CostUSD = resp.Provider, resp.CostUSD
	data, kind := m.keptData(w.Kind, resp.Text)
	if data == nil {
		m.finishLife(ctx, d, start, decision.OutcomeFailed, "no "+f.Needs()+" that can be used")
		return nil, render.ErrNoData
	}
	if err := m.Store.SetWorkData(ctx, w.ID, kind, data); err != nil {
		m.Log.Error("keeping the data failed", "work", w.ID.String(), "err", err)
	}
	m.finishLife(ctx, d, start, decision.OutcomeOK, fmt.Sprintf("wrote the %s of %q", kind, w.Title))
	m.Log.Info("wrote the data of something made before", "work", w.Title, "data", kind, "cost_usd", d.CostUSD)
	return data, nil
}
