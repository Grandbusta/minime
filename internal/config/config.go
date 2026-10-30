// Package config loads the agent's YAML configuration.
package config

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"minime/internal/action"
	"minime/internal/pricing"
)

type Config struct {
	Self          Self          `yaml:"self"`
	Database      Database      `yaml:"database"`
	Telegram      Telegram      `yaml:"telegram"`
	Decision      Decision      `yaml:"decision"`  // System 1
	Reasoning     Reasoning     `yaml:"reasoning"` // System 2
	WorkingMemory WorkingMemory `yaml:"working_memory"`
	Memory        Memory        `yaml:"memory"`
	Portfolio     Portfolio     `yaml:"portfolio"`
	Initiative    Initiative    `yaml:"initiative"`
	DailyLife     DailyLife     `yaml:"daily_life"`
	Console       Console       `yaml:"console"`
	Autonomy      Autonomy      `yaml:"autonomy"`
	Judgment      Judgment      `yaml:"judgment"`
	Search        Search        `yaml:"search"`
	// Pricing per model, used to record what each decision cost.
	Pricing map[string]pricing.Price `yaml:"pricing"`
	Log     Log                      `yaml:"log"`
}

// Memory is long-term memory: facts about the agent itself and the people it talks to.
type Memory struct {
	// RecallCandidates is how many of the newest facts System 1 checks for
	// relevance before each System 2 reply; about 170 input tokens each.
	RecallCandidates int `yaml:"recall_candidates"`
}

// Portfolio is the agent's public profile page, served by the agent itself.
type Portfolio struct {
	Addr   string `yaml:"addr"`   // where to serve it, e.g. 127.0.0.1:8080; empty: no page
	Prompt string `yaml:"prompt"` // file with how System 2 writes the page
	// UpdateAt is when it updates the page each day, "HH:MM" in UTC.
	UpdateAt string `yaml:"update_at"`
}

// UpdateTime is UpdateAt as a time of day: the time since midnight UTC.
func (p Portfolio) UpdateTime() (time.Duration, error) {
	at, err := timeOfDay(p.UpdateAt)
	if err != nil {
		return 0, fmt.Errorf("config: portfolio.update_at must be HH:MM in UTC, e.g. \"00:00\", have %q", p.UpdateAt)
	}
	return at, nil
}

// Initiative is the agent speaking first: reminders, and checking in on people.
type Initiative struct {
	// Timezone is the agent's own, an IANA name like Africa/Lagos: reminders
	// and check-in times are in it. Empty: the one in its life story.
	Timezone string `yaml:"timezone"`
	// CheckIns are the times of day ("HH:MM") it considers checking in on
	// people, at most once a day each. Empty: never unprompted.
	CheckIns []string `yaml:"check_ins"`
	// Quiet is how long a conversation must have been quiet before a check-in.
	Quiet time.Duration `yaml:"quiet"`
}

func (i Initiative) Zone() (*time.Location, error) {
	loc, err := time.LoadLocation(i.Timezone)
	if err != nil || i.Timezone == "" {
		return nil, fmt.Errorf("a time zone like Africa/Lagos is needed: timezone in the life story, or initiative.timezone in the config; have %q",
			i.Timezone)
	}
	return loc, nil
}

// CheckInTimes are CheckIns as times of day: the time since midnight.
func (i Initiative) CheckInTimes() ([]time.Duration, error) {
	out := make([]time.Duration, len(i.CheckIns))
	for n, s := range i.CheckIns {
		at, err := timeOfDay(s)
		if err != nil {
			return nil, fmt.Errorf("config: initiative.check_ins must be HH:MM times, have %q", s)
		}
		out[n] = at
	}
	return out, nil
}

// Console is the owner's private mind console.
type Console struct {
	Addr  string `yaml:"addr"`  // e.g. 127.0.0.1:8090; empty: no console
	Token string `yaml:"token"` // required unless addr is localhost
}

