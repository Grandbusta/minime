package journal

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/Grandbusta/minime/internal/ability"
	"github.com/Grandbusta/minime/internal/memory"
)

type fakeStore struct {
	intentions []memory.Intention
	works      []memory.Work
}

func (s fakeStore) IntentionsDue(context.Context, time.Time, time.Time) ([]memory.Intention, error) {
	return s.intentions, nil
}
func (s fakeStore) WorksBetween(context.Context, time.Time, time.Time) ([]memory.Work, error) {
	return s.works, nil
}

func TestJournalLooksBackAtTheDay(t *testing.T) {
	lagos := time.FixedZone("WAT", 3600)
	now := time.Date(2026, 10, 29, 22, 0, 0, 0, lagos)
	st := fakeStore{
		intentions: []memory.Intention{
			{Subject: memory.SubjectSelf, Text: "Write verse 2", DueAt: now.Add(-12 * time.Hour), Status: memory.IntentionDone},
			{Subject: "telegram:1", Text: "Remind Sam", DueAt: now.Add(-6 * time.Hour), Status: memory.IntentionDone},
			{Subject: memory.SubjectSelf, Ability: Name, Text: "Write today's journal entry.", DueAt: now, Status: memory.IntentionPending},
		},
		works: []memory.Work{{ID: ulid.Make(), Kind: "lyrics", Title: "Lagos Rain, verse 2"}},
	}
	m, err := Journal{Store: st}.Prepare(context.Background(), ability.Act{Now: now, Zone: lagos})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(m.Note, "journal entry") || !strings.Contains(m.Note, "- 10:00 Write verse 2: done") ||
		!strings.Contains(m.Note, "- lyrics: Lagos Rain, verse 2") || strings.Contains(m.Note, "Remind Sam") ||
		strings.Contains(m.Note, "Write today's journal entry.: ") || m.WorkKind != "journal" {
		t.Errorf("material = %+v", m)
	}
}
