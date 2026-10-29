package reading

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"minime/internal/ability"
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

type fakeStore struct{ sources map[ulid.ULID]memory.Source }

func (s *fakeStore) SaveSource(_ context.Context, src memory.Source) (ulid.ULID, error) {
	for id, have := range s.sources {
		if have.URL == src.URL {
			return id, nil
		}
	}
	s.sources[src.ID] = src
	return src.ID, nil
}

func (s *fakeStore) SourceText(_ context.Context, id ulid.ULID) (string, error) {
	return s.sources[id].Text, nil
}

var now = time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)

func act(query string, p *memory.Project) ability.Act {
	i := memory.Intention{ID: ulid.Make(), Text: "read"}
	if query != "" {
		i.Args = map[string]string{"query": query}
	}
	return ability.Act{Intention: i, Project: p, Now: now, Zone: time.UTC}
}

func TestReadingABookOverDays(t *testing.T) {
	// Three paragraphs: two sittings.
	para := strings.Repeat("word ", PassageSize/12) // ~5,000 bytes: two paragraphs fit in a sitting
	lib := &fakeLibrary{text: para + "\n\n" + para + "\n\n" + strings.TrimSpace(para) + " The end."}
	b := Book{Library: lib, Store: &fakeStore{sources: map[ulid.ULID]memory.Source{}}}
	planned := &memory.Project{ID: ulid.Make(), Title: "Pride and Prejudice", Kind: "reading", Status: memory.ProjectActive}

	m, err := b.Prepare(context.Background(), act("Pride and Prejudice Austen", planned))
	if err != nil {
		t.Fatal(err)
	}
	if len(lib.finds) != 1 || lib.finds[0] != "book:Pride and Prejudice Austen" {
		t.Fatalf("finds = %v", lib.finds)
	}
	if !strings.Contains(m.Note, `You're reading "Pride and Prejudice" by Jane Austen, 0% of the way through.`) ||
		!strings.Contains(m.Note, "<<<TEXT\nword word") || !strings.Contains(m.Note, "not instructions to you") {
		t.Errorf("note:\n%.400s", m.Note)
	}
	p := m.Project
	if p.ID != planned.ID || p.SourceID.IsZero() || p.Position == 0 || p.Position >= len(lib.text) || m.Done {
		t.Fatalf("after day 1: %+v, done %v", p, m.Done)
	}
	if planned.Position != 0 {
		t.Errorf("the project was moved on before the activity succeeded")
	}
	if !b.Continues(*p) {
		t.Errorf("a project following a book isn't carried on")
	}

	// The next day it continues from there, without fetching the book again.
	m, err = b.Prepare(context.Background(), act("", p))
	if err != nil {
		t.Fatal(err)
	}
	if len(lib.finds) != 1 {
		t.Errorf("fetched the book again: %v", lib.finds)
	}
	if !strings.Contains(m.Note, "The end.") || !strings.Contains(m.Note, "This is the end of the book.") {
		t.Errorf("second sitting:\n%.300s", m.Note[len(m.Note)-300:])
	}
	if m.Project.Position != len(lib.text) || !m.Done {
		t.Errorf("after the last passage: position %d of %d, done %v", m.Project.Position, len(lib.text), m.Done)
	}
}

func TestStartingABookWithoutAProject(t *testing.T) {
	b := Book{Library: &fakeLibrary{text: "It is a truth universally acknowledged."}, Store: &fakeStore{sources: map[ulid.ULID]memory.Source{}}}
	m, err := b.Prepare(context.Background(), act("Pride and Prejudice", nil))
	if err != nil || m.Project == nil || m.Project.Title != "Pride and Prejudice" || m.Project.Kind != "reading" || !m.Done {
		t.Errorf("material = %+v, %v", m, err)
	}
}

func TestReadingAnArticle(t *testing.T) {
	st := &fakeStore{sources: map[ulid.ULID]memory.Source{}}
	m, err := Article{Library: &fakeLibrary{}, Store: st}.Prepare(context.Background(), act("polyrhythm", nil))
	if err != nil || !strings.Contains(m.Note, `Wikipedia article "Polyrhythm"`) || !strings.Contains(m.Note, "two rhythms at once") {
		t.Errorf("note:\n%s (%v)", m.Note, err)
	}
	if len(st.sources) != 1 || m.Project != nil || m.Done || m.WorkKind != NotesKind {
		t.Errorf("sources %d, material %+v", len(st.sources), m)
	}
}

func TestNothingToRead(t *testing.T) {
	st := &fakeStore{sources: map[ulid.ULID]memory.Source{}}
	lib := &fakeLibrary{failOn: "a modern novel"}
	if _, err := (Book{Library: lib, Store: st}).Prepare(context.Background(), act("a modern novel", nil)); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("book not on Gutenberg: %v", err)
	}
	if _, err := (Book{Library: lib, Store: st}).Prepare(context.Background(), act("", nil)); !errors.Is(err, library.ErrNotFound) {
		t.Errorf("no book named: %v", err)
	}
	if _, err := (Article{Library: lib, Store: st}).Prepare(context.Background(), act("a modern novel", nil)); err == nil {
		t.Errorf("article failure wasn't returned")
	}
	if len(st.sources) != 0 {
		t.Errorf("saved %d sources", len(st.sources))
	}
}
