// Package portfolio is the agent's public profile page.
//
// Only facts about itself can reach the page, and System 1 first decides,
// fact by fact, whether each may be public (public_profile). System 2 then
// writes the page from its life story and those facts, again only when they
// change. Works are decided the same way (public_work) and shown as they are.
// Its journal and what it made for someone are never public.
package portfolio

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/Grandbusta/minime/internal/action"
	"github.com/Grandbusta/minime/internal/decision"
	"github.com/Grandbusta/minime/internal/decision/questions"
	"github.com/Grandbusta/minime/internal/memory"
	"github.com/Grandbusta/minime/internal/mind"
	"github.com/Grandbusta/minime/internal/providers/system1"
	"github.com/Grandbusta/minime/internal/providers/system2"
	"github.com/Grandbusta/minime/internal/self"
)

type Page struct {
	Tagline   string   `json:"tagline"`
	About     []string `json:"about"`
	Interests []string `json:"interests"`
	Now       []string `json:"now"` // may be empty
}

var pageFormat = &system2.Format{Name: "profile_page", Schema: map[string]any{
	"type": "object", "additionalProperties": false, "required": []string{"tagline", "about", "interests", "now"},
	"properties": map[string]any{
		"tagline":   map[string]any{"type": "string"},
		"about":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"interests": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		"now":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
	},
}}

type Store interface {
	UndecidedPublicFacts(ctx context.Context, limit int) ([]memory.Fact, error)
	SetFactPublic(ctx context.Context, id ulid.ULID, public bool) error
	PublicFacts(ctx context.Context, limit int) ([]memory.Fact, error)
	Profile(ctx context.Context) (page []byte, inputs string, at time.Time, ok bool, err error)
	SaveProfile(ctx context.Context, page []byte, inputs string, at time.Time) error
	SaveDecision(ctx context.Context, d decision.Decision) error
	UndecidedPublicWorks(ctx context.Context, limit int) ([]memory.Work, error)
	SetWorkPublic(ctx context.Context, id ulid.ULID, public bool, by string) (bool, error)
	AwaitWorkPublic(ctx context.Context, id ulid.ULID) error
	SaveApproval(ctx context.Context, a memory.Approval) error
	AutonomyRules(ctx context.Context) (map[string]string, error)
}

const (
	factsPerCheck = 100 // in one System 1 call
	pageFacts     = 30
	worksPerCheck = 20
	workChars     = 2000
)

// Writer keeps the page up to date: once a day at UpdateAt (UTC), and on
// start only if the page has never been written.
type Writer struct {
	Life    self.Life
	Store   Store
	System1 system1.DecisionModel
	System2 system2.Reasoner
	// Public and PublicWork zero: nothing is ever public.
	Public     questions.Set
	PublicWork questions.Set
	// Autonomy zero: a work System 1 judged public goes on the page on its own.
	Autonomy action.Policy
	Prompt   mind.Prompt
	UpdateAt time.Duration // since midnight UTC
	Log      *slog.Logger
}

func (w *Writer) Run(ctx context.Context) error {
	if _, _, _, ok, err := w.Store.Profile(ctx); err != nil {
		w.Log.Warn("checking for the profile page failed", "err", err)
	} else if !ok {
		w.refresh(ctx)
	}
	for {
		next := nextUpdate(time.Now(), w.UpdateAt)
		w.Log.Info("next profile page update", "at", next.Format(time.RFC3339))
		t := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-t.C:
			w.refresh(ctx)
		}
	}
}

func nextUpdate(now time.Time, at time.Duration) time.Time {
	now = now.UTC()
	next := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).Add(at)
	if !next.After(now) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}

func (w *Writer) refresh(ctx context.Context) {
	if _, err := w.Update(ctx, false); err != nil {
		w.Log.Warn("updating the profile page failed; it keeps its last version", "err", err)
	}
}

// Update rewrites the page only if the public facts changed, or always with
// force. It reports whether the page was rewritten.
func (w *Writer) Update(ctx context.Context, force bool) (bool, error) {
	if err := w.decidePublic(ctx); err != nil {
		w.Log.Warn("deciding which facts are public failed; the page uses what was decided before", "err", err)
	}
	if err := w.decidePublicWorks(ctx); err != nil {
		w.Log.Warn("deciding which works are public failed; the page shows what was decided before", "err", err)
	}
	return w.write(ctx, force)
}

