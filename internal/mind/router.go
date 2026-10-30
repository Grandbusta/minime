package mind

import (
	"github.com/Grandbusta/minime/internal/decision"
	"github.com/Grandbusta/minime/internal/decision/questions"
)

type routeKind string

const (
	routeAct      routeKind = "act"      // confident enough to act on the choice
	routeAsk      routeKind = "ask"      // plausible, but not certain
	routeEscalate routeKind = "escalate" // too unsure (or refused): needs System 2
)

type route struct {
	Kind       routeKind
	Choice     string // the primary answer's choice ("" if refused or missing)
	Confidence float64
}

// routeFor applies the set's thresholds to its primary answer. This is the
// metacognition step: how sure is System 1, and is that sure enough?
func routeFor(set questions.Set, answers map[string]decision.Answer) route {
	a, ok := answers[set.Primary]
	if !ok || a.Refused {
		return route{Kind: routeEscalate}
	}
	r := route{Choice: a.Choice, Confidence: a.Confidence}
	switch {
	case a.Confidence >= set.Thresholds.Act:
		r.Kind = routeAct
	case a.Confidence >= set.Thresholds.Ask:
		r.Kind = routeAsk
	default:
		r.Kind = routeEscalate
	}
	return r
}

// holds reports whether a set's yes/no primary question came back yes with
// at least its act threshold of probability. A refusal or a missing answer is no.
func holds(set questions.Set, answers map[string]decision.Answer) bool {
	return isYes(set, answers, set.Primary)
}

// isYes reports whether yes/no question name came back yes with at least
// the set's act threshold of probability.
func isYes(set questions.Set, answers map[string]decision.Answer, name string) bool {
	a, ok := answers[name]
	return ok && !a.Refused && a.Probability >= set.Thresholds.Act
}
