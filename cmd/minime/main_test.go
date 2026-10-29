package main

import (
	"context"
	"strings"
	"testing"

	"github.com/oklog/ulid/v2"

	"minime/internal/memory"
)

func TestNewAbilities(t *testing.T) {
	set, err := newAbilities([]string{"read_article", "read_book", "read_article"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(set.Names(), ","); got != "journal,read_article,read_book" {
		t.Errorf("abilities = %s", got)
	}
	if set, err := newAbilities(nil, nil); err != nil || strings.Join(set.Names(), ",") != "journal" {
		t.Errorf("none listed: %v, %v", set.Names(), err)
	}
	if _, err := newAbilities([]string{"web_search"}, nil); err == nil || !strings.Contains(err.Error(), "read_article, read_book") {
		t.Errorf("unknown ability: %v", err)
	}
}

type fakeWorks struct{ works []memory.Work }

func (f *fakeWorks) RecentWorks(context.Context, int) ([]memory.Work, error) { return f.works, nil }

func (f *fakeWorks) SetWorkPublic(_ context.Context, id ulid.ULID, public bool, by string) (bool, error) {
	for i := range f.works {
		if f.works[i].ID == id && (!public || f.works[i].Kind != "journal") {
			f.works[i].Public, f.works[i].PublicBy = &public, by
			return true, nil
		}
	}
	return false, nil
}

func TestWorksCommand(t *testing.T) {
	yes := true
	lyrics := memory.Work{ID: ulid.Make(), Kind: "lyrics", Title: "Lagos Rain", Public: &yes, PublicBy: memory.PublicBySystem1}
	journal := memory.Work{ID: ulid.Make(), Kind: "journal", Title: "29 Oct"}
	st := &fakeWorks{works: []memory.Work{lyrics, journal}}
	var out strings.Builder
	if err := works(context.Background(), st, nil, 20, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "yes (system1)") || !strings.Contains(out.String(), "undecided") {
		t.Errorf("list:\n%s", out.String())
	}
	if err := works(context.Background(), st, []string{"hide", lyrics.ID.String()}, 20, &out); err != nil || *st.works[0].Public ||
		st.works[0].PublicBy != memory.PublicByOwner {
		t.Errorf("hide: %v, %+v", err, st.works[0])
	}
	if err := works(context.Background(), st, []string{"show", journal.ID.String()}, 20, &out); err == nil ||
		!strings.Contains(err.Error(), "journal") {
		t.Errorf("showing the journal: %v", err)
	}
	for _, args := range [][]string{{"hide"}, {"publish", lyrics.ID.String()}, {"show", "not-an-id"}} {
		if err := works(context.Background(), st, args, 20, &out); err == nil {
			t.Errorf("%v: no error", args)
		}
	}
}
