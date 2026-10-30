package reading

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/Grandbusta/minime/internal/ability"
	"github.com/Grandbusta/minime/internal/library"
	"github.com/Grandbusta/minime/internal/memory"
)

type fakeLibrary struct {
	text  string
	finds []string
}

func (f *fakeLibrary) FindBook(_ context.Context, q string) (library.Book, error) {
	f.finds = append(f.finds, "book:"+q)
	return library.Book{ID: 1342, Title: "Pride and Prejudice", Author: "Jane Austen", TextURL: "https://gutenberg/1342"}, nil
}

func (f *fakeLibrary) BookText(context.Context, library.Book) (string, error) { return f.text, nil }

func (f *fakeLibrary) FindArticle(context.Context, string) (library.Article, error) {
	return library.Article{}, library.ErrNotFound
}

type fakeStore struct{ sources map[ulid.ULID]memory.Source }

func (s *fakeStore) ReadingProject(context.Context, ulid.ULID) (memory.Project, bool, error) {
	return memory.Project{}, false, nil
}

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
