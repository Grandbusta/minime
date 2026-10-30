package replay

import (
	"context"
	"fmt"
	"testing"

	"github.com/Grandbusta/minime/internal/decision"
	"github.com/Grandbusta/minime/internal/decision/questions"
	"github.com/Grandbusta/minime/internal/providers/system1"
	"github.com/Grandbusta/minime/internal/store"
)

type fakeStore struct{ examples []store.Example }

func (f fakeStore) LabelledExamples(context.Context, string, string, int) ([]store.Example, error) {
	return f.examples, nil
}

type fakeS1 map[string]decision.Answer

func (f fakeS1) Decide(_ context.Context, req system1.Request) (system1.Response, error) {
	a, ok := f[req.State.(map[string]any)["message"].(string)]
	if !ok {
		return system1.Response{}, fmt.Errorf("no answer")
	}
	return system1.Response{Answers: map[string]decision.Answer{"intent": a}, CostUSD: 0.0001}, nil
}

var set = questions.Set{ID: "incoming_message", Version: 2, Primary: "intent",
	Questions: []system1.Question{{Name: "intent", Type: decision.QuestionChoice}}, Thresholds: questions.Thresholds{Act: 0.85, Ask: 0.5}}

func choice(c string, conf float64) decision.Answer {
	return decision.Answer{Type: decision.QuestionChoice, Choice: c, Confidence: conf}
}

func TestReplaySuggestsAThreshold(t *testing.T) {
	var ex []store.Example
	s1 := fakeS1{}
	// 10 right at 0.70 to 0.95; one wrong at 0.62.
	for i := 0; i < 10; i++ {
		text := fmt.Sprintf("msg %d", i)
		ex = append(ex, store.Example{Text: text, Answer: choice("share", 0.9), Label: "share"})
		s1[text] = choice("share", 0.70+float64(i)*0.025)
	}
	ex = append(ex, store.Example{Text: "odd", Answer: choice("share", 0.9), Label: "small_talk"})
	s1["odd"] = choice("share", 0.62)
	r, err := Run(context.Background(), fakeStore{examples: ex}, s1, set, 100)
	if err != nil {
		t.Fatal(err)
	}
	if r.Suggest != 0.65 {
		t.Errorf("suggested %.2f, want 0.65: the lowest threshold above the one wrong answer", r.Suggest)
	}
}
