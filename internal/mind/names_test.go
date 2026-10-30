package mind

import (
	"context"
	"strings"
	"testing"

	"github.com/Grandbusta/minime/internal/memory"
)

func TestNameNote(t *testing.T) {
	said := func(replies ...string) []memory.Exchange {
		var es []memory.Exchange
		for _, r := range replies {
			es = append(es, memory.Exchange{Message: "hi", Reply: r})
		}
		return es
	}
	for name, c := range map[string]struct {
		recent []memory.Exchange
		want   bool
	}{
		"none lately":          {said("Sounds good.", "Hope it goes well!"), false},
		"in the last":          {said("Hey.", "Thanks, Sam."), true},
		"a few back":           {said("Thanks, sam!", "Okay.", "Sure.", "Nice."), true},
		"too far back":         {said("Hey Sam.", "a", "b", "c", "d"), false},
		"it spoke first, none": {[]memory.Exchange{{Reply: "Morning!"}}, false},
	} {
		if got := nameNote(c.recent, "Sam") != ""; got != c.want {
			t.Errorf("%s: note = %v, want %v", name, got, c.want)
		}
	}
	if nameNote(said("Hi Friend"), fallbackName) != "" {
		t.Error("a note about a name it doesn't know")
	}
}

func TestRepliesGoEasyOnTheName(t *testing.T) {
	s2 := &fakeSystem2{}
	m, s := newMind(&fakeReplier{}, &fakeSystem1{choice: "ask_for_help", confidence: 0.3}, s2)
	s.exchanges = []memory.Exchange{{Message: "I have meetings all day", Reply: "Sounds like a packed day, Sam."}}
	m.handle(context.Background(), percept("1", "Yeah, engineering calls"))
	if len(s2.requests) == 0 || !strings.Contains(s2.requests[len(s2.requests)-1].Note, "don't use it in this one") {
		t.Errorf("the reply wasn't told to leave the name out")
	}
}
