package midi

import (
	"encoding/json"
	"strings"

	"github.com/Grandbusta/minime/internal/memory"
)

type Format struct{}

func (Format) Name() string      { return "midi" }
func (Format) Ext() string       { return "mid" }
func (Format) MediaType() string { return "audio/midi" }
func (Format) Describe() string  { return "a MIDI file that plays in any music app" }
func (Format) WriteHelp() string { return "" }
func (Format) Needs() string     { return "score" }

func (Format) Render(w memory.Work, _ string) ([]byte, error) { return Render(w.Title, w.Data) }

func (Format) DataHelp() string {
	return "every note of it as you describe it, placed by bar and beat, each part on its own track, so it can be played " +
		"back exactly. Pitches are note names with an octave, C4 being middle C; on drums, the drum's name. Score the bars " +
		"you describe, at most 16; if it repeats, its loop. The words and the score must agree: the same tempo, key, chords and parts."
}

func (Format) CleanData(raw json.RawMessage) (json.RawMessage, error) {
	s, err := Parse(raw)
	if err != nil {
		return nil, err
	}
	return json.Marshal(s)
}

var scoreSchema = func() map[string]any {
	note := map[string]any{"type": "object", "additionalProperties": false,
		"required": []string{"pitch", "bar", "beat", "length", "velocity"},
		"properties": map[string]any{
			"pitch": map[string]any{"type": "string", "description": "a note name with its octave, C4 being middle C (F#3, Bb2); " +
				"on drums, one of: " + strings.Join(Drums(), ", ")},
			"bar":      map[string]any{"type": "integer", "description": "from 1"},
			"beat":     map[string]any{"type": "number", "description": "from 1; 2.5 is the \"and\" of 2"},
			"length":   map[string]any{"type": "number", "description": "in beats"},
			"velocity": map[string]any{"type": "integer", "description": "1 to 127"},
		}}
	track := map[string]any{"type": "object", "additionalProperties": false, "required": []string{"name", "instrument", "notes"},
		"properties": map[string]any{
			"name":       map[string]any{"type": "string"},
			"instrument": map[string]any{"type": "string", "enum": Instruments()},
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
}()

func (Format) DataSchema() map[string]any { return scoreSchema }