// Local reports whether the console only listens on this machine.
func (c Console) Local() bool {
	host, _, err := net.SplitHostPort(c.Addr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return host == "localhost" || (ip != nil && ip.IsLoopback())
}

// DailyLife is the agent's own day: it plans it, does things, and journals.
type DailyLife struct {
	Enabled bool `yaml:"enabled"`
	// Abilities are what it can do beyond writing, by name, e.g. read_book;
	// the journal is always on. Unset: the default abilities.
	Abilities []string `yaml:"abilities"`
	// Reading is the old switch for read_book and read_article, used only
	// when Abilities is unset. Deprecated: list abilities instead.
	Reading        *bool   `yaml:"reading"`
	PlanAt         string  `yaml:"plan_at"`    // "HH:MM" in its time zone
	JournalAt      string  `yaml:"journal_at"` // "HH:MM" in its time zone
	ReviewAt       string  `yaml:"review_at"`  // its weekly review, "Sun 20:00" in its time zone
	Activities     int     `yaml:"activities"` // things it plans a day, besides the journal
	BudgetUSD      float64 `yaml:"budget_usd"` // most its own day may cost
	PlanPrompt     string  `yaml:"plan_prompt"`
	ActivityPrompt string  `yaml:"activity_prompt"`
	ReviewPrompt   string  `yaml:"review_prompt"`
	LearnPrompt    string  `yaml:"learn_prompt"` // how it plans to learn a skill for a goal
	Web            Web     `yaml:"web"`          // how much of the web it may use a day
}

// Web is how much of the open web it may use in a day.
type Web struct {
	Searches int `yaml:"searches_per_day"`
	Pages    int `yaml:"pages_per_day"`
}

// Search is the provider its web abilities search with. The OpenAI provider
// uses the System 2 key (reasoning.api_key).
type Search struct {
	Provider string `yaml:"provider"` // openai; later tinyfish
	Model    string `yaml:"model"`    // empty: the System 2 model
}

// ReviewTime is ReviewAt as a weekday and a time of day.
func (d DailyLife) ReviewTime() (time.Weekday, time.Duration, error) {
	day, at, ok := strings.Cut(strings.TrimSpace(d.ReviewAt), " ")
	days := map[string]time.Weekday{"sun": time.Sunday, "mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
		"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday}
	wd, known := days[strings.ToLower(day)]
	t, err := timeOfDay(strings.TrimSpace(at))
	if !ok || !known || err != nil {
		return 0, 0, fmt.Errorf("config: daily_life.review_at must be a day and HH:MM, like \"Sun 20:00\", have %q", d.ReviewAt)
	}
	return wd, t, nil
}

// DefaultAbilities are on unless the config lists its own.
var DefaultAbilities = []string{"read_book", "read_article"}

// AbilityNames are the abilities switched on, besides the journal.
func (d DailyLife) AbilityNames() []string {
	switch {
	case d.Abilities != nil:
		return d.Abilities
	case d.Reading != nil && !*d.Reading:
		return nil
	}
	return DefaultAbilities
}

// Judgment is how it learns its owner's judgment from its own decisions:
// it predicts what they'd choose, asks some on Telegram, and the rest wait
// to be graded in the console.
type Judgment struct {
	Calls      bool `yaml:"calls"`        // off: it makes no calls, and sets and drops goals on its own
	AsksPerDay int  `yaml:"asks_per_day"` // day-plan calls asked a day; goal calls are always asked
	// ReplyWindow is how long after the owner picks differently their next
	// message is taken as why.
	ReplyWindow time.Duration `yaml:"reply_window"`
}

// Autonomy is what the agent may do on its own (see package action).
type Autonomy struct {
	// Owner approves what it asks to do, e.g. "telegram:123". Empty: the
	// first of telegram.owner_ids.
	Owner string `yaml:"owner"`
	// Classes overrides the default verdict (auto, ask or forbid) per action class.
	Classes map[string]string `yaml:"classes"`
	// AskExpires is how long a request waits for an answer; then it is denied.
	AskExpires time.Duration `yaml:"ask_expires"`
}

// Policy is the autonomy policy: the defaults with the config's overrides,
// and the owner (the first Telegram owner if none is set).
func (c *Config) Policy() (action.Policy, error) {
	classes := action.Defaults()
	overrides := map[action.Class]action.Verdict{}
	for k, v := range c.Autonomy.Classes {
		overrides[action.Class(k)] = action.Verdict(strings.TrimSpace(v))
	}
	if err := action.Validate(overrides); err != nil {
		return action.Policy{}, fmt.Errorf("config: %w", err)
	}
	for k, v := range overrides {
		classes[k] = v
	}
	owner := strings.TrimSpace(c.Autonomy.Owner)
	if owner == "" && len(c.Telegram.OwnerIDs) > 0 {
		owner = "telegram:" + strconv.FormatInt(c.Telegram.OwnerIDs[0], 10)
	}
	if c.Autonomy.AskExpires <= 0 {
		return action.Policy{}, errors.New("config: autonomy.ask_expires must be positive, e.g. 24h")
	}
	return action.Policy{Owner: owner, Classes: classes, AskExpires: c.Autonomy.AskExpires}, nil
}

// Times are PlanAt and JournalAt as times of day: the time since midnight.
func (d DailyLife) Times() (plan, journal time.Duration, err error) {
	if plan, err = timeOfDay(d.PlanAt); err != nil {
		return 0, 0, fmt.Errorf("config: daily_life.plan_at must be HH:MM, have %q", d.PlanAt)
	}
	if journal, err = timeOfDay(d.JournalAt); err != nil || journal <= plan {
		return 0, 0, fmt.Errorf("config: daily_life.journal_at must be HH:MM after plan_at, have %q", d.JournalAt)
	}
	return plan, journal, nil
}

// timeOfDay parses "HH:MM" into the time since midnight.
func timeOfDay(s string) (time.Duration, error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, err
	}
	return time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute, nil
}

// WorkingMemory is how much of the current conversation System 2 sees.
type WorkingMemory struct {
	Turns  int           `yaml:"turns"`  // earlier exchanges; 0 turns recall off
	Window time.Duration `yaml:"window"` // e.g. 2h: older exchanges are left out
}

// Self is where the person is: a folder with their life (life.yaml), how
// they live (living.yaml) and their owner's judgment (judgment.yaml), laid
// out like each life in lives/.
type Self struct {
	Dir string `yaml:"dir"`
}

func (s Self) LifeFile() string     { return filepath.Join(s.Dir, "life.yaml") }
func (s Self) LivingFile() string   { return filepath.Join(s.Dir, "living.yaml") }
func (s Self) JudgmentFile() string { return filepath.Join(s.Dir, "judgment.yaml") }

type Database struct {
	URL string `yaml:"url"`
}

type Telegram struct {
	Token              string  `yaml:"token"`
	OwnerIDs           []int64 `yaml:"owner_ids"`
	PollTimeoutSeconds int     `yaml:"poll_timeout_seconds"`
}

type Provider struct {
	Provider string `yaml:"provider"`
	Model    string `yaml:"model"`    // empty: the provider's default
	APIKey   string `yaml:"api_key"`  // usually ${SOME_API_KEY}
	BaseURL  string `yaml:"base_url"` // empty: the provider's default
}

type Decision struct {
	Provider     `yaml:",inline"`
	QuestionSets string `yaml:"question_sets"` // directory of question set files
}

type Reasoning struct {
	Provider `yaml:",inline"`
	Prompt   string `yaml:"prompt"` // file with the agent's reply instructions
	// Effort is how hard System 2 thinks before replying: none, low, medium,
	// high, ... Empty leaves the model's default (medium for gpt-6-luna).
	Effort string `yaml:"effort"`
}

type Log struct {
	Level string `yaml:"level"` // debug | info | warn | error
}

// Load reads the file at path, expanding ${VAR} references from the environment.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	cfg := &Config{
		Self:          Self{Dir: "self"},
		Telegram:      Telegram{PollTimeoutSeconds: 30},
		Decision:      Decision{QuestionSets: "decisions"},
		Reasoning:     Reasoning{Prompt: "prompts/reply.md", Effort: "low"},
		WorkingMemory: WorkingMemory{Turns: 8, Window: 30 * time.Minute},
		Memory:        Memory{RecallCandidates: 30},
		Portfolio:     Portfolio{Prompt: "prompts/profile.md", UpdateAt: "00:00"},
		Initiative:    Initiative{Quiet: 4 * time.Hour},
		DailyLife: DailyLife{Enabled: true, PlanAt: "08:00", JournalAt: "22:00", Activities: 4, BudgetUSD: 0.05,
			PlanPrompt: "prompts/plan.md", ActivityPrompt: "prompts/activity.md",
			ReviewAt: "Sun 20:00", ReviewPrompt: "prompts/review.md", LearnPrompt: "prompts/learn.md",
			Web: Web{Searches: 10, Pages: 15}},
		Search:   Search{Provider: "openai"},
		Autonomy: Autonomy{AskExpires: 24 * time.Hour},
		Judgment: Judgment{Calls: true, AsksPerDay: 3, ReplyWindow: 30 * time.Minute},
		Log:      Log{Level: "info"},
	}
	if err := yaml.Unmarshal([]byte(os.ExpandEnv(string(raw))), cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	if err := cfg.loadLiving(); err != nil {
		return nil, err
	}
	if cfg.Database.URL == "" {
		return nil, errors.New("config: database.url is required")
	}
	return cfg, nil
}

