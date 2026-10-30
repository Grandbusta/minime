package mind

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/Grandbusta/minime/internal/memory"
	"github.com/Grandbusta/minime/internal/providers/embed"
	"github.com/Grandbusta/minime/internal/providers/system2"
)

// wordEmbedder embeds a text as how often it mentions each of a few words,
// so texts about the same thing are alike.
type wordEmbedder struct{}

func (wordEmbedder) Embed(_ context.Context, texts []string) (embed.Embedded, error) {
	words := []string{"song", "beat", "rest", "tired", "money", "publish"}
	out := embed.Embedded{Model: "fake/words"}
	for _, t := range texts {
		v := make([]float32, len(words))
		for i, w := range words {
			v[i] = float32(strings.Count(strings.ToLower(t), w))
		}
		out.Vectors = append(out.Vectors, v)
	}
	return out, nil
}

func addWork(s *fakeStore, title, kind, text, subject string, at time.Time) memory.Work {
	w := memory.Work{ID: ulid.Make(), Kind: kind, Title: title, Text: text, CreatedAt: at}
	if subject != "" {
		i := memory.Intention{ID: ulid.Make(), Subject: subject, Kind: memory.IntentionTask, Text: title, Status: memory.IntentionDone}
		s.intentions = append(s.intentions, i)
		w.IntentionID = i.ID
	}
	s.works = append(s.works, w)
	return w
}

func TestRepliesKnowItsOwnWorks(t *testing.T) {
	s2 := &fakeSystem2{}
	m, s := promiseMind(&fakeReplier{}, s2)
	m.Embedder = wordEmbedder{}
	addWork(s, "Lagos After Rain", "beat_idea", "82 BPM, D minor. A soft beat, kick on one.", "", now.Add(-3*time.Hour))
	addWork(s, "Money Notes", "notes", "Saving money each month.", "", now.Add(-2*time.Hour))
	addWork(s, "Ada's Song", "lyrics", "A song for Ada.", "test:ada", now.Add(-time.Hour))
	addWork(s, "Diary", "journal", "Tired today, beat.", "", now.Add(-time.Minute))

	m.handle(context.Background(), percept("1", "Can you send me that beat you made?"))
	note := s2.requests[0].Note
	if !strings.Contains(note, "Money Notes") || !strings.Contains(note, "Lagos After Rain") {
		t.Errorf("no list of its works:\n%s", note)
	}
	if strings.Contains(note, "Ada's Song") || strings.Contains(note, "Diary") {
		t.Errorf("brought up someone else's work or its journal:\n%s", note)
	}
	if !strings.Contains(note, "What this seems to be about, exactly as you made it:\n\n[w2] \"Lagos After Rain\" (Tue 27 Oct 11:16, beat idea, your own):\n<<<WORK\n82 BPM, D minor.") ||
		strings.Contains(note, "Saving money each month") {
		t.Errorf("the work meant isn't given in full, or another is:\n%s", note)
	}
}

func TestAReplySendsAWorkItMade(t *testing.T) {
	r, filer := &fakeReplier{}, &fakeFiler{}
	s2 := &fakeSystem2{send: system2.Send{Work: "w2", As: "pdf"}}
	m, s := promiseMind(r, s2)
	m.Embedder, m.Filer = wordEmbedder{}, filer
	addWork(s, "Lagos After Rain", "beat_idea", "82 BPM, D minor. A soft beat.", "", now.Add(-3*time.Hour))
	addWork(s, "Money Notes", "notes", "Saving money each month.", "", now.Add(-2*time.Hour))
	m.handle(context.Background(), percept("1", "Send me that beat as a PDF"))

	if len(r.sent) != 0 || len(filer.sent) != 1 || filer.sent[0].name != "lagos-after-rain.pdf" || filer.sent[0].caption != "a thoughtful reply" {
		t.Fatalf("messages %q, files %d", r.sent, len(filer.sent))
	}
	if len(s.intentions) != 0 {
		t.Errorf("promised it as well: %+v", s.intentions)
	}

	for name, send := range map[string]system2.Send{"unknown ref": {Work: "w9", As: "pdf"}, "unknown format": {Work: "w2", As: "mp3"}} {
		s2.send = send
		m.handle(context.Background(), percept(name, "Send me that beat"))
		if len(filer.sent) != 1 || len(r.sent) == 0 {
			t.Errorf("%s: files %d, messages %d", name, len(filer.sent), len(r.sent))
		}
	}

	s.people[sam] = memory.Person{Key: sam, Name: "Sam", Circle: memory.CircleStranger}
	s2.send = system2.Send{Work: "w2", As: "pdf"}
	m.handle(context.Background(), percept("3", "Send me that beat as a PDF"))
	if len(filer.sent) != 1 || strings.Contains(s2.requests[len(s2.requests)-1].Note, "Lagos After Rain") {
		t.Errorf("a stranger saw or got its works")
	}
}
