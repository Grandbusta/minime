package main

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/oklog/ulid/v2"

	"github.com/Grandbusta/minime/internal/config"
	"github.com/Grandbusta/minime/internal/memory"
)

func TestNewAbilities(t *testing.T) {
	set, err := newAbilities([]string{"read_article", "read_book", "read_article"}, abilityDeps{})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(set.Names(), ","); got != "journal,read_article,read_book" {
		t.Errorf("abilities = %s", got)
	}
	if set, err := newAbilities(nil, abilityDeps{}); err != nil || strings.Join(set.Names(), ",") != "journal" {
		t.Errorf("none listed: %v, %v", set.Names(), err)
	}
	if _, err := newAbilities([]string{"fly"}, abilityDeps{}); err == nil || !strings.Contains(err.Error(), "read_article, read_book, read_page, web_search") {
		t.Errorf("unknown ability: %v", err)
	}
	if _, err := newAbilities([]string{"web_search"}, abilityDeps{}); err == nil || !strings.Contains(err.Error(), "search.provider") {
		t.Errorf("web search without a provider: %v", err)
	}
	set, err = newAbilities([]string{"web_search", "read_page"}, abilityDeps{search: config.Search{Provider: "openai"},
		log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if err != nil || strings.Join(set.Names(), ",") != "journal,web_search,read_page" {
		t.Errorf("web abilities = %v, %v", set.Names(), err)
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

func TestPaceScalesTheBudgetAndTheWeb(t *testing.T) {
	cfg := &config.Config{DailyLife: config.DailyLife{Enabled: true, Pace: 10, PlanAt: "06:00", JournalAt: "22:00", ReviewAt: "Sun 20:00",
		Activities: 4, BudgetUSD: 0.05, Web: config.Web{Searches: 10, Pages: 15},
		PlanPrompt: "../../prompts/plan.md", ActivityPrompt: "../../prompts/activity.md", ReviewPrompt: "../../prompts/review.md",
		LearnPrompt: "../../prompts/learn.md"}}
	day, err := newDay(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if day.Pace != 10 || day.BudgetUSD < 0.4999 || day.BudgetUSD > 0.5001 || day.MaxActivities != 4 {
		t.Errorf("day = pace %d, budget %v, activities %d", day.Pace, day.BudgetUSD, day.MaxActivities)
	}
	if d := depsFor(cfg, nil, nil); d.web.Searches != 100 || d.web.Pages != 150 {
		t.Errorf("web = %+v", d.web)
	}
}
