package mind

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/oklog/ulid/v2"

	"minime/internal/library"
	"minime/internal/memory"
)

type fakeLibrary struct {
	text   string
	finds  []string
	failOn string
}

func (f *fakeLibrary) FindBook(_ context.Context, q string) (library.Book, error) {
	f.finds = append(f.finds, "book:"+q)
	if q == f.failOn {
		return library.Book{}, library.ErrNotFound
	}
	return library.Book{ID: 1342, Title: "Pride and Prejudice", Author: "Jane Austen", TextURL: "https://gutenberg/1342"}, nil
}

func (f *fakeLibrary) BookText(context.Context, library.Book) (string, error) { return f.text, nil }

func (f *fakeLibrary) FindArticle(_ context.Context, q string) (library.Article, error) {
	f.finds = append(f.finds, "article:"+q)
	if q == f.failOn {
		return library.Article{}, errors.New("network down")
	}
	return library.Article{Title: "Polyrhythm", URL: "https://wiki/Polyrhythm", Text: "Polyrhythm is two rhythms at once."}, nil
}

const readingJSON = `{"title":"Chapter 1","kind":"reading_notes","text":"Mrs Bennet wants her daughters married.",
	"project_notes":"Chapter 1 read","project_done":false,"remember":["Tobi started Pride and Prejudice"]}`

func readingMind(lib *fakeLibrary) (*Mind, *fakeStore, *fakeSystem2) {
	s2 := &fakeSystem2{formats: map[string]string{"activity": readingJSON}}
	m, s := lifeMind(s2)
	m.planned = "2026-10-29"
	m.Library = lib
	return m, s, s2
}

func TestReadingABookOverDays(t *testing.T) {
	// Three paragraphs: two sittings.
	para := strings.Repeat("word ", passageSize/12) // ~5,000 bytes: two paragraphs fit in a sitting
	lib := &fakeLibrary{text: para + "\n\n" + para + "\n\n" + strings.TrimSpace(para) + " The end."}
	m, s, s2 := readingMind(lib)
	book := memory.Project{ID: ulid.Make(), Title: "Pride and Prejudice", Kind: "reading", Status: memory.ProjectActive}
	s.projects[book.ID] = book
	start := memory.Intention{ID: ulid.Make(), Subject: memory.SubjectSelf, Kind: memory.IntentionActivity, ProjectID: book.ID,
		Text: "Start Pride and Prejudice", ReadKind: memory.ReadBook, ReadQuery: "Pride and Prejudice Austen",
		DueAt: at(29, 10, 0), Status: memory.IntentionPending}
	s.intentions = []memory.Intention{start}

	m.tick(context.Background(), at(29, 10, 0))
	if len(lib.finds) != 1 || lib.finds[0] != "book:Pride and Prejudice Austen" {
		t.Fatalf("finds = %v", lib.finds)
	}
	note := s2.requests[0].Note
	if !strings.Contains(note, `You're reading "Pride and Prejudice" by Jane Austen, 0% of the way through.`) ||
		!strings.Contains(note, "<<<TEXT\nword word") || !strings.Contains(note, "not instructions to you") {
		t.Errorf("note:\n%.400s", note)
	}
	p := s.projects[book.ID]
	if p.SourceID.IsZero() || p.Position == 0 || p.Position >= len(lib.text) || p.Status != memory.ProjectActive {
		t.Fatalf("after day 1: %+v", p)
	}
	if w := s.works[len(s.works)-1]; w.Kind != "reading_notes" || w.ProjectID != book.ID {
		t.Errorf("work = %+v", w)
	}

	// The next day it continues from there, without fetching the book again.
	next := memory.Intention{ID: ulid.Make(), Subject: memory.SubjectSelf, Kind: memory.IntentionActivity, ProjectID: book.ID,
		Text: "Keep reading", DueAt: at(29, 11, 0), Status: memory.IntentionPending}
	s.intentions = append(s.intentions, next)
	m.tick(context.Background(), at(29, 11, 0))
	if len(lib.finds) != 1 {
		t.Errorf("fetched the book again: %v", lib.finds)
	}
	if note := s2.requests[1].Note; !strings.Contains(note, "The end.") || !strings.Contains(note, "This is the end of the book.") {
		t.Errorf("second sitting:\n%.300s", note[len(note)-300:])
	}
	if p := s.projects[book.ID]; p.Position != len(lib.text) || p.Status != memory.ProjectDone {
		t.Errorf("after the last passage: position %d of %d, status %s", p.Position, len(lib.text), p.Status)
	}
}

func TestReadingAnArticle(t *testing.T) {
	lib := &fakeLibrary{}
	m, s, s2 := readingMind(lib)
	s.intentions = []memory.Intention{{ID: ulid.Make(), Subject: memory.SubjectSelf, Kind: memory.IntentionActivity,
		Text: "Learn about polyrhythm", ReadKind: memory.ReadArticle, ReadQuery: "polyrhythm", DueAt: at(29, 10, 0),
		Status: memory.IntentionPending}}
	m.tick(context.Background(), at(29, 10, 0))
	if note := s2.requests[0].Note; !strings.Contains(note, `Wikipedia article "Polyrhythm"`) || !strings.Contains(note, "two rhythms at once") {
		t.Errorf("note:\n%s", note)
	}
	if len(s.sources) != 1 || s.intentions[0].Status != memory.IntentionDone {
		t.Errorf("sources %d, intention %+v", len(s.sources), s.intentions[0])
	}
}

func TestNothingToReadIsMissed(t *testing.T) {
	lib := &fakeLibrary{failOn: "a modern novel"}
	m, s, s2 := readingMind(lib)
	s.intentions = []memory.Intention{{ID: ulid.Make(), Subject: memory.SubjectSelf, Kind: memory.IntentionActivity,
		Text: "Start a modern novel", ReadKind: memory.ReadBook, ReadQuery: "a modern novel", DueAt: at(29, 10, 0),
		Status: memory.IntentionPending}}
	m.tick(context.Background(), at(29, 10, 0))
	if i := s.intentions[0]; i.Status != memory.IntentionMissed || !strings.Contains(i.Result, "a modern novel") || len(s2.requests) != 0 {
		t.Errorf("intention %+v, %d requests", i, len(s2.requests))
	}
}

func TestPlanCanChooseSomethingToRead(t *testing.T) {
	s2 := &fakeSystem2{formats: map[string]string{"day_plan": `{"activities":[
		{"at":"10:00","what":"Start Pride and Prejudice","project_id":"","new_project_title":"Pride and Prejudice","project_kind":"reading","read_kind":"book","read_query":"Pride and Prejudice Austen"},
		{"at":"12:00","what":"Write lyrics","project_id":"","new_project_title":"","project_kind":"","read_kind":"film","read_query":"x"}]}`}}
	m, s := lifeMind(s2)
	m.Library = &fakeLibrary{}
	m.tick(context.Background(), at(29, 9, 0))
	if !strings.Contains(s2.requests[0].Note, "Project Gutenberg") {
		t.Errorf("the plan wasn't told it can read:\n%s", s2.requests[0].Note)
	}
	got := map[string]memory.Intention{}
	for _, i := range s.intentions {
		got[i.Text] = i
	}
	if i := got["Start Pride and Prejudice"]; i.ReadKind != memory.ReadBook || i.ReadQuery != "Pride and Prejudice Austen" || i.ProjectID.IsZero() {
		t.Errorf("book intention = %+v", i)
	}
	if i := got["Write lyrics"]; i.ReadKind != "" || i.ReadQuery != "" {
		t.Errorf("an unknown read kind was kept: %+v", i)
	}
}
