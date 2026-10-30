// Package midi writes a score (tracks of notes, placed by bar and beat) as
// a standard MIDI file that any DAW opens. A score is cleaned first: what
// can't be played is dropped and the rest kept inside the piece.
package midi

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	gomidi "gitlab.com/gomidi/midi/v2"
	"gitlab.com/gomidi/midi/v2/smf"
)

type Score struct {
	BPM         float64 `json:"bpm"`
	BeatsPerBar int     `json:"beats_per_bar"`
	Bars        int     `json:"bars"`
	Tracks      []Track `json:"tracks"`
}

type Track struct {
	Name       string `json:"name"`
	Instrument string `json:"instrument"` // one of Instruments
	Notes      []Note `json:"notes"`
}

type Note struct {
	Pitch    string  `json:"pitch"`    // "C4" (middle C), "F#3", "Bb2"; a drum's name on drums, e.g. "kick"
	Bar      int     `json:"bar"`      // from 1
	Beat     float64 `json:"beat"`     // from 1; 2.5 is the "and" of 2
	Length   float64 `json:"length"`   // in beats
	Velocity int     `json:"velocity"` // 1 to 127
}

const (
	ticksPerBeat = 480
	maxTracks    = 15
	maxNotes     = 4000
	minLength    = 1.0 / 32
)

// programs is the General MIDI program of each instrument but drums.
var programs = map[string]uint8{
	"piano": 0, "electric_piano": 4, "bells": 14, "organ": 16, "guitar": 25, "bass": 33, "synth_bass": 38,
	"strings": 48, "pluck": 45, "choir": 52, "brass": 61, "lead": 80, "pad": 89,
}

// drums is the General MIDI key of each drum.
var drums = map[string]uint8{
	"kick": 36, "rim": 37, "snare": 38, "clap": 39, "closed_hat": 42, "low_tom": 45, "pedal_hat": 44, "open_hat": 46,
	"mid_tom": 47, "crash": 49, "high_tom": 50, "ride": 51, "tambourine": 54, "cowbell": 56, "bongo": 60,
	"high_conga": 63, "low_conga": 64, "shaker": 82,
}

// Instruments are what a track can be played on.
func Instruments() []string { return sortedKeys(programs, "drums") }

// Drums are the names a note on drums can have.
func Drums() []string { return sortedKeys(drums) }

