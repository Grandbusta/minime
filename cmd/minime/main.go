// Command minime runs the agent.
//
//	minime [run] [-config path]          run the agent
//	minime decisions [-n 20] [-config path]  show the latest decisions
//	minime profile [-force] [-config path]   update the profile page now
//	minime day [-date YYYY-MM-DD] [-config path]  what it planned and made that day
package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"maps"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	_ "time/tzdata" // time zones work without the system's database, e.g. in a slim container

	"golang.org/x/sync/errgroup"

	"minime/internal/ability"
	"minime/internal/ability/journal"
	"minime/internal/ability/reading"
	"minime/internal/bus"
	"minime/internal/channel/telegram"
	"minime/internal/config"
	"minime/internal/console"
	"minime/internal/decision"
	"minime/internal/decision/questions"
	"minime/internal/library"
	"minime/internal/memory"
	"minime/internal/mind"
	"minime/internal/perception"
	"minime/internal/portfolio"
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
	force := fs.Bool("force", false, "profile: rewrite the page even if nothing changed")
	date := fs.String("date", "", "day: the day to show, YYYY-MM-DD (default today)")
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
	case "profile":
		if err := cfg.ValidateAgent(); err != nil {
			return err
		}
		life, err := self.Load(cfg.Self.Life)
		if err != nil {
			return err
		}
		st, err := store.Open(ctx, cfg.Database.URL)
		if err != nil {
			return err
		}
		defer st.Close()
		return updateProfile(ctx, cfg, life, st, *force, log)
	case "day":
		zone, err := cfg.Initiative.Zone()
		if err != nil {
			return err
		}
		st, err := store.Open(ctx, cfg.Database.URL)
		if err != nil {
			return err
		}
		defer st.Close()
		return printDay(ctx, st, zone, *date)
	default:
		return fmt.Errorf("unknown command %q (want run, decisions, profile or day)", cmd)
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
	var profile *portfolio.Writer
	if cfg.Portfolio.Addr != "" {
		if profile, err = newProfileWriter(cfg, life, st, s1, s2, sets, log.With("component", "portfolio")); err != nil {
			return err
		}
	}

	zone, err := cfg.Initiative.Zone()
	if err != nil {
		return err
	}
	checkIns, err := cfg.Initiative.CheckInTimes()
	if err != nil {
		return err
	}
	ticks := make(chan time.Time)
	// What it can do in its day beyond writing: the journal, and those switched on.
	var names []string
	if cfg.DailyLife.Enabled {
		names = cfg.DailyLife.AbilityNames()
	}
	abilitySet, err := newAbilities(names, st)
	if err != nil {
		return err
	}
	var day mind.Day
	if dl := cfg.DailyLife; dl.Enabled {
		if day.PlanAt, day.JournalAt, err = dl.Times(); err != nil {
			return err
		}
		if day.PlanPrompt, err = loadPrompt(dl.PlanPrompt); err != nil {
			return err
		}
		if day.ActivityPrompt, err = loadPrompt(dl.ActivityPrompt); err != nil {
			return err
		}
		day.MaxActivities, day.BudgetUSD = dl.Activities, dl.BudgetUSD
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
		Ticks:   ticks, Zone: zone, CheckIns: mind.CheckIns{Times: checkIns, Quiet: cfg.Initiative.Quiet}, Day: day,
		Abilities: abilitySet,
		Log:       log.With("component", "mind"),
	})
	sensor := telegram.NewSensor(tg, cfg.Telegram.OwnerIDs, cfg.Telegram.PollTimeoutSeconds, percepts,
		log.With("component", "telegram"))

	log.Info("mini-me awake", "name", life.Name, "life_story_lines", len(life.Story), "owners", len(cfg.Telegram.OwnerIDs),
		"system1", cfg.Decision.Provider.Provider, "question_set", incoming.Ref(), "follow_up", followUp.Ref(),
		"life_area", lifeArea.Ref(), "person_circle", personCircle.Ref(), "fact_relevance", factRelevance.Ref(),
		"open_threads", openThreads.Ref(), "timezone", zone.String(), "check_ins", cfg.Initiative.CheckIns,
		"daily_life", cfg.DailyLife.Enabled, "abilities", abilitySet.Names(), "routine_lines", len(life.Routine), "makes", len(life.Makes),
		"system2", cfg.Reasoning.Provider.Provider+"/"+cfg.Reasoning.Model, "effort", cfg.Reasoning.Effort, "prompt", replyPrompt.Ref)
	started := time.Now()
	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error { return sensor.Run(ctx) })
	g.Go(func() error { return m.Run(ctx) })
	g.Go(func() error { return runClock(ctx, ticks) })
	if cfg.Console.Addr != "" {
		sets := []string{incoming.Ref(), followUp.Ref(), lifeArea.Ref(), personCircle.Ref(), factRelevance.Ref(),
			openThreads.Ref(), replyPrompt.Ref, day.PlanPrompt.Ref, day.ActivityPrompt.Ref}
		con := &console.Server{Store: st, Token: cfg.Console.Token, Log: log.With("component", "console"), Info: console.Info{
			Name: life.Name, Zone: zone, Started: started, Sets: sets,
			System1: cfg.Decision.Provider.Provider + "/" + orDefault(cfg.Decision.Model, s1openai.DefaultModel),
			System2: fmt.Sprintf("%s/%s (effort %s)", cfg.Reasoning.Provider.Provider, cfg.Reasoning.Model, orDefault(cfg.Reasoning.Effort, "default")),
		}}
		g.Go(func() error { return console.Serve(ctx, cfg.Console.Addr, con.Handler(), con.Log) })
	}
	if profile != nil {
		g.Go(func() error { return profile.Run(ctx) })
		g.Go(func() error {
			return portfolio.Serve(ctx, cfg.Portfolio.Addr, portfolio.Handler(life.Name, st, profile.Log), profile.Log)
		})
	}
	err = g.Wait()
	log.Info("mini-me asleep")
	return err
}

