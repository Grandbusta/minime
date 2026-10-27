// Command minime runs the agent.
//
//	minime [run] [-config path]          run the agent
//	minime decisions [-n 20] [-config path]  show the latest decisions
package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"golang.org/x/sync/errgroup"

	"minime/internal/bus"
	"minime/internal/channel/telegram"
	"minime/internal/config"
	"minime/internal/decision"
	"minime/internal/decision/questions"
	"minime/internal/mind"
	"minime/internal/perception"
	"minime/internal/pricing"
	"minime/internal/providers/system1"
	s1openai "minime/internal/providers/system1/openai"
	"minime/internal/providers/system2"
	s2openai "minime/internal/providers/system2/openai"
	"minime/internal/self"
	"minime/internal/store"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "minime:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cmd := "run"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		cmd, args = args[0], args[1:]
	}
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	cfgPath := fs.String("config", "configs/minime.yaml", "path to the config file")
	limit := fs.Int("n", 20, "number of decisions to show")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return err
	}
	log, err := newLogger(cfg.Log.Level)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch cmd {
	case "run":
		if err := cfg.ValidateAgent(); err != nil {
			return err
		}
		// Who the agent is comes first: without a life story it does not start,
		// and the database is not touched.
		life, err := self.Load(cfg.Self.Life)
		if err != nil {
			return err
		}
		st, err := store.Open(ctx, cfg.Database.URL)
		if err != nil {
			return err
		}
		defer st.Close()
		return runAgent(ctx, cfg, life, st, log)
	case "decisions":
		st, err := store.Open(ctx, cfg.Database.URL)
		if err != nil {
			return err
		}
		defer st.Close()
		return printDecisions(ctx, st, *limit)
	default:
		return fmt.Errorf("unknown command %q (want run or decisions)", cmd)
	}
}

func runAgent(ctx context.Context, cfg *config.Config, life self.Life, st *store.Store, log *slog.Logger) error {
	s1, err := newDecisionModel(cfg.Decision.Provider, cfg.Pricing, log)
	if err != nil {
		return err
	}
	sets, err := questions.LoadDir(cfg.Decision.QuestionSets)
	if err != nil {
		return err
	}
	incoming, ok := sets["incoming_message"]
	if !ok {
		return fmt.Errorf("question set incoming_message not found in %s", cfg.Decision.QuestionSets)
	}
	followUp, ok := sets["follow_up"]
	if !ok {
		log.Warn("question set follow_up not found; replies to the agent's questions are routed on intent alone",
			"dir", cfg.Decision.QuestionSets)
	}
	lifeArea, ok := sets["life_area"]
	if !ok {
		log.Warn("question set life_area not found; messages and facts are not filed by life area",
			"dir", cfg.Decision.QuestionSets)
	} else if err := checkLifeAreaSet(lifeArea); err != nil {
		return err
	}
	personCircle, ok := sets["person_circle"]
	if !ok {
		log.Warn("question set person_circle not found; people are stored without a circle",
			"dir", cfg.Decision.QuestionSets)
	}
	factRelevance, ok := sets["fact_relevance"]
	if !ok {
		log.Warn("question set fact_relevance not found; System 2 replies without remembered facts",
			"dir", cfg.Decision.QuestionSets)
	} else if err := checkFactTemplate(factRelevance); err != nil {
		return err
	}
	openThreads, ok := sets["open_threads"]
	if !ok {
		log.Warn("question set open_threads not found; small talk recalls like any other message",
			"dir", cfg.Decision.QuestionSets)
	} else if err := checkFactTemplate(openThreads); err != nil {
		return err
	}
	s2, err := newReasoner(cfg.Reasoning, cfg.Pricing, log)
	if err != nil {
		return err
	}
	replyPrompt, err := loadPrompt(cfg.Reasoning.Prompt)
	if err != nil {
		return err
	}

	tg := telegram.NewClient(cfg.Telegram.Token)
	percepts := bus.New[perception.Percept](64)

	m := mind.New(mind.Config{
		Life:     life,
		Percepts: percepts.Subscribe(), Store: st, Replier: tg,
		System1: s1, System2: s2, Incoming: incoming, FollowUp: followUp, LifeArea: lifeArea, ReplyPrompt: replyPrompt,
		PersonCircle:  personCircle,
		FactRelevance: factRelevance, OpenThreads: openThreads, RecallCandidates: cfg.Memory.RecallCandidates,
		Working: mind.WorkingMemory{Turns: cfg.WorkingMemory.Turns, Window: cfg.WorkingMemory.Window},
		Log:     log.With("component", "mind"),
	})
	sensor := telegram.NewSensor(tg, cfg.Telegram.OwnerIDs, cfg.Telegram.PollTimeoutSeconds, percepts,
		log.With("component", "telegram"))

	log.Info("mini-me awake", "name", life.Name, "life_story_lines", len(life.Story), "owners", len(cfg.Telegram.OwnerIDs),
		"system1", cfg.Decision.Provider.Provider, "question_set", incoming.Ref(), "follow_up", followUp.Ref(),
		"life_area", lifeArea.Ref(), "person_circle", personCircle.Ref(), "fact_relevance", factRelevance.Ref(),
		"open_threads", openThreads.Ref(),
		"system2", cfg.Reasoning.Provider.Provider+"/"+cfg.Reasoning.Model, "effort", cfg.Reasoning.Effort, "prompt", replyPrompt.Ref)
	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error { return sensor.Run(ctx) })
	g.Go(func() error { return m.Run(ctx) })
	err = g.Wait()
	log.Info("mini-me asleep")
	return err
}

