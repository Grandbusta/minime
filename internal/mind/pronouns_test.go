package mind

import "testing"

func TestResolvePronouns(t *testing.T) {
	cases := map[string]string{
		"What is your church?":             "What is Tobi's church?",
		"What is my church?":               "What is Sam's church?",
		"I'm 40 years old":                 "Sam is 40 years old",
		"I am a male":                      "Sam is a male",
		"I live with my parents":           "Sam live with Sam's parents",
		"Tell me about yourself":           "Tell Sam about Tobi",
		"What do you know about me?":       "What do Tobi know about Sam?",
		"We should go to church together":  "Sam and Tobi should go to church together",
		"Is this book yours or mine?":      "Is this book Tobi's or Sam's?",
		"You’re kind and I’ve noticed":     "Tobi is kind and Sam has noticed",
		"What's the capital of France?":    "What's the capital of France?",
		"Imagine Iceland":                  "Imagine Iceland", // "I" only as a whole word
		"Tobi's favourite colour is green": "Tobi's favourite colour is green",
	}
	for in, want := range cases {
		if got := resolvePronouns(in, "Sam", "Tobi"); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
}