// runClock ticks once a minute, on the minute, so the mind can speak first.
func runClock(ctx context.Context, ticks chan<- time.Time) error {
	for {
		now := time.Now()
		next := now.Truncate(time.Minute).Add(time.Minute)
		select {
		case <-ctx.Done():
			return nil
		case t := <-time.After(next.Sub(now)):
			select {
			case ticks <- t:
			case <-ctx.Done():
				return nil
			}
		}
	}
}

// updateProfile updates the profile page once, now, the way the daily update
// does; with force it rewrites the page even if nothing changed. It works
// whether or not the agent is running: the page is served from the database.
func updateProfile(ctx context.Context, cfg *config.Config, life self.Life, st *store.Store, force bool, log *slog.Logger) error {
	s1, err := newDecisionModel(cfg.Decision.Provider, cfg.Pricing, log)
	if err != nil {
		return err
	}
	s2, err := newReasoner(cfg.Reasoning, cfg.Pricing, log)
	if err != nil {
		return err
	}
	sets, err := questions.LoadDir(cfg.Decision.QuestionSets)
	if err != nil {
		return err
	}
	w, err := newProfileWriter(cfg, life, st, s1, s2, sets, log.With("component", "portfolio"))
	if err != nil {
		return err
	}
	written, err := w.Update(ctx, force)
	if err != nil {
		return err
	}
	if written {
		fmt.Println("Profile page rewritten.")
	} else {
		fmt.Println("Nothing new to share; the profile page is unchanged. Use -force to rewrite it anyway.")
	}
	return nil
}

