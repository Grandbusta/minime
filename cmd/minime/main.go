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
	"io"
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

	"github.com/oklog/ulid/v2"
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
	"minime/internal/replay"
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
	limit := fs.Int("n", 20, "decisions: how many to show; replay: how many labelled messages")
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
	case "review":
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
		return runReview(ctx, cfg, life, st, log)
	case "growth":
		zone, err := cfg.Initiative.Zone()
		if err != nil {
			return err
		}
		st, err := store.Open(ctx, cfg.Database.URL)
		if err != nil {
			return err
		}
		defer st.Close()
		return printGrowth(ctx, st, zone, os.Stdout)
	case "learn":
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
		return runLearn(ctx, cfg, life, st, log)
	case "replay":
		st, err := store.Open(ctx, cfg.Database.URL)
		if err != nil {
			return err
		}
		defer st.Close()
		return runReplay(ctx, cfg, st, *limit, log)
	case "work":
		st, err := store.Open(ctx, cfg.Database.URL)
		if err != nil {
			return err
		}
		defer st.Close()
		return works(ctx, st, fs.Args(), *limit, os.Stdout)
	default:
		return fmt.Errorf("unknown command %q (want run, decisions, profile, day, work, growth, review, learn or replay)", cmd)
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
	replayHint(ctx, st, incoming, log)
	skillLevel, ok := sets["skill_level"]
	if !ok {
		log.Warn("question set skill_level not found; works aren't scored, and skills only move with the owner's feedback")
	} else if len(skillLevel.Questions) != 1 || skillLevel.Questions[0].Type != decision.QuestionScore {
		return fmt.Errorf("question set %s must be exactly one score question", skillLevel.Ref())
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
	policy, err := cfg.Policy()
	if err != nil {
		return err
	}
	abilitySet, err := newAbilities(names, st)
	if err != nil {
		return err
	}
	day, err := newDay(cfg)
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
		Ticks:   ticks, Zone: zone, CheckIns: mind.CheckIns{Times: checkIns, Quiet: cfg.Initiative.Quiet}, Day: day,
		Abilities:  abilitySet,
		SkillLevel: skillLevel,
		Autonomy:   policy,
		Asker:      tg,
		Log:        log.With("component", "mind"),
	})
	sensor := telegram.NewSensor(tg, cfg.Telegram.OwnerIDs, cfg.Telegram.PollTimeoutSeconds, percepts,
		log.With("component", "telegram"))

	log.Info("mini-me awake", "name", life.Name, "life_story_lines", len(life.Story), "owners", len(cfg.Telegram.OwnerIDs),
		"system1", cfg.Decision.Provider.Provider, "question_set", incoming.Ref(), "follow_up", followUp.Ref(),
		"life_area", lifeArea.Ref(), "person_circle", personCircle.Ref(), "fact_relevance", factRelevance.Ref(),
		"open_threads", openThreads.Ref(), "timezone", zone.String(), "check_ins", cfg.Initiative.CheckIns,
		"daily_life", cfg.DailyLife.Enabled, "abilities", abilitySet.Names(), "routine_lines", len(life.Routine), "makes", len(life.Makes),
		"autonomy_owner", policy.Owner,
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
// Each tick is the minute it was due, not when the timer fired: a timer may
// fire a hair before the wall clock reaches the minute, and something due at
// 17:50 must count as due at the 17:50 tick.
func runClock(ctx context.Context, ticks chan<- time.Time) error {
	for {
		now := time.Now()
		next := now.Truncate(time.Minute).Add(time.Minute)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(next.Sub(now)):
			select {
			case ticks <- next:
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

// newDay is its own day, from config: when it plans, journals and reviews,
// and how. The zero Day (daily life off) has no day of its own.
func newDay(cfg *config.Config) (mind.Day, error) {
	var day mind.Day
	dl := cfg.DailyLife
	if !dl.Enabled {
		return day, nil
	}
	var err error
	if day.PlanAt, day.JournalAt, err = dl.Times(); err != nil {
		return day, err
	}
	if day.ReviewDay, day.ReviewAt, err = dl.ReviewTime(); err != nil {
		return day, err
	}
	for _, p := range []struct {
		to   *mind.Prompt
		path string
	}{{&day.PlanPrompt, dl.PlanPrompt}, {&day.ActivityPrompt, dl.ActivityPrompt}, {&day.ReviewPrompt, dl.ReviewPrompt},
		{&day.LearnPrompt, dl.LearnPrompt}} {
		if *p.to, err = loadPrompt(p.path); err != nil {
			return day, err
		}
	}
	day.MaxActivities, day.BudgetUSD = dl.Activities, dl.BudgetUSD
	return day, nil
}

// growthMind is the mind the review and learn commands run: enough to
// review its week and plan its learning, without talking to anyone. What it
// wants to change about its dreams is saved for the owner's approval; the
// running agent asks them.
func growthMind(cfg *config.Config, life self.Life, st *store.Store, log *slog.Logger) (*mind.Mind, error) {
	s2, err := newReasoner(cfg.Reasoning, cfg.Pricing, log)
	if err != nil {
		return nil, err
	}
	zone, err := cfg.Initiative.Zone()
	if err != nil {
		return nil, err
	}
	day, err := newDay(cfg)
	if err != nil {
		return nil, err
	}
	if day.ReviewPrompt.Text == "" {
		return nil, errors.New("daily_life is off: no growth")
	}
	policy, err := cfg.Policy()
	if err != nil {
		return nil, err
	}
	abilities, err := newAbilities(cfg.DailyLife.AbilityNames(), st) // so it knows what it can study with
	if err != nil {
		return nil, err
	}
	return mind.New(mind.Config{Life: life, Store: st, System2: s2, Zone: zone, Day: day, Autonomy: policy, Abilities: abilities,
		Log: log.With("component", "mind")}), nil
}

// runReview runs the weekly review now.
func runReview(ctx context.Context, cfg *config.Config, life self.Life, st *store.Store, log *slog.Logger) error {
	m, err := growthMind(cfg, life, st, log)
	if err != nil {
		return err
	}
	if err := m.Review(ctx, time.Now()); err != nil {
		return err
	}
	fmt.Println("Reviewed the week. Run `minime growth`, or open the console's Growth view, to see where it stands.")
	return nil
}

// runLearn drafts a learning plan now for each skill goal that has none.
func runLearn(ctx context.Context, cfg *config.Config, life self.Life, st *store.Store, log *slog.Logger) error {
	m, err := growthMind(cfg, life, st, log)
	if err != nil {
		return err
	}
	m.EnsurePlans(ctx, time.Now())
	fmt.Println("Every skill goal has a learning plan. See them in the console's Growth view.")
	return nil
}

// growthStore is what printGrowth reads.
type growthStore interface {
	Dreams(ctx context.Context, activeOnly bool) ([]memory.Dream, error)
	Goals(ctx context.Context, activeOnly bool) ([]memory.Goal, error)
	Skills(ctx context.Context) ([]memory.Skill, error)
}

// printGrowth shows who it wants to become: its dreams, its goals and its skills.
func printGrowth(ctx context.Context, st growthStore, zone *time.Location, out io.Writer) error {
	dreams, err := st.Dreams(ctx, true)
	if err != nil {
		return err
	}
	goals, err := st.Goals(ctx, true)
	if err != nil {
		return err
	}
	skills, err := st.Skills(ctx)
	if err != nil {
		return err
	}
	if len(dreams) == 0 {
		fmt.Fprintln(out, "No dreams yet: they're seeded from self/life.yaml when the agent starts.")
	}
	for _, d := range dreams {
		fmt.Fprintf(out, "Dream: %s\n", d.Text)
		for _, g := range goals {
			if g.DreamID == d.ID {
				fmt.Fprintf(out, "  Goal, due %s: %s\n    done when: %s\n    now: %s\n", g.Due.In(zone).Format("Mon 2 Jan"), g.Text, g.DoneWhen,
					map[bool]string{true: g.Progress, false: "just started"}[g.Progress != ""])
			}
		}
	}
	if len(skills) > 0 {
		fmt.Fprintln(out, "\nSkills (1-10):")
		w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		for _, sk := range skills {
			fmt.Fprintf(w, "  %s\t%.1f\t%s\n", sk.Name, sk.Level, strings.Repeat("▮", int(sk.Level+0.5)))
		}
		w.Flush()
	}
	return nil
}

// runReplay re-asks System 1 about the messages the owner labelled, with the
// incoming_message question set as it is now, and prints how it compares.
func runReplay(ctx context.Context, cfg *config.Config, st *store.Store, n int, log *slog.Logger) error {
	s1, err := newDecisionModel(cfg.Decision.Provider, cfg.Pricing, log)
	if err != nil {
		return err
	}
	sets, err := questions.LoadDir(cfg.Decision.QuestionSets)
	if err != nil {
		return err
	}
	set, ok := sets["incoming_message"]
	if !ok {
		return errors.New("question set incoming_message not found")
	}
	if n == 20 {
		n = 500 // the flag's default is for decisions; replay all there is, within reason
	}
	r, err := replay.Run(ctx, st, s1, set, n)
	if err != nil {
		return err
	}
	r.Write(os.Stdout)
	return nil
}

// replayHint logs when labelled messages were read by an older version of
// the incoming_message set: the new one should be replayed before it's trusted.
func replayHint(ctx context.Context, st *store.Store, set questions.Set, log *slog.Logger) {
	examples, err := st.LabelledExamples(ctx, set.ID, set.Primary, 500)
	if err != nil {
		return
	}
	older := 0
	for _, e := range examples {
		if e.SetID != set.Ref() {
			older++
		}
	}
	if older > 0 {
		log.Info("labelled messages were read by an older question set; run `minime replay` to check this one",
			"set", set.Ref(), "older", older, "labelled", len(examples))
	}
}

// worksStore is what the work command needs.
type worksStore interface {
	RecentWorks(ctx context.Context, limit int) ([]memory.Work, error)
	SetWorkPublic(ctx context.Context, id ulid.ULID, public bool, by string) (bool, error)
}

// works lists the newest works and whether each is on the profile page
// (no args), or puts one on it or takes it off ("show <id>", "hide <id>"),
// as the owner: the daily check never overrides that.
func works(ctx context.Context, st worksStore, args []string, n int, out io.Writer) error {
	if len(args) == 0 {
		ws, err := st.RecentWorks(ctx, n)
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tMADE\tKIND\tPUBLIC\tTITLE")
		for _, wk := range ws {
			public := "undecided"
			if wk.Public != nil {
				public = map[bool]string{true: "yes", false: "no"}[*wk.Public] + " (" + wk.PublicBy + ")"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", wk.ID, wk.CreatedAt.Local().Format("2 Jan 15:04"), wk.Kind, public, wk.Title)
		}
		return w.Flush()
	}
	if len(args) != 2 || (args[0] != "show" && args[0] != "hide") {
		return errors.New("usage: minime work [show|hide <id>]")
	}
	id, err := ulid.Parse(args[1])
	if err != nil {
		return fmt.Errorf("%q is not a work ID", args[1])
	}
	show := args[0] == "show"
	changed, err := st.SetWorkPublic(ctx, id, show, memory.PublicByOwner)
	if err != nil {
		return err
	}
	switch {
	case !changed && show:
		return fmt.Errorf("work %s can't be shown: there's no such work, or it's the journal or something made for someone", id)
	case !changed:
		return fmt.Errorf("no work %s", id)
	case show:
		fmt.Fprintln(out, "It's on the profile page now.")
	default:
		fmt.Fprintln(out, "It's off the profile page, and stays off.")
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
	policy, err := cfg.Policy()
	if err != nil {
		return nil, err
	}
	publicWork, ok := sets["public_work"]
	if !ok {
		log.Warn("question set public_work not found; no work goes on the profile page", "dir", cfg.Decision.QuestionSets)
	} else if len(publicWork.Questions) != 1 || publicWork.Questions[0].Type != decision.QuestionPredicate ||
		!strings.Contains(publicWork.Questions[0].Instructions, "{work}") {
		return nil, fmt.Errorf("question set %s must be exactly one predicate whose instructions contain {work}", publicWork.Ref())
	}
	prompt, err := loadPrompt(cfg.Portfolio.Prompt)
	if err != nil {
		return nil, err
	}
	at, err := cfg.Portfolio.UpdateTime()
	if err != nil {
		return nil, err
	}
	return &portfolio.Writer{Life: life, Store: st, System1: s1, System2: s2, Public: public, PublicWork: publicWork, Autonomy: policy,
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
		return system2.WithoutEmDashes(s2openai.New(c.APIKey, c.Model, c.BaseURL, priceFor(c.Model, prices, log), c.Effort)), nil
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
