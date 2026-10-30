// Package replay re-asks System 1 about messages the owner labelled (a
// correction in chat, or a label in the console) with the question set as it
// is now, so a change to a question, a threshold or the model can be checked
// before it is trusted. It reports how often the old and new answers match
// the labels, and suggests an act threshold; it never changes anything.
package replay

import (
	"context"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/Grandbusta/minime/internal/decision"
	"github.com/Grandbusta/minime/internal/decision/questions"
	"github.com/Grandbusta/minime/internal/providers/system1"
	"github.com/Grandbusta/minime/internal/store"
)

// Store has the labelled examples.
type Store interface {
	LabelledExamples(ctx context.Context, setID, question string, limit int) ([]store.Example, error)
}

// Row is one labelled message: what System 1 said then, and says now.
type Row struct {
	Text, Label string
	Then, Now   string // the answers (a choice, or "true" / "false")
	ThenConf    float64
	NowConf     float64
	ThenSetID   string // which version decided then
	Failed      error  // asking now failed
}

// Report is a replay's result.
type Report struct {
	Set     string // the set and version replayed, e.g. "incoming_message@2"
	Rows    []Row
	Act     float64 // the act threshold now
	Suggest float64 // a suggested act threshold; 0 if there's too little to go on
	CostUSD float64
}

// MinExamples is how many labels a threshold suggestion needs.
const MinExamples = 10

// Target is the share of answers at or above the act threshold that should be right.
const Target = 0.95

// Run replays up to limit of the newest labelled examples of set's primary question.
func Run(ctx context.Context, st Store, s1 system1.DecisionModel, set questions.Set, limit int) (Report, error) {
	r := Report{Set: set.Ref(), Act: set.Thresholds.Act}
	examples, err := st.LabelledExamples(ctx, set.ID, set.Primary, limit)
	if err != nil {
		return r, err
	}
	for _, e := range examples {
		row := Row{Text: e.Text, Label: e.Label, ThenSetID: e.SetID}
		row.Then, row.ThenConf = value(e.Answer)
		resp, err := s1.Decide(ctx, system1.Request{State: map[string]any{"message": e.Text}, Questions: set.Questions})
		if err != nil {
			row.Failed = err
		} else {
			r.CostUSD += resp.CostUSD
			row.Now, row.NowConf = value(resp.Answers[set.Primary])
		}
		r.Rows = append(r.Rows, row)
	}
	r.Suggest = suggest(r.Rows)
	return r, nil
}

// value is an answer as a label would say it, and how sure it was.
func value(a decision.Answer) (string, float64) {
	switch {
	case a.Refused:
		return "refused", 0
	case a.Type == decision.QuestionPredicate:
		if a.Probability >= 0.5 {
			return "true", a.Probability
		}
		return "false", 1 - a.Probability
	}
	return a.Choice, a.Confidence
}

// suggest is the lowest act threshold (in steps of 0.05) at which at least
// Target of the new answers at or above it are right: it acts on the most
// while staying right. 0 with fewer than MinExamples answers.
func suggest(rows []Row) float64 {
	var ok []Row
	for _, r := range rows {
		if r.Failed == nil {
			ok = append(ok, r)
		}
	}
	if len(ok) < MinExamples {
		return 0
	}
	for t := 0.50; t <= 0.99; t += 0.05 {
		acted, right := 0, 0
		for _, r := range ok {
			if r.NowConf >= t {
				acted++
				if r.Now == r.Label {
					right++
				}
			}
		}
		if acted > 0 && float64(right)/float64(acted) >= Target {
			return float64(int(t*100+0.5)) / 100
		}
	}
	return 0.99
}

// at counts the rows a threshold acts on, and how many of those are right.
func at(rows []Row, t float64, now bool) (acted, right int) {
	for _, r := range rows {
		conf, ans := r.ThenConf, r.Then
		if now {
			conf, ans = r.NowConf, r.Now
		}
		if r.Failed == nil && conf >= t {
			acted++
			if ans == r.Label {
				right++
			}
		}
	}
	return acted, right
}

// Write prints the report.
func (r Report) Write(w io.Writer) {
	if len(r.Rows) == 0 {
		fmt.Fprintf(w, "No labelled messages for %s yet. Correct Daniel in chat, or label decisions in the console.\n", r.Set)
		return
	}
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "MESSAGE\tLABEL\tTHEN\tNOW\t")
	thenRight, nowRight, failed := 0, 0, 0
	for _, row := range r.Rows {
		mark := func(ans string, conf float64) string {
			if ans == row.Label {
				return fmt.Sprintf("✓ %s %.2f", ans, conf)
			}
			return fmt.Sprintf("✗ %s %.2f", ans, conf)
		}
		now := mark(row.Now, row.NowConf)
		if row.Failed != nil {
			now, failed = "failed", failed+1
		}
		if row.Then == row.Label {
			thenRight++
		}
		if row.Failed == nil && row.Now == row.Label {
			nowRight++
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t\n", short(row.Text), row.Label, mark(row.Then, row.ThenConf), now)
	}
	tw.Flush()
	n := len(r.Rows)
	fmt.Fprintf(w, "\nRight then: %d/%d. Right now (%s): %d/%d", thenRight, n, r.Set, nowRight, n-failed)
	if failed > 0 {
		fmt.Fprintf(w, " (%d failed to ask)", failed)
	}
	fmt.Fprintf(w, ". Cost $%.5f.\n", r.CostUSD)
	acted, right := at(r.Rows, r.Act, true)
	fmt.Fprintf(w, "At act %.2f it acts on %d of them; %d right.\n", r.Act, acted, right)
	switch {
	case r.Suggest == 0:
		fmt.Fprintf(w, "Too few labels to suggest a threshold (%d; need %d).\n", n-failed, MinExamples)
	case r.Suggest == r.Act:
		fmt.Fprintln(w, "The act threshold looks right.")
	default:
		a, rt := at(r.Rows, r.Suggest, true)
		fmt.Fprintf(w, "Suggested act threshold: %.2f (it would act on %d; %d right). To use it, set thresholds.act in decisions/%s.yaml and bump its version.\n",
			r.Suggest, a, rt, strings.Split(r.Set, "@")[0])
	}
}

func short(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 48 {
		return string(r[:47]) + "…"
	}
	return s
}