// newDecisionModel picks the System 1 provider named in config. Adding a
// provider means one more case here and a package under providers/system1.
func newDecisionModel(c config.Provider, prices map[string]pricing.Price, log *slog.Logger) (system1.DecisionModel, error) {
	switch c.Provider {
	case "openai":
		model := c.Model
		if model == "" {
			model = s1openai.DefaultModel
		}
		return s1openai.New(c.APIKey, model, c.BaseURL, priceFor(model, prices, log)), nil
	case "typesafe":
		return nil, errors.New("decision provider typesafe (Jev) is not implemented yet")
	default:
		return nil, fmt.Errorf("unknown decision provider %q (want openai)", c.Provider)
	}
}

// newReasoner picks the System 2 provider named in config, the same way.
func newReasoner(c config.Reasoning, prices map[string]pricing.Price, log *slog.Logger) (system2.Reasoner, error) {
	switch c.Provider.Provider {
	case "openai":
		return s2openai.New(c.APIKey, c.Model, c.BaseURL, priceFor(c.Model, prices, log), c.Effort), nil
	default:
		return nil, fmt.Errorf("unknown reasoning provider %q (want openai)", c.Provider.Provider)
	}
}

// priceFor looks a model up in the pricing table. A missing model still runs,
// but its decisions are recorded at $0, so say so once at startup.
func priceFor(model string, prices map[string]pricing.Price, log *slog.Logger) pricing.Price {
	p, ok := prices[model]
	if !ok {
		log.Warn("no price for model in config pricing table; its cost is recorded as $0", "model", model)
	}
	return p
}

// checkFactTemplate makes sure fact_relevance is the one-question template
// the mind expands per fact.
func checkFactTemplate(s questions.Set) error {
	if len(s.Questions) != 1 || s.Questions[0].Type != decision.QuestionPredicate ||
		!strings.Contains(s.Questions[0].Instructions, "{fact}") {
		return fmt.Errorf("question set %s must be exactly one predicate whose instructions contain {fact}", s.Ref())
	}
	return nil
}

// checkLifeAreaSet makes sure life_area has the questions the mind reads:
// an `area` choice and the `personal` and `broad` predicates.
func checkLifeAreaSet(s questions.Set) error {
	want := map[string]decision.QuestionType{
		"area": decision.QuestionChoice, "personal": decision.QuestionPredicate, "broad": decision.QuestionPredicate,
	}
	for _, q := range s.Questions {
		if t, ok := want[q.Name]; ok && t == q.Type {
			delete(want, q.Name)
		}
	}
	if len(want) > 0 || s.Thresholds.Label <= 0 {
		return fmt.Errorf("question set %s needs an `area` choice, `personal` and `broad` predicates, and thresholds.label > 0", s.Ref())
	}
	return nil
}

// loadPrompt reads a prompt file. Its ref is the file name plus a short hash
// of the text, so every edit shows up as a new version in the decision log.
func loadPrompt(path string) (mind.Prompt, error) {
	text, err := os.ReadFile(path)
	if err != nil {
		return mind.Prompt{}, fmt.Errorf("read prompt: %w", err)
	}
	sum := sha256.Sum256(text)
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	return mind.Prompt{Ref: fmt.Sprintf("%s@%x", name, sum[:4]), Text: string(text)}, nil
}

func printDecisions(ctx context.Context, st *store.Store, n int) error {
	ds, err := st.RecentDecisions(ctx, n)
	if err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "AT\tTIER\tSET\tPROVIDER\tACTION\tCONFIDENCE\tGATE\tOUTCOME\tLATENCY\tCOST\tREPLY")
	for _, d := range ds {
		outcome, provider, confidence := "-", "-", "-"
		if d.Outcome != nil {
			outcome = string(d.Outcome.Status)
		}
		if d.Provider != "" {
			provider = d.Provider
		}
		for _, a := range d.Answers {
			if a.Type == decision.QuestionChoice && !a.Refused {
				confidence = fmt.Sprintf("%.2f", a.Confidence)
				break
			}
		}
		reply := strings.Join(strings.Fields(d.Reply), " ") // one line
		if r := []rune(reply); len(r) > 60 {
			reply = string(r[:59]) + "…"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t$%.6f\t%s\n", d.At.Local().Format("2006-01-02 15:04:05"),
			d.Tier, d.SetID, provider, d.Action, confidence, d.Gate, outcome, d.Latency.Round(time.Millisecond), d.CostUSD, reply)
	}
	return w.Flush()
}

func newLogger(level string) (*slog.Logger, error) {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("config: log.level: %w", err)
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l})), nil
}
