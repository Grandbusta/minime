package mind

import "testing"

func TestResolvePronouns(t *testing.T) {
	cases := map[string]string{
		"What is your church?":             "What is Tobi's church?",
		"What is my church?":               "What is Sam's church?",
		"I'm 40 years old":                 "Sam is 40 years old",
		"Is this book yours or mine?":      "Is this book Tobi's or Sam's?",
		"Imagine Iceland":                  "Imagine Iceland", // "I" only as a whole word
		"Tobi's favourite colour is green": "Tobi's favourite colour is green",
	}
	for in, want := range cases {
		if got := resolvePronouns(in, "Sam", "Tobi"); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
}