// newProfileWriter builds what keeps the agent's profile page up to date.
func newProfileWriter(cfg *config.Config, life self.Life, st *store.Store, s1 system1.DecisionModel,
	s2 system2.Reasoner, sets map[string]questions.Set, log *slog.Logger) (*portfolio.Writer, error) {
	public, ok := sets["public_profile"]
	if !ok {
		log.Warn("question set public_profile not found; no fact goes on the profile page", "dir", cfg.Decision.QuestionSets)
	} else if err := checkFactTemplate(public); err != nil {
		return nil, err
	}
	prompt, err := loadPrompt(cfg.Portfolio.Prompt)
	if err != nil {
		return nil, err
	}
	at, err := cfg.Portfolio.UpdateTime()
	if err != nil {
		return nil, err
	}
	return &portfolio.Writer{Life: life, Store: st, System1: s1, System2: s2, Public: public,
		Prompt: prompt, UpdateAt: at, Log: log}, nil
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

// checkFactTemplate makes sure a set (fact_relevance, open_threads,
// public_profile) is a one-question template expanded per fact.
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

// printDay shows what the agent planned on a day, how each went, and what it made.
func printDay(ctx context.Context, st *store.Store, zone *time.Location, date string) error {
	day := time.Now().In(zone)
	if date != "" {
		var err error
		if day, err = time.ParseInLocation("2006-01-02", date, zone); err != nil {
			return fmt.Errorf("-date must be YYYY-MM-DD: %w", err)
		}
	}
	from := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, zone)
	to := from.AddDate(0, 0, 1)
	intentions, err := st.IntentionsDue(ctx, from, to)
	if err != nil {
		return err
	}
	works, err := st.WorksBetween(ctx, from, to)
	if err != nil {
		return err
	}
	fmt.Printf("%s (%s)\n\nPlanned:\n", from.Format("Monday 2 January 2006"), zone)
	if len(intentions) == 0 {
		fmt.Println("  nothing")
	}
	for _, i := range intentions {
		who := "himself"
		if i.Subject != memory.SubjectSelf {
			who = i.Subject
		}
		line := fmt.Sprintf("  %s  %-9s %-8s for %s: %s", i.DueAt.In(zone).Format("15:04"), i.Status, i.Kind, who, i.Text)
		if i.Result != "" {
			line += " → " + i.Result
		}
		fmt.Println(line)
	}
	fmt.Println("\nMade:")
	if len(works) == 0 {
		fmt.Println("  nothing yet")
	}
	for _, w := range works {
		fmt.Printf("\n--- %s · %s · %s\n%s\n", w.CreatedAt.In(zone).Format("15:04"), w.Kind, w.Title, w.Text)
	}
	return nil
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func newLogger(level string) (*slog.Logger, error) {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("config: log.level: %w", err)
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: l})), nil
}

// abilityMakers builds each ability by the name config uses for it. A new
// ability is a package under internal/ability and a line here.
var abilityMakers = map[string]func(st *store.Store, lib func() *library.Client) ability.Ability{
	reading.BookName: func(st *store.Store, lib func() *library.Client) ability.Ability {
		return reading.Book{Library: lib(), Store: st}
	},
	reading.ArticleName: func(st *store.Store, lib func() *library.Client) ability.Ability {
		return reading.Article{Library: lib(), Store: st}
	},
}

// newAbilities builds the journal and the abilities named in config.
func newAbilities(names []string, st *store.Store) (*ability.Set, error) {
	var client *library.Client
	lib := func() *library.Client { // one client, made only if an ability reads
		if client == nil {
			client = library.New("", "")
		}
		return client
	}
	abilities := []ability.Ability{journal.Journal{Store: st}}
	seen := map[string]bool{journal.Name: true}
	for _, name := range names {
		build, ok := abilityMakers[name]
		if !ok {
			known := slices.Sorted(maps.Keys(abilityMakers))
			return nil, fmt.Errorf("config: daily_life.abilities: no ability %q; there are %s (the journal is always on)", name, strings.Join(known, ", "))
		}
		if !seen[name] {
			seen[name] = true
			abilities = append(abilities, build(st, lib))
		}
	}
	return ability.NewSet(abilities...), nil
}
