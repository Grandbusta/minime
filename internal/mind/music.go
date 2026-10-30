package mind

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/Grandbusta/minime/internal/decision"
	"github.com/Grandbusta/minime/internal/memory"
	"github.com/Grandbusta/minime/internal/providers/system2"
	"github.com/Grandbusta/minime/internal/render/midi"
)

func (m *Mind) scored() []string {
	m.scoredOnce.Do(func() { m.scoredKinds = m.Life.Carrying(memory.DataScore) })
	return m.scoredKinds
}

func (m *Mind) isMusic(kind string) bool { return slices.Contains(m.scored(), kind) }

// scoreSchema is render/midi's Score, for Structured Outputs.
func scoreSchema() map[string]any {
	note := map[string]any{"type": "object", "additionalProperties": false,
		"required": []string{"pitch", "bar", "beat", "length", "velocity"},
		"properties": map[string]any{
			"pitch": map[string]any{"type": "string", "description": "a note name with its octave, C4 being middle C (F#3, Bb2); " +
				"on drums, one of: " + strings.Join(midi.Drums(), ", ")},
			"bar":      map[string]any{"type": "integer", "description": "from 1"},
			"beat":     map[string]any{"type": "number", "description": "from 1; 2.5 is the \"and\" of 2"},
			"length":   map[string]any{"type": "number", "description": "in beats"},
			"velocity": map[string]any{"type": "integer", "description": "1 to 127"},
		}}
	track := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"name", "instrument", "notes"},
		"properties": map[string]any{
			"name":       map[string]any{"type": "string"},
			"instrument": map[string]any{"type": "string", "enum": midi.Instruments()},
			"notes":      map[string]any{"type": "array", "items": note},
		}}
	return map[string]any{"type": "object", "additionalProperties": false,
		"required": []string{"bpm", "beats_per_bar", "bars", "tracks"},
		"properties": map[string]any{
			"bpm":           map[string]any{"type": "number"},
			"beats_per_bar": map[string]any{"type": "integer"},
			"bars":          map[string]any{"type": "integer"},
			"tracks":        map[string]any{"type": "array", "items": track},
		}}
}

func (m *Mind) scoreHelp() string {
	kinds := m.scored()
	if len(kinds) == 0 {
		return ""
	}
	return fmt.Sprintf("\n\nIf what you make is a %s, also write its score: every note of it as you describe it, placed by bar "+
		"and beat, each part on its own track, so it can be played back exactly. Score the bars you describe, at most 16; if it "+
		"repeats, its loop. The words and the score must agree: the same tempo, key, chords and parts. If it isn't music, "+
		"leave the score's tracks empty.", strings.Join(kinds, " or "))
}

// keptScore is score cleaned, for a work of kind; nil if kind isn't music
// or nothing in it can be played.
func (m *Mind) keptScore(kind string, score json.RawMessage) json.RawMessage {
	if !m.isMusic(kind) || len(score) == 0 {
		return nil
	}
	s, err := midi.Parse(score)
	if err != nil {
		m.Log.Warn("its music came without a score that can be played", "kind", kind, "err", err)
		return nil
	}
	out, _ := json.Marshal(s)
	return out
}

// scoreOldMusic scores one piece of music made before scores each tick, so
// it can be sent as MIDI without keeping a reply waiting on System 2.
func (m *Mind) scoreOldMusic(ctx context.Context) {
	if m.Day.ScorePrompt.Text == "" || len(m.scored()) == 0 {
		return
	}
	ws, err := m.Store.UnscoredWorks(ctx, m.scored(), 20)
	if err != nil {
		m.Log.Warn("loading music to score failed", "err", err)
		return
	}
	for _, w := range ws {
		if m.unscorable[w.ID] {
			continue
		}
		if _, err := m.writeScore(ctx, w); err != nil {
			if m.unscorable == nil {
				m.unscorable = map[ulid.ULID]bool{}
			}
			m.unscorable[w.ID] = true
		}
		return
	}
}

// writeScore writes, from its text, the score of music made before scores,
// and keeps it on the work.
func (m *Mind) writeScore(ctx context.Context, w memory.Work) (json.RawMessage, error) {
	start := time.Now()
	d := &decision.Decision{ID: ulid.Make(), At: start.UTC(), Tier: decision.TierSystem2, SetID: m.Day.ScorePrompt.Ref,
		Action: "score_work", Gate: decision.GateAuto}
	resp, err := m.System2.Reason(ctx, system2.Request{
		Instructions: m.lifeInstructions(m.Day.ScorePrompt),
		Note:         fmt.Sprintf("The piece, a %s you made on %s:\n\n%s", strings.ReplaceAll(w.Kind, "_", " "), w.CreatedAt.In(m.Zone).Format("Monday 2 January"), titled(w)),
		Format: &system2.Format{Name: "score", Schema: map[string]any{"type": "object", "additionalProperties": false,
			"required": []string{"score"}, "properties": map[string]any{"score": scoreSchema()}}},
	})
	if err != nil {
		m.finishLife(ctx, d, start, decision.OutcomeFailed, err.Error())
		return nil, err
	}
	d.Provider, d.CostUSD = resp.Provider, resp.CostUSD
	var out struct {
		Score json.RawMessage `json:"score"`
	}
	score := json.RawMessage(nil)
	if err := json.Unmarshal([]byte(resp.Text), &out); err == nil {
		score = m.keptScore(w.Kind, out.Score)
	}
	if score == nil {
		m.finishLife(ctx, d, start, decision.OutcomeFailed, "no score that can be played")
		return nil, midi.ErrEmpty
	}
	if err := m.Store.SetWorkScore(ctx, w.ID, score); err != nil {
		m.Log.Error("keeping the score failed", "work", w.ID.String(), "err", err)
	}
	m.finishLife(ctx, d, start, decision.OutcomeOK, fmt.Sprintf("scored %q", w.Title))
	m.Log.Info("scored music made before scores", "work", w.Title, "cost_usd", d.CostUSD)
	return score, nil
}