// loadLiving reads how the person lives (living.yaml in their folder) over
// the defaults and the config file: their days, check-ins, abilities, what
// they may do on their own, and how they learn their owner's judgment. A
// missing file leaves those as they are.
func (c *Config) loadLiving() error {
	raw, err := os.ReadFile(c.Self.LivingFile())
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read living: %w", err)
	}
	living := struct {
		Initiative *Initiative `yaml:"initiative"`
		DailyLife  *DailyLife  `yaml:"daily_life"`
		Autonomy   *Autonomy   `yaml:"autonomy"`
		Judgment   *Judgment   `yaml:"judgment"`
	}{&c.Initiative, &c.DailyLife, &c.Autonomy, &c.Judgment}
	dec := yaml.NewDecoder(strings.NewReader(os.ExpandEnv(string(raw))))
	dec.KnownFields(true)
	if err := dec.Decode(&living); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("%s: %w", c.Self.LivingFile(), err)
	}
	return nil
}

// ValidateAgent checks the settings needed to run the agent, beyond what Load requires.
func (c *Config) ValidateAgent() error {
	if c.Telegram.Token == "" {
		return errors.New("config: telegram.token is required")
	}
	if c.Telegram.PollTimeoutSeconds < 1 || c.Telegram.PollTimeoutSeconds > 50 {
		return errors.New("config: telegram.poll_timeout_seconds must be between 1 and 50")
	}
	if c.Decision.Provider.Provider == "" {
		return errors.New("config: decision.provider is required")
	}
	if c.Decision.APIKey == "" {
		return errors.New("config: decision.api_key is required")
	}
	r := c.Reasoning
	if r.Provider.Provider == "" || r.APIKey == "" || r.Model == "" {
		return errors.New("config: reasoning.provider, reasoning.api_key and reasoning.model are required")
	}
	if c.Portfolio.Addr != "" {
		if _, err := c.Portfolio.UpdateTime(); err != nil {
			return err
		}
	}
	if _, err := c.Initiative.Zone(); err != nil {
		return err
	}
	if _, err := c.Initiative.CheckInTimes(); err != nil {
		return err
	}
	if c.Console.Addr != "" && !c.Console.Local() && len(c.Console.Token) < 16 {
		return errors.New("config: console.token (16+ characters) is required when console.addr is not localhost")
	}
	if c.DailyLife.Enabled {
		if _, _, err := c.DailyLife.Times(); err != nil {
			return err
		}
		if c.DailyLife.Activities < 1 || c.DailyLife.BudgetUSD < 0 {
			return errors.New("config: daily_life.activities must be at least 1 and budget_usd not negative")
		}
	}
	return nil
}
