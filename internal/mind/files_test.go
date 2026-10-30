package mind

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Grandbusta/minime/internal/memory"
	"github.com/Grandbusta/minime/internal/perception"
	"github.com/Grandbusta/minime/internal/providers/system2"
)

type sentFile struct {
	name, caption string
	data          []byte
}

type fakeFiler struct{ sent []sentFile }

func (f *fakeFiler) SendFile(_ context.Context, _ perception.Percept, name string, data []byte, caption string) error {
	f.sent = append(f.sent, sentFile{name, caption, data})
	return nil
}

func TestPromisesCanBeSentAsAPDF(t *testing.T) {
	s2 := &fakeSystem2{promises: []system2.Promise{
		{By: "2026-10-27T20:00", What: "Tobi writes Sam a plan for the week", As: "pdf"},
		{By: "2026-10-27T21:00", What: "Tobi records Sam a song", As: "mp3"},
	}}
	m, s := promiseMind(&fakeReplier{}, s2)
	m.handle(context.Background(), percept("1", "Can you send me a plan as a PDF?"))
	if len(s.intentions) != 2 || s.intentions[0].Args["as"] != "" || s.intentions[1].Args["as"] != "" {
		t.Errorf("without a way to send files: %+v", s.intentions)
	}

	m, s = promiseMind(&fakeReplier{}, s2)
	m.Filer = &fakeFiler{}
	m.handle(context.Background(), percept("1", "Can you send me a plan as a PDF?"))
	if len(s.intentions) != 2 || s.intentions[0].Args["as"] != "pdf" || s.intentions[1].Args["as"] != "" {
		t.Errorf("intentions = %+v", s.intentions)
	}
}

const weekJSON = `{"title":"Sam's Week","kind":"writing","text":"Sam's Week\n\n## Monday\n\n- Call the clinic","project_notes":"",
	"project_done":false,"remember":[]}`

func TestAPDFPromiseIsSentAsAFile(t *testing.T) {
	r, filer := &fakeReplier{}, &fakeFiler{}
	s2 := &fakeSystem2{formats: map[string]string{"activity": weekJSON}}
	m, s := promiseMind(r, s2)
	m.Filer = filer
	i := promised("Tobi writes Sam a plan for the week", "", "", now.Add(-time.Minute))
	i.Args = map[string]string{"as": "pdf"}
	s.intentions = []memory.Intention{i}
	m.tick(context.Background(), now)

	if len(r.sent) != 0 || len(filer.sent) != 1 {
		t.Fatalf("messages %q, files %d", r.sent, len(filer.sent))
	}
	if f := filer.sent[0]; f.name != "sam-s-week.pdf" || f.caption != "a thoughtful reply" || !bytes.HasPrefix(f.data, []byte("%PDF-")) {
		t.Errorf("file %s, caption %q, %d bytes", f.name, f.caption, len(f.data))
	}
	if got := s.intention(i.ID); got.Status != memory.IntentionDone {
		t.Errorf("intention = %+v", got)
	}
}

func TestAnApprovedPDFGoesWithItsFile(t *testing.T) {
	r, filer := &fakeReplier{}, &fakeFiler{}
	s2 := &fakeSystem2{formats: map[string]string{"activity": weekJSON}}
	m, s, asker := gatedMind(r, s2)
	m.Filer = filer
	i := promised("Tobi writes Sam a plan for the week", "", "", now.Add(-time.Minute))
	i.Args = map[string]string{"as": "pdf"}
	s.intentions = []memory.Intention{i}
	m.tick(context.Background(), now)

	if len(asker.asked) != 1 || !strings.Contains(asker.asked[0], "📎 sam-s-week.pdf") || len(filer.sent) != 0 {
		t.Fatalf("asked %q, files %d", asker.asked, len(filer.sent))
	}
	m.handle(context.Background(), tap(owner, asker.ids[0], true))
	if len(filer.sent) != 1 || filer.sent[0].caption != "a thoughtful reply" || len(r.sent) != 0 {
		t.Fatalf("files %d, messages %q", len(filer.sent), r.sent)
	}
}
