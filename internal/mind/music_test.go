package mind

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Grandbusta/minime/internal/providers/system2"
	"github.com/Grandbusta/minime/internal/self"
)

const score = `{"bpm":82,"beats_per_bar":4,"bars":1,"tracks":[{"name":"Drums","instrument":"drums","notes":[
	{"pitch":"kick","bar":1,"beat":1,"length":0.5,"velocity":100},{"pitch":"snare","bar":1,"beat":2,"length":0.5,"velocity":0},
	{"pitch":"cowbell2","bar":1,"beat":3,"length":0.5,"velocity":90}]}]}`

const beatJSON = `{"title":"Lagos After Rain","kind":"beat_idea","text":"82 BPM, D minor. Kick on one, snare on two.",
	"project_notes":"","project_done":false,"remember":[],"lessons":[],"ask_for_review":true,"review_question":"Too sparse?",
	"stage_done":"","score":` + score + `}`

var musicLife = self.Life{Name: "Tobi", Story: []string{"Tobi makes beats."}, Makes: []self.Craft{
	{Kind: "beat_idea", About: "a beat", Score: true}, {Kind: "lyrics", About: "lyrics"}}}

func TestMusicIsMadeWithItsScore(t *testing.T) {
	m, s, s2, r, _ := learningMind()
	m.Life = musicLife
	m.Filer = &fakeFiler{}
	s2.formats["activity"] = beatJSON
	m.tick(context.Background(), at(29, 10, 0))

	var req system2.Request
	for _, q := range s2.requests {
		if q.Format != nil && q.Format.Name == "activity" {
			req = q
		}
	}
	if _, ok := req.Format.Schema["properties"].(map[string]any)["score"]; !ok || !strings.Contains(req.Note, "If what you make is a beat_idea, also write its score") {
		t.Errorf("no score asked for:\n%s", req.Note)
	}
	beat, ok, err := s.LatestWork(context.Background(), "beat_idea")
	if !ok || err != nil {
		t.Fatalf("no beat: %+v", s.works)
	}
	var kept struct {
		Tracks []struct {
			Notes []struct {
				Pitch    string `json:"pitch"`
				Velocity int    `json:"velocity"`
			} `json:"notes"`
		} `json:"tracks"`
	}
	if err := json.Unmarshal(beat.Score, &kept); err != nil || len(kept.Tracks) != 1 || len(kept.Tracks[0].Notes) != 2 ||
		kept.Tracks[0].Notes[1].Velocity != 90 {
		t.Errorf("score kept = %s (%v)", beat.Score, err)
	}
	filer := m.Filer.(*fakeFiler)
	if len(filer.sent) != 1 || filer.sent[0].name != "lagos-after-rain.mid" || !bytes.HasPrefix(filer.sent[0].data, []byte("MThd")) ||
		!strings.Contains(filer.sent[0].caption, "82 BPM, D minor") || len(r.sent) != 0 {
		t.Errorf("review sent: files %d, messages %d", len(filer.sent), len(r.sent))
	}
}

func TestOldMusicIsScoredToBeSentAsMIDI(t *testing.T) {
	r, filer := &fakeReplier{}, &fakeFiler{}
	s2 := &fakeSystem2{send: system2.Send{Work: "w1", As: "midi"}, formats: map[string]string{"score": `{"score":` + score + `}`}}
	m, s := promiseMind(r, s2)
	m.Life, m.Filer, m.Embedder = musicLife, filer, &wordEmbedder{}
	m.Day.ScorePrompt = Prompt{Ref: "score@test", Text: "You are {name}, scoring a beat."}
	old := addWork(s, "Lagos After Rain", "beat_idea", "82 BPM, D minor. Kick on one, snare on two.", "", now.Add(-3*time.Hour))
	m.tick(context.Background(), now) // the life loop scores it, not a reply

	var asked system2.Request
	for _, req := range s2.requests {
		if req.Format != nil && req.Format.Name == "score" {
			asked = req
		}
	}
	if !strings.Contains(asked.Note, "Lagos After Rain\n\n82 BPM, D minor. Kick on one") || asked.Instructions != "You are Tobi, scoring a beat." {
		t.Errorf("scoring request: %+v", asked)
	}
	if w, _, _ := s.Work(context.Background(), old.ID); len(w.Score) == 0 {
		t.Fatal("the score wasn't kept")
	}

	before := len(s2.requests)
	m.handle(context.Background(), percept("1", "Send me that beat as MIDI"))
	if len(filer.sent) != 1 || filer.sent[0].name != "lagos-after-rain.mid" || len(r.sent) != 0 {
		t.Fatalf("files %d, messages %q", len(filer.sent), r.sent)
	}
	for _, req := range s2.requests[before:] {
		if req.Format != nil && req.Format.Name == "score" {
			t.Error("the reply waited on scoring")
		}
	}
	if d := s.decisions[len(s.decisions)-1]; !strings.HasSuffix(d.Reply, "📎 lagos-after-rain.mid") {
		t.Errorf("recorded %q", d.Reply)
	}

	s2.send = system2.Send{Work: "w1", As: "midi"}
	addWork(s, "Rain Verse", "lyrics", "Rain on the roof.", "", now.Add(-time.Minute))
	m.handle(context.Background(), percept("2", "Send me those lyrics as MIDI"))
	if len(filer.sent) != 1 || len(r.sent) != 1 {
		t.Errorf("lyrics went as MIDI: files %d", len(filer.sent))
	}
}
