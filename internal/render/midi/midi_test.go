package midi

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	gomidi "gitlab.com/gomidi/midi/v2"
	"gitlab.com/gomidi/midi/v2/smf"
)

var beat = Score{BPM: 82, BeatsPerBar: 4, Bars: 2, Tracks: []Track{
	{Name: "Drums", Instrument: "drums", Notes: []Note{
		{Pitch: "kick", Bar: 1, Beat: 1, Length: 0.5, Velocity: 110},
		{Pitch: "snare", Bar: 1, Beat: 2, Length: 0.5},
		{Pitch: "closed hat", Bar: 1, Beat: 2.5, Length: 0.25, Velocity: 60},
		{Pitch: "talking_drum", Bar: 1, Beat: 3, Length: 1}, // not a drum it knows
	}},
	{Name: "Keys", Instrument: "electric_piano", Notes: []Note{
		{Pitch: "D4", Bar: 1, Beat: 1, Length: 4, Velocity: 70},
		{Pitch: "F#3", Bar: 2, Beat: 1, Length: 8, Velocity: 70}, // cut at the end
		{Pitch: "H2", Bar: 1, Beat: 1, Length: 1},                // not a pitch
		{Pitch: "C4", Bar: 3, Beat: 1, Length: 1},                // past the end
	}},
	{Name: "Kazoo", Instrument: "kazoo", Notes: []Note{{Pitch: "C4", Bar: 1, Beat: 1, Length: 1}}},
}}

func TestRender(t *testing.T) {
	if _, err := Render("Silence", []byte(`{"tracks":[{"instrument":"kazoo","notes":[]}]}`)); !errors.Is(err, ErrEmpty) {
		t.Errorf("an empty score: %v", err)
	}
	raw, _ := json.Marshal(beat)
	data, err := Render("Lagos After Rain", raw)
	if err != nil {
		t.Fatal(err)
	}
	f, err := smf.ReadFrom(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Tracks) != 3 {
		t.Fatalf("%d tracks", len(f.Tracks))
	}
	var bpm float64
	for _, e := range f.Tracks[0] {
		e.Message.GetMetaTempo(&bpm)
	}
	if bpm < 81.9 || bpm > 82.1 {
		t.Errorf("tempo %v", bpm)
	}
	ons := map[int][]uint8{}
	var drumCh, keysCh, keysProgram uint8 = 255, 255, 255
	for i, tr := range f.Tracks[1:] {
		for _, e := range tr {
			var ch, k, v, prog uint8
			switch {
			case e.Message.GetNoteStart(&ch, &k, &v):
				ons[i] = append(ons[i], k)
				if i == 0 {
					drumCh = ch
				} else {
					keysCh = ch
				}
			case gomidi.Message(e.Message).GetProgramChange(&ch, &prog):
				keysProgram = prog
			}
		}
	}
	if drumCh != 9 || keysCh != 0 || keysProgram != 4 {
		t.Errorf("channels %d, %d, program %d", drumCh, keysCh, keysProgram)
	}
	if !bytes.Equal(ons[0], []uint8{36, 38, 42}) || !bytes.Equal(ons[1], []uint8{62, 54}) {
		t.Errorf("notes %v", ons)
	}
}
