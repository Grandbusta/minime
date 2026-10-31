package mind

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Grandbusta/minime/internal/ability"
	"github.com/Grandbusta/minime/internal/memory"
	"github.com/Grandbusta/minime/internal/providers/system2"
)

// maxSteps is the calls one activity can make to chained abilities (tools),
// the first included: enough to search, then read what it found.
const maxSteps = 3

const stepInstructions = `You are %s, in the middle of something you planned to do: the note says what, and what your ` +
	`tools have returned so far. If you need one more thing from a tool before you can do it well, like reading a file a ` +
	`search found, give that tool's name as ability and its query. If you have what you need, leave ability empty. Use a ` +
	`tool only for what this needs.`

// followUp lets an activity that used a chained ability use up to
// maxSteps-1 more of those it chains to, each chosen by System 2 from what
// the calls so far returned, before it writes. It returns what they added
// and what choosing cost.
func (m *Mind) followUp(ctx context.Context, i memory.Intention, note string, ch ability.Chained, project *memory.Project,
	now time.Time) (string, float64) {
	names := []string{""}
	next := map[string]ability.Ability{}
	var list strings.Builder
	for _, a := range ch.Next() {
		if m.allowed(a) {
			names, next[a.Name()] = append(names, a.Name()), a
			fmt.Fprintf(&list, "\n- %s: %s", a.Name(), a.Describe())
		}
	}
	if len(names) == 1 {
		return "", 0
	}
	format := &system2.Format{Name: "next_step", Schema: map[string]any{"type": "object", "additionalProperties": false,
		"required": []string{"ability", "query"}, "properties": map[string]any{
			"ability": map[string]any{"type": "string", "enum": names, "description": "the tool to use next; empty if you have what you need"},
			"query":   map[string]any{"type": "string", "description": "what to give it, as its description says"},
		}}}
	log := m.Log.With("intention", i.ID.String())
	var added strings.Builder
	var cost float64
	for step := 1; step < maxSteps; step++ {
		resp, err := m.System2.Reason(ctx, system2.Request{
			Instructions: fmt.Sprintf(stepInstructions, m.Life.Name),
			Note:         note + added.String() + "\n\nYour tools:" + list.String(),
			Format:       format,
		})
		if err != nil {
			log.Warn("choosing the next step failed; it writes with what it has", "err", err)
			break
		}
		cost += resp.CostUSD
		var chose struct {
			Ability string `json:"ability"`
			Query   string `json:"query"`
		}
		if json.Unmarshal([]byte(resp.Text), &chose) != nil {
			break
		}
		a, ok := next[chose.Ability]
		if !ok { // empty: it has what it needs
			break
		}
		mat, err := a.Prepare(ctx, ability.Act{Intention: memory.Intention{ID: i.ID, Text: i.Text, Ability: a.Name(),
			Args: map[string]string{"query": chose.Query}}, Project: project, Now: now, Zone: m.Zone})
		if err != nil {
			log.Warn("a next step failed", "ability", a.Name(), "query", chose.Query, "err", err)
			fmt.Fprintf(&added, "\n\nYou then tried %s with %s; it didn't work: %s", a.Name(), chose.Query, err)
			continue
		}
		log.Info("took a next step", "ability", a.Name(), "query", chose.Query)
		added.WriteString(mat.Note)
	}
	return added.String(), cost
}
