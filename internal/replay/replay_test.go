package replay

import (
	"context"
	"fmt"
	"strings"
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

func TestReplayComparesThenAndNow(t *testing.T) {
	st := fakeStore{examples: []store.Example{
		{Text: "I need a dentist", SetID: "incoming_message@1", Answer: choice("share", 0.6), Label: "ask_for_help"},
		{Text: "Hi", SetID: "incoming_message@1", Answer: choice("small_talk", 0.95), Label: "small_talk"},
		{Text: "gone", SetID: "incoming_message@1", Answer: choice("share", 0.9), Label: "share"},
	}}
	s1 := fakeS1{"I need a dentist": choice("ask_for_help", 0.9), "Hi": choice("small_talk", 0.97)}
	r, err := Run(context.Background(), st, s1, set, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Rows) != 3 || r.Rows[0].Now != "ask_for_help" || r.Rows[0].Then != "share" || r.Rows[2].Failed == nil || r.Suggest != 0 {
		t.Fatalf("report = %+v", r)
	}
	var b strings.Builder
	r.Write(&b)
	out := b.String()
	for _, want := range []string{"✗ share 0.60", "✓ ask_for_help 0.90", "Right then: 2/3. Right now (incoming_message@2): 2/2 (1 failed to ask)",
		"At act 0.85 it acts on 2 of them; 2 right.", "Too few labels to suggest a threshold (2; need 10)."} {
		if !strings.Contains(out, want) {
			t.Errorf("report is missing %q:\n%s", want, out)
		}
	}
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
	var b strings.Builder
	r.Write(&b)
	if !strings.Contains(b.String(), "Suggested act threshold: 0.65 (it would act on 10; 10 right)") {
		t.Errorf("report:\n%s", b.String())
	}
}

func TestReplayWithoutLabels(t *testing.T) {
	r, _ := Run(context.Background(), fakeStore{}, fakeS1{}, set, 100)
	var b strings.Builder
	r.Write(&b)
	if !strings.Contains(b.String(), "No labelled messages for incoming_message@2 yet") {
		t.Errorf("report:\n%s", b.String())
	}
}