func sortedKeys[V any](m map[string]V, more ...string) []string {
	out := append([]string{}, more...)
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

var (
	notePitch   = regexp.MustCompile(`^([A-Ga-g])([#b]?)(-?\d)$`)
	steps       = map[string]int{"C": 0, "D": 2, "E": 4, "F": 5, "G": 7, "A": 9, "B": 11}
	accidentals = map[string]int{"#": 1, "b": -1}
)

// key is a note's MIDI key: a drum by name on drums, else a pitch like
// "F#3" (C4 is 60) or a number.
func key(pitch string, isDrums bool) (uint8, bool) {
	pitch = strings.TrimSpace(pitch)
	if isDrums {
		k, ok := drums[strings.ToLower(strings.ReplaceAll(pitch, " ", "_"))]
		return k, ok
	}
	if n, err := strconv.Atoi(pitch); err == nil {
		return uint8(n), n >= 0 && n <= 127
	}
	m := notePitch.FindStringSubmatch(pitch)
	if m == nil {
		return 0, false
	}
	n := steps[strings.ToUpper(m[1])] + accidentals[m[2]]
	octave, _ := strconv.Atoi(m[3])
	n += (octave + 1) * 12
	return uint8(n), n >= 0 && n <= 127
}

var ErrEmpty = errors.New("midi: the score has no notes that can be played")

func Parse(data []byte) (Score, error) {
	var s Score
	if err := json.Unmarshal(data, &s); err != nil {
		return Score{}, fmt.Errorf("midi: %w", err)
	}
	return s.Clean()
}

// Clean keeps what can be played: a tempo from 40 to 240 (else 90), 2 to 12
// beats a bar (else 4), known instruments and pitches, and notes inside the
// piece, cut at its end. Bars, if not given, is where the last note ends.
func (s Score) Clean() (Score, error) {
	out := Score{BPM: s.BPM, BeatsPerBar: s.BeatsPerBar, Bars: s.Bars}
	if out.BPM < 40 || out.BPM > 240 {
		out.BPM = 90
	}
	if out.BeatsPerBar < 2 || out.BeatsPerBar > 12 {
		out.BeatsPerBar = 4
	}
	bpb := float64(out.BeatsPerBar)
	if out.Bars <= 0 || out.Bars > 256 {
		out.Bars = 0
		for _, t := range s.Tracks {
			for _, n := range t.Notes {
				out.Bars = max(out.Bars, n.Bar+int(math.Ceil((n.Beat-1+n.Length)/bpb))-1)
			}
		}
		out.Bars = min(max(out.Bars, 1), 256)
	}
	end := float64(out.Bars) * bpb
	notes := 0
	for _, t := range s.Tracks {
		inst := strings.ToLower(strings.TrimSpace(t.Instrument))
		if _, ok := programs[inst]; !ok && inst != "drums" {
			continue
		}
		ct := Track{Name: strings.TrimSpace(t.Name), Instrument: inst}
		for _, n := range t.Notes {
			if _, ok := key(n.Pitch, inst == "drums"); !ok || n.Bar < 1 || n.Beat < 1 || n.Beat >= bpb+1 {
				continue
			}
			at := float64(n.Bar-1)*bpb + n.Beat - 1
			if at >= end || notes >= maxNotes {
				continue
			}
			n.Pitch = strings.TrimSpace(n.Pitch)
			n.Length = min(max(n.Length, minLength), end-at)
			if n.Velocity < 1 || n.Velocity > 127 {
				n.Velocity = 90
			}
			ct.Notes = append(ct.Notes, n)
			notes++
		}
		if len(ct.Notes) > 0 && len(out.Tracks) < maxTracks {
			out.Tracks = append(out.Tracks, ct)
		}
	}
	if len(out.Tracks) == 0 {
		return Score{}, ErrEmpty
	}
	return out, nil
}

type event struct {
	tick uint32
	off  bool
	msg  []byte
}

// Render writes a score, given as JSON and cleaned, as a type 1 MIDI file
// named title.
func Render(title string, score []byte) ([]byte, error) {
	s, err := Parse(score)
	if err != nil {
		return nil, err
	}
	f := smf.NewSMF1()
	f.TimeFormat = smf.MetricTicks(ticksPerBeat)

	var conductor smf.Track
	conductor.Add(0, smf.MetaTrackSequenceName(title))
	conductor.Add(0, smf.MetaMeter(uint8(s.BeatsPerBar), 4))
	conductor.Add(0, smf.MetaTempo(s.BPM))
	conductor.Close(0)
	if err := f.Add(conductor); err != nil {
		return nil, err
	}

	channel := uint8(0)
	for _, t := range s.Tracks {
		isDrums := t.Instrument == "drums"
		ch := uint8(9)
		if !isDrums {
			ch = channel
			if channel++; channel == 9 { // 9 is drums
				channel++
			}
		}
		var tr smf.Track
		tr.Add(0, smf.MetaTrackSequenceName(cmp.Or(t.Name, strings.ReplaceAll(t.Instrument, "_", " "))))
		tr.Add(0, smf.MetaInstrument(t.Instrument))
		if !isDrums {
			tr.Add(0, gomidi.ProgramChange(ch, programs[t.Instrument]))
		}
		var evs []event
		bpb := float64(s.BeatsPerBar)
		for _, n := range t.Notes {
			k, _ := key(n.Pitch, isDrums)
			start := uint32(math.Round((float64(n.Bar-1)*bpb + n.Beat - 1) * ticksPerBeat))
			stop := start + max(1, uint32(math.Round(n.Length*ticksPerBeat)))
			evs = append(evs, event{start, false, gomidi.NoteOn(ch, k, uint8(n.Velocity))}, event{stop, true, gomidi.NoteOff(ch, k)})
		}
		slices.SortStableFunc(evs, func(a, b event) int {
			if c := cmp.Compare(a.tick, b.tick); c != 0 {
				return c
			}
			switch { // a note ends before the next one on the same key starts
			case a.off && !b.off:
				return -1
			case b.off && !a.off:
				return 1
			}
			return 0
		})
		last := uint32(0)
		for _, e := range evs {
			tr.Add(e.tick-last, e.msg)
			last = e.tick
		}
		tr.Close(0)
		if err := f.Add(tr); err != nil {
			return nil, err
		}
	}
	var buf bytes.Buffer
	if _, err := f.WriteTo(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
