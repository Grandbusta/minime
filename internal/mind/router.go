package mind

import (
	"github.com/Grandbusta/minime/internal/decision"
	"github.com/Grandbusta/minime/internal/decision/questions"
)

type routeKind string

const (
	routeAct      routeKind = "act"
	routeAsk      routeKind = "ask"
	routeEscalate routeKind = "escalate" // too unsure (or refused): needs System 2
)

type route struct {
	Kind       routeKind
	Choice     string // the primary answer's choice ("" if refused or missing)
	Confidence float64
}

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

// isYes needs at least the set's act threshold of probability.
func isYes(set questions.Set, answers map[string]decision.Answer, name string) bool {
	a, ok := answers[name]
	return ok && !a.Refused && a.Probability >= set.Thresholds.Act
}
