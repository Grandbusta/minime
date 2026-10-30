// Package reading is the agent reading books a passage a sitting over many
// days, and Wikipedia articles at once. Texts are kept in the store, so a
// book is downloaded once.
package reading

import (
	"context"
	"fmt"
	"strings"

	"github.com/oklog/ulid/v2"

	"github.com/Grandbusta/minime/internal/ability"
	"github.com/Grandbusta/minime/internal/library"
	"github.com/Grandbusta/minime/internal/memory"
)

const (
	BookName    = "read_book"
	ArticleName = "read_article"

	NotesKind = "reading_notes"

	PassageSize = 12000 // bytes of a book read in one sitting, about 3,000 tokens
	ArticleSize = 20000
)

type Library interface {
	FindBook(ctx context.Context, query string) (library.Book, error)
	BookText(ctx context.Context, b library.Book) (string, error)
	FindArticle(ctx context.Context, query string) (library.Article, error)
}

type Store interface {
	// SaveSource stores a source and returns its ID: the existing one if its URL was fetched before.
	SaveSource(ctx context.Context, src memory.Source) (ulid.ULID, error)
	SourceText(ctx context.Context, id ulid.ULID) (string, error)
	ReadingProject(ctx context.Context, source ulid.ULID) (memory.Project, bool, error)
}

type Book struct {
	Library Library
	Store   Store
}

func (Book) Name() string { return BookName }

func (Book) Describe() string {
	return "start a public-domain book from Project Gutenberg and read it a passage a day; query: its title and author, e.g. \"Pride and Prejudice Austen\", with new_project_title the book's title and project_kind \"reading\". Only classics, mostly published before 1930, are there; modern books aren't. To keep reading a book, give its project's id and leave ability and query empty."
}

func (Book) Risk() ability.Risk { return ability.ReadsWeb }

func (Book) Continues(p memory.Project) bool { return !p.SourceID.IsZero() }

// Prepare gives today's passage of the book, and the project with its place
// moved on (a new project for a book started without one).
func (b Book) Prepare(ctx context.Context, a ability.Act) (ability.Material, error) {
	project := a.Project
	if project == nil || project.SourceID.IsZero() {
		if strings.TrimSpace(a.Query()) == "" {
			return ability.Material{}, fmt.Errorf("which book: %w", library.ErrNotFound)
		}
		book, err := b.Library.FindBook(ctx, a.Query())
		if err != nil {
			return ability.Material{}, err
		}
		text, err := b.Library.BookText(ctx, book)
		if err != nil {
			return ability.Material{}, err
		}
		src := memory.Source{ID: ulid.Make(), Reading: memory.Reading{Kind: memory.ReadBook, Title: book.Title, Author: book.Author,
			Chars: len(text)}, URL: book.TextURL, Text: text, FetchedAt: a.Now.UTC()}
		id, err := b.Store.SaveSource(ctx, src)
		if err != nil {
			return ability.Material{}, err
		}
		reading, ok, err := b.Store.ReadingProject(ctx, id)
		switch {
		case err != nil:
			return ability.Material{}, err
		case ok:
			project = &reading // a book it's already reading: carry on where it got to
		case project == nil:
			project = &memory.Project{ID: ulid.Make(), Title: book.Title, Kind: "reading", Status: memory.ProjectActive,
				CreatedAt: a.Now.UTC(), UpdatedAt: a.Now.UTC()}
		default:
			p := *project
			project = &p
		}
		if !ok {
			r := src.Reading
			project.SourceID, project.Position, project.Source = id, 0, &r
		}
	} else {
		p := *project
		project = &p
	}

	text, err := b.Store.SourceText(ctx, project.SourceID)
	if err != nil {
		return ability.Material{}, err
	}
	from := project.Position
	passage, next := library.Passage(text, from, PassageSize)
	if passage == "" {
		return ability.Material{Note: "\n\nYou've already read to the end of this book.", Project: project, Done: true, WorkKind: NotesKind}, nil
	}
	project.Position = next
	ending := ""
	if next >= len(text) {
		ending = " This is the end of the book."
	}
	title, author := "this book", "an unknown author"
	if project.Source != nil {
		title, author = project.Source.Title, orDefault(project.Source.Author, author)
	}
	return ability.Material{
		Note: fmt.Sprintf("\n\nYou're reading %q by %s, %d%% of the way through. Today's passage:%s%s",
			title, author, percentThrough(from, len(text)), ability.Fence(passage), ending),
		Project:  project,
		Done:     next >= len(text),
		WorkKind: NotesKind,
	}, nil
}

type Article struct {
	Library Library
	Store   Store
}

func (Article) Name() string { return ArticleName }

func (Article) Describe() string {
	return "look a topic up on Wikipedia and read the article, to learn about it; query: the topic, e.g. \"polyrhythm\". Usually part of a learning project."
}

func (Article) Risk() ability.Risk { return ability.ReadsWeb }

func (r Article) Prepare(ctx context.Context, a ability.Act) (ability.Material, error) {
	art, err := r.Library.FindArticle(ctx, a.Query())
	if err != nil {
		return ability.Material{}, err
	}
	if _, err := r.Store.SaveSource(ctx, memory.Source{ID: ulid.Make(), Reading: memory.Reading{Kind: memory.ReadArticle,
		Title: art.Title, Chars: len(art.Text)}, URL: art.URL, Text: art.Text, FetchedAt: a.Now.UTC()}); err != nil {
		return ability.Material{}, err
	}
	passage, _ := library.Passage(art.Text, 0, ArticleSize)
	return ability.Material{
		Note:     fmt.Sprintf("\n\nYou looked up %q and are reading the Wikipedia article %q.%s", a.Query(), art.Title, ability.Fence(passage)),
		Project:  a.Project,
		WorkKind: NotesKind,
	}, nil
}

func percentThrough(pos, total int) int {
	if total <= 0 {
		return 0
	}
	return min(100, pos*100/total)
}

func orDefault(s, def string) string {
	if s = strings.TrimSpace(s); s != "" {
		return s
	}
	return def
}
