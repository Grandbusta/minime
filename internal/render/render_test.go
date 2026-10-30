package render_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Grandbusta/minime/internal/memory"
	"github.com/Grandbusta/minime/internal/render"
	"github.com/Grandbusta/minime/internal/render/midi"
	"github.com/Grandbusta/minime/internal/render/pdf"
)

func TestAWorkIsMadeOnlyIntoFilesItCarriesTheDataFor(t *testing.T) {
	s := render.NewSet(pdf.Format{}, midi.Format{})
	words := memory.Work{Title: "Verse", Text: "Rain on the roof"}
	beat := memory.Work{Title: "Beat", Text: "82 BPM", Score: json.RawMessage(
		`{"bpm":82,"tracks":[{"instrument":"drums","notes":[{"pitch":"kick","bar":1,"beat":1,"length":1,"velocity":100}]}]}`)}

	if fs := s.For(words); len(fs) != 1 || fs[0].Name() != "pdf" {
		t.Errorf("words can be %d formats", len(fs))
	}
	if fs := s.For(beat); len(fs) != 2 {
		t.Errorf("a scored beat can be %d formats", len(fs))
	}
	if f, err := s.File("midi", beat, "Tobi"); err != nil || f.Name != "beat.mid" || f.Type != "audio/midi" || !bytes.HasPrefix(f.Data, []byte("MThd")) {
		t.Errorf("midi = %s %s, %v", f.Name, f.Type, err)
	}
	if _, err := s.File("midi", words, "Tobi"); !errors.Is(err, render.ErrNoData) {
		t.Errorf("unscored = %v", err)
	}
}
