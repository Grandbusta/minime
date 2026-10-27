package decision

import (
	"encoding/json"
	"testing"
)

func TestAnswerJSONKeepsZeros(t *testing.T) {
	cases := map[string]Answer{
		`{"probability":0,"type":"predicate"}`: {Type: QuestionPredicate},
		`{"choice":"a","confidence":0,"probabilities":{"a":0.5},"type":"choice"}`: {
			Type: QuestionChoice, Choice: "a", Probabilities: map[string]float64{"a": 0.5}},
		`{"confidence":0.4,"probabilities":null,"score":0,"type":"score"}`: {Type: QuestionScore, Confidence: 0.4},
		`{"refused":true,"type":"choice"}`:                                 {Type: QuestionChoice, Refused: true, Choice: "ignored"},
	}
	for want, a := range cases {
		got, err := json.Marshal(a)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Errorf("got  %s\nwant %s", got, want)
		}
		var back Answer
		if err := json.Unmarshal(got, &back); err != nil || back.Type != a.Type || back.Refused != a.Refused {
			t.Errorf("round trip of %s: %+v (%v)", got, back, err)
		}
	}
}