// decidePublic counts a refusal as no.
func (w *Writer) decidePublic(ctx context.Context) error {
	if w.Public.ID == "" {
		return nil
	}
	facts, err := w.Store.UndecidedPublicFacts(ctx, factsPerCheck)
	if err != nil || len(facts) == 0 {
		return err
	}
	start := time.Now()
	d := w.newDecision(start, decision.TierSystem1, w.Public.Ref(), "decide_public")
	template := w.Public.Questions[0]
	texts := make([]string, len(facts))
	qs := make([]system1.Question, len(facts))
	for i, f := range facts {
		texts[i] = f.Text
		q := template
		q.Name = f.ID.String() // answers come back keyed by fact ID
		q.Instructions = strings.NewReplacer("{fact}", fmt.Sprintf("`facts[%d]`", i), "{agent}", w.Life.Name).
			Replace(template.Instructions)
		qs[i] = q
	}
	resp, err := w.System1.Decide(ctx, system1.Request{State: map[string]any{"facts": texts}, Questions: qs})
	if err != nil {
		return err
	}
	d.Provider, d.Answers, d.CostUSD = resp.Provider, resp.Answers, resp.CostUSD

	public := 0
	for _, f := range facts {
		a, ok := resp.Answers[f.ID.String()]
		if !ok {
			continue // asked again next time
		}
		yes := !a.Refused && a.Probability >= w.Public.Thresholds.Act
		if err := w.Store.SetFactPublic(ctx, f.ID, yes); err != nil {
			return err
		}
		if yes {
			public++
		}
	}
	w.finish(ctx, d, start, fmt.Sprintf("%d of %d facts may be public", public, len(facts)))
	return nil
}

// decidePublicWorks asks each work in its own call: asked together, a harmless
// work next to a private one was dragged down with it. A refusal counts as no.
func (w *Writer) decidePublicWorks(ctx context.Context) error {
	if w.PublicWork.ID == "" {
		return nil
	}
	works, err := w.Store.UndecidedPublicWorks(ctx, worksPerCheck)
	if err != nil || len(works) == 0 {
		return err
	}
	start := time.Now()
	d := w.newDecision(start, decision.TierSystem1, w.PublicWork.Ref(), "decide_public_works")
	d.Answers = map[string]decision.Answer{}
	q := w.PublicWork.Questions[0]
	q.Instructions = strings.NewReplacer("{work}", "`work`", "{agent}", w.Life.Name).Replace(q.Instructions)
	public, waiting, asked := 0, 0, 0
	policy := w.Autonomy
	if rules, err := w.Store.AutonomyRules(ctx); err == nil {
		policy = policy.WithChatRules(rules)
	} else {
		w.Log.Warn("loading the owner's permission changes failed; using the config's", "err", err)
	}
	var failed error
	for _, work := range works {
		text := work.Text
		if r := []rune(text); len(r) > workChars {
			text = string(r[:workChars])
		}
		resp, err := w.System1.Decide(ctx, system1.Request{
			State:     map[string]any{"work": map[string]string{"title": work.Title, "kind": work.Kind, "text": text}},
			Questions: []system1.Question{q},
		})
		if err != nil {
			failed = err
			continue
		}
		d.Provider, d.CostUSD = resp.Provider, d.CostUSD+resp.CostUSD
		a, ok := resp.Answers[q.Name]
		if !ok {
			continue
		}
		d.Answers[work.ID.String()] = a
		asked++
		yes := !a.Refused && a.Probability >= w.PublicWork.Thresholds.Act
		verdict := action.Auto
		if yes {
			verdict = policy.Check(action.Publish, "")
		}
		switch verdict {
		case action.Ask:
			if err := w.askToPublish(ctx, work); err != nil {
				return err
			}
			waiting++
		case action.Forbid:
			if _, err := w.Store.SetWorkPublic(ctx, work.ID, false, memory.PublicByGate); err != nil {
				return err
			}
		default:
			if _, err := w.Store.SetWorkPublic(ctx, work.ID, yes, memory.PublicBySystem1); err != nil {
				return err
			}
			if yes {
				public++
			}
		}
	}
	if asked > 0 {
		detail := fmt.Sprintf("%d of %d works may be public", public, asked)
		if waiting > 0 {
			detail += fmt.Sprintf("; %d waiting for the owner's OK", waiting)
		}
		w.finish(ctx, d, start, detail)
	}
	return failed
}

