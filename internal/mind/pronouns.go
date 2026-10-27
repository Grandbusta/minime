package mind

import "regexp"

// A reflex, not a model call: people resolve "I" and "you" by grammar, and
// the decision model is unreliable at it (in testing, 6–10 of 15 messages
// put "your church" or "my church" in the wrong life). With names in place
// of pronouns it scored "What is Tobi's church?" and "What is Sam's
// church?" correctly every time.

type pronounRule struct {
	re   *regexp.Regexp
	repl string // $1 = first person, $2 = second person, $3 = both
}

// Contractions first, then possessives before plain forms. Apostrophes may be ' or ’.
var pronounRules = func() []pronounRule {
	rules := []struct{ pattern, repl string }{
		// second person → the one addressed
		{`(?i)\byou['’]re\b`, "$2 is"},
		{`(?i)\byou['’]ve\b`, "$2 has"},
		{`(?i)\byou['’]ll\b`, "$2 will"},
		{`(?i)\byou['’]d\b`, "$2 would"},
		{`(?i)\byourself\b`, "$2"},
		{`(?i)\byours\b`, "$2's"},
		{`(?i)\byour\b`, "$2's"},
		{`(?i)\byou\b`, "$2"},
		// first person plural → both
		{`(?i)\bwe['’]re\b`, "$3 are"},
		{`(?i)\bourselves\b`, "$3"},
		{`(?i)\bours\b`, "$3's"},
		{`(?i)\bour\b`, "$3's"},
		{`(?i)\bus\b`, "$3"},
		{`(?i)\bwe\b`, "$3"},
		// first person singular → the one speaking ("I" is always upper case)
		{`\bI['’]m\b`, "$1 is"},
		{`\bI am\b`, "$1 is"},
		{`\bI['’]ve\b`, "$1 has"},
		{`\bI['’]ll\b`, "$1 will"},
		{`\bI['’]d\b`, "$1 would"},
		{`\bI\b`, "$1"},
		{`(?i)\bmyself\b`, "$1"},
		{`(?i)\bmine\b`, "$1's"},
		{`(?i)\bmy\b`, "$1's"},
		{`(?i)\bme\b`, "$1"},
	}
	out := make([]pronounRule, len(rules))
	for i, r := range rules {
		out[i] = pronounRule{regexp.MustCompile(r.pattern), r.repl}
	}
	return out
}()

// resolvePronouns rewrites text so it names people instead of pointing at
// them: "I/me/my" become firstPerson, "you/your" secondPerson, and
// "we/us/our" both of them. Text without pronouns comes back unchanged.
func resolvePronouns(text, firstPerson, secondPerson string) string {
	both := firstPerson + " and " + secondPerson
	for _, r := range pronounRules {
		text = r.re.ReplaceAllStringFunc(text, func(string) string {
			return expand(r.repl, firstPerson, secondPerson, both)
		})
	}
	return text
}

func expand(repl, first, second, both string) string {
	out := make([]byte, 0, len(repl)+len(both))
	for i := 0; i < len(repl); i++ {
		if repl[i] == '$' && i+1 < len(repl) {
			switch repl[i+1] {
			case '1':
				out, i = append(out, first...), i+1
				continue
			case '2':
				out, i = append(out, second...), i+1
				continue
			case '3':
				out, i = append(out, both...), i+1
				continue
			}
		}
		out = append(out, repl[i])
	}
	return string(out)
}
