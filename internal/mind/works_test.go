package mind

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/Grandbusta/minime/internal/memory"
	"github.com/Grandbusta/minime/internal/providers/system2"
)

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
	e := &wordEmbedder{}
	m.Embedder = e
	addWork(s, "Lagos After Rain", "beat_idea", "82 BPM, D minor. A soft beat, kick on one.", "", now.Add(-3*time.Hour))
	addWork(s, "Money Notes", "notes", "Saving money each month.", "", now.Add(-2*time.Hour))
	addWork(s, "Ada's Song", "lyrics", "A song for Ada.", "test:ada", now.Add(-time.Hour))
	addWork(s, "Diary", "journal", "Tired today, beat.", "", now.Add(-time.Minute))

	m.handle(context.Background(), percept("1", "Can you send me that beat you made?"))
	note := s2.requests[0].Note
	if !strings.Contains(note, "What you've made lately, newest first, each with a ref:\n- [w1] Tue 27 Oct 12:16, notes, your own: \"Money Notes\"\n- [w2] Tue 27 Oct 11:16, beat idea, your own: \"Lagos After Rain\"") {
		t.Errorf("no list of its works:\n%s", note)
	}
	if strings.Contains(note, "Ada's Song") || strings.Contains(note, "Diary") {
		t.Errorf("brought up someone else's work or its journal:\n%s", note)
	}
	if !strings.Contains(note, "What this seems to be about, exactly as you made it:\n\n[w2] \"Lagos After Rain\" (Tue 27 Oct 11:16, beat idea, your own):\n<<<WORK\n82 BPM, D minor.") ||
		strings.Contains(note, "Saving money each month") {
		t.Errorf("the work meant isn't given in full, or another is:\n%s", note)
	}
	for _, w := range s.works {
		if w.Kind != "journal" && w.Title != "Ada's Song" && w.EmbeddedWith != "fake/words" {
			t.Errorf("%q not embedded", w.Title)
		}
	}

	calls := e.calls
	m.handle(context.Background(), percept("2", "How are you?"))
	if e.calls != calls+1 {
		t.Errorf("embedded the works again: %d calls", e.calls-calls)
	}
	if note := s2.requests[1].Note; strings.Contains(note, "exactly as you made it") {
		t.Errorf("small talk was given a work:\n%s", note)
	}
}

func TestAPromiseOfSomethingMadeSendsIt(t *testing.T) {
	s2 := &fakeSystem2{formats: map[string]string{"activity": verseJSON}}
	m, s := promiseMind(&fakeReplier{}, s2)
	m.Embedder = &wordEmbedder{}
	addWork(s, "Lagos After Rain", "beat_idea", "82 BPM, D minor. A soft beat.", "", now.Add(-3*time.Hour))
	s.intentions = append(s.intentions, promised("Tobi sends Sam the Lagos After Rain beat", "", "", now.Add(-time.Minute)))
	m.tick(context.Background(), now)
	if note := s2.requests[0].Note; !strings.Contains(note, "<<<WORK\n82 BPM, D minor. A soft beat.\nWORK>>>") ||
		!strings.Contains(note, "send that: keep it as it is") {
		t.Errorf("making note:\n%s", note)
	}
}

func TestAReplySendsAWorkItMade(t *testing.T) {
	r, filer := &fakeReplier{}, &fakeFiler{}
	s2 := &fakeSystem2{send: system2.Send{Work: "w2", As: "pdf"}}
	m, s := promiseMind(r, s2)
	m.Embedder, m.Filer = &wordEmbedder{}, filer
	addWork(s, "Lagos After Rain", "beat_idea", "82 BPM, D minor. A soft beat.", "", now.Add(-3*time.Hour))
	addWork(s, "Money Notes", "notes", "Saving money each month.", "", now.Add(-2*time.Hour))
	m.handle(context.Background(), percept("1", "Send me that beat as a PDF"))

	if len(r.sent) != 0 || len(filer.sent) != 1 || filer.sent[0].name != "lagos-after-rain.pdf" || filer.sent[0].caption != "a thoughtful reply" {
		t.Fatalf("messages %q, files %d", r.sent, len(filer.sent))
	}
	if d := s.decisions[len(s.decisions)-1]; !strings.HasSuffix(d.Reply, "a thoughtful reply\n\n📎 lagos-after-rain.pdf") {
		t.Errorf("recorded %q", d.Reply)
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

func TestAPromiseOfAWorkSendsItAsItIs(t *testing.T) {
	r, filer := &fakeReplier{}, &fakeFiler{}
	s2 := &fakeSystem2{promises: []system2.Promise{{By: "2026-10-27T20:00", What: "Tobi sends Sam Lagos After Rain", As: "pdf", Work: "w1"}}}
	m, s := promiseMind(r, s2)
	m.Embedder, m.Filer = &wordEmbedder{}, filer
	beat := addWork(s, "Lagos After Rain", "beat_idea", "82 BPM, D minor. A soft beat.", "", now.Add(-3*time.Hour))
	m.handle(context.Background(), percept("1", "Send me the beat as a PDF tonight"))
	if len(s.intentions) != 1 || s.intentions[0].Args["work"] != beat.ID.String() {
		t.Fatalf("intentions = %+v", s.intentions)
	}

	s.intentions[0].DueAt = now.Add(-time.Minute)
	asked := len(s2.requests)
	m.tick(context.Background(), now)
	if len(s2.requests) != asked+1 || s2.requests[asked].Format != nil {
		t.Errorf("made it again: %d requests", len(s2.requests)-asked)
	}
	if len(filer.sent) != 1 || filer.sent[0].name != "lagos-after-rain.pdf" {
		t.Fatalf("files %d", len(filer.sent))
	}
	if got := s.intention(s.intentions[0].ID); got.Status != memory.IntentionDone || got.Result != "Lagos After Rain" {
		t.Errorf("intention = %+v", got)
	}
}

func TestSimilarWorksAreToldApart(t *testing.T) {
	s2 := &fakeSystem2{}
	m, s := promiseMind(&fakeReplier{}, s2)
	m.Embedder = &wordEmbedder{}
	addWork(s, "Beat Draft", "beat_idea", "A beat, soft kick.", "", now.Add(-3*time.Hour))
	addWork(s, "Beat for Sam", "beat_idea", "A beat, soft kick.", sam, now.Add(-time.Hour))
	addWork(s, "Money Notes", "notes", "Saving money.", "", now.Add(-2*time.Hour))
	m.handle(context.Background(), percept("1", "Send me the beat"))
	note := s2.requests[0].Note
	for _, want := range []string{
		"- [w1] Tue 27 Oct 13:16, beat idea, made for Sam when they asked, and sent to them: \"Beat for Sam\"",
		"- [w3] Tue 27 Oct 11:16, beat idea, your own: \"Beat Draft\"",
		"[w1] and [w3] fit about equally well. If the talk makes clear which is meant, use that one and say which it is",
	} {
		if !strings.Contains(note, want) {
			t.Errorf("note lacks %q:\n%s", want, note)
		}
	}

	s.works = s.works[:1]
	m.handle(context.Background(), percept("2", "Send me the beat"))
	if note := s2.requests[1].Note; strings.Contains(note, "fit about equally well") {
		t.Errorf("one match was flagged as close:\n%s", note)
	}
}