func (w *Writer) askToPublish(ctx context.Context, work memory.Work) error {
	if err := w.Store.AwaitWorkPublic(ctx, work.ID); err != nil {
		return err
	}
	return w.Store.SaveApproval(ctx, memory.Approval{ID: ulid.Make(), Class: string(action.Publish), Ref: work.ID,
		What:   fmt.Sprintf("%s (%s)\n\n%s", work.Title, strings.ReplaceAll(work.Kind, "_", " "), excerpt(work.Text)),
		Status: memory.ApprovalPending, AskedAt: time.Now().UTC()})
}

func (w *Writer) write(ctx context.Context, force bool) (bool, error) {
	facts, err := w.Store.PublicFacts(ctx, pageFacts)
	if err != nil {
		return false, err
	}
	instructions := w.instructions(facts)
	sum := sha256.Sum256([]byte(instructions))
	inputs := fmt.Sprintf("%s:%x", w.Prompt.Ref, sum[:8])
	_, last, _, ok, err := w.Store.Profile(ctx)
	if err != nil {
		return false, err
	}
	if ok && last == inputs && !force {
		return false, nil
	}

	start := time.Now()
	d := w.newDecision(start, decision.TierSystem2, w.Prompt.Ref, "write_profile")
	resp, err := w.System2.Reason(ctx, system2.Request{
		Instructions: instructions,
		Message:      "Write your profile page.",
		Format:       pageFormat,
	})
	if err != nil {
		return false, err
	}
	d.Provider, d.CostUSD = resp.Provider, resp.CostUSD
	var page Page
	if err := json.Unmarshal([]byte(resp.Text), &page); err != nil {
		return false, fmt.Errorf("profile page is not the requested JSON: %w", err)
	}
	page = page.tidy()
	if len(facts) == 0 {
		// "Lately" is only ever what the agent actually said it is up to; without
		// facts, System 2 filled it from the life story in testing.
		page.Now = nil
	}
	if len(page.About) == 0 {
		return false, fmt.Errorf("profile page has no about text")
	}
	raw, err := json.Marshal(page)
	if err != nil {
		return false, err
	}
	if err := w.Store.SaveProfile(ctx, raw, inputs, time.Now().UTC()); err != nil {
		return false, err
	}
	d.Reply = page.Tagline + "\n\n" + strings.Join(page.About, "\n\n")
	w.finish(ctx, d, start, fmt.Sprintf("written from the life story and %d public facts", len(facts)))
	w.Log.Info("profile page written", "public_facts", len(facts), "cost_usd", resp.CostUSD)
	return true, nil
}

func (w *Writer) instructions(facts []memory.Fact) string {
	list := func(lines []string, none string) string {
		if len(lines) == 0 {
			return none
		}
		return "- " + strings.Join(lines, "\n- ")
	}
	shared := make([]string, len(facts))
	for i, f := range facts {
		shared[i] = f.At.Format("2 Jan 2006") + ": " + f.Text
	}
	return strings.NewReplacer("{name}", w.Life.Name,
		"{life_story}", list(w.Life.Story, "- (none)"),
		"{personality}", list(w.Life.Personality, "- Be yourself."),
		"{facts}", list(shared, "- (nothing yet beyond your life story)")).Replace(w.Prompt.Text)
}

func (p Page) tidy() Page {
	clean := func(lines []string, max int) []string {
		var out []string
		for _, s := range lines {
			if s = strings.TrimSpace(s); s != "" && len(out) < max {
				out = append(out, s)
			}
		}
		return out
	}
	return Page{Tagline: strings.TrimSpace(p.Tagline), About: clean(p.About, 3),
		Interests: clean(p.Interests, 6), Now: clean(p.Now, 6)}
}

func (w *Writer) newDecision(at time.Time, tier decision.Tier, setID, action string) *decision.Decision {
	return &decision.Decision{ID: ulid.Make(), At: at.UTC(), Tier: tier, SetID: setID, Action: action, Gate: decision.GateAuto}
}

func (w *Writer) finish(ctx context.Context, d *decision.Decision, start time.Time, detail string) {
	now := time.Now()
	d.Latency = now.Sub(start)
	d.Outcome = &decision.Outcome{Status: decision.OutcomeOK, Detail: detail, At: now.UTC()}
	if err := w.Store.SaveDecision(ctx, *d); err != nil {
		w.Log.Error("save decision", "err", err)
	}
}
