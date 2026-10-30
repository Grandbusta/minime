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

	"github.com/Grandbusta/minime/internal/action"
	"github.com/Grandbusta/minime/internal/pricing"
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
	// Embedding finds the past decisions most like the one at hand; no
	// provider: the newest are used instead.
	Embedding Provider                 `yaml:"embedding"`
	Pricing   map[string]pricing.Price `yaml:"pricing"`
	Log       Log                      `yaml:"log"`
}

type Memory struct {
	RecallCandidates int `yaml:"recall_candidates"`
}

type Portfolio struct {
	Addr     string `yaml:"addr"` // empty: no page
	Prompt   string `yaml:"prompt"`
	UpdateAt string `yaml:"update_at"` // "HH:MM" in UTC, not its own time zone
}

func (p Portfolio) UpdateTime() (time.Duration, error) {
	at, err := timeOfDay(p.UpdateAt)
	if err != nil {
		return 0, fmt.Errorf("config: portfolio.update_at must be HH:MM in UTC, e.g. \"00:00\", have %q", p.UpdateAt)
	}
	return at, nil
}

type Initiative struct {
	Timezone string        `yaml:"timezone"` // empty: the one in its life story
	CheckIns []string      `yaml:"check_ins"`
	Quiet    time.Duration `yaml:"quiet"`
}

func (i Initiative) Zone() (*time.Location, error) {
	loc, err := time.LoadLocation(i.Timezone)
	if err != nil || i.Timezone == "" {
		return nil, fmt.Errorf("a time zone like Africa/Lagos is needed: timezone in the life story, or initiative.timezone in the config; have %q",
			i.Timezone)
	}
	return loc, nil
}

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

type Console struct {
	Addr  string `yaml:"addr"`  // empty: no console
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

type DailyLife struct {
	Enabled bool `yaml:"enabled"`
	// Abilities nil: DefaultAbilities. Empty: none besides the journal.
	Abilities []string `yaml:"abilities"`
	// Deprecated: list Abilities instead. Used only when Abilities is nil.
	Reading    *bool  `yaml:"reading"`
	PlanAt     string `yaml:"plan_at"`
	JournalAt  string `yaml:"journal_at"`
	ReviewAt   string `yaml:"review_at"`
	Activities int    `yaml:"activities"`
	// Pace is the rounds in its waking day; BudgetUSD and Web are per pace-1
	// day and are multiplied by it.
	Pace           int     `yaml:"pace"`
	BudgetUSD      float64 `yaml:"budget_usd"`
	PlanPrompt     string  `yaml:"plan_prompt"`
	ActivityPrompt string  `yaml:"activity_prompt"`
	ReviewPrompt   string  `yaml:"review_prompt"`
	LearnPrompt    string  `yaml:"learn_prompt"`
	Web            Web     `yaml:"web"`
}

type Web struct {
	Searches int `yaml:"searches_per_day"`
	Pages    int `yaml:"pages_per_day"`
}

// Search with the openai provider uses the System 2 key (reasoning.api_key).
type Search struct {
	Provider string `yaml:"provider"`
	Model    string `yaml:"model"` // empty: the System 2 model
}

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

var DefaultAbilities = []string{"read_book", "read_article"}

func (d DailyLife) AbilityNames() []string {
	switch {
	case d.Abilities != nil:
		return d.Abilities
	case d.Reading != nil && !*d.Reading:
		return nil
	}
	return DefaultAbilities
}

type Judgment struct {
	Calls       bool          `yaml:"calls"`
	AsksPerDay  int           `yaml:"asks_per_day"` // day-plan calls only; goal calls are always asked
	ReplyWindow time.Duration `yaml:"reply_window"`
}

type Autonomy struct {
	Owner      string            `yaml:"owner"`
	Classes    map[string]string `yaml:"classes"`
	AskExpires time.Duration     `yaml:"ask_expires"`
}

// Policy is the defaults with the config's overrides. The owner defaults to
// the first Telegram owner.
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

func (d DailyLife) Times() (plan, journal time.Duration, err error) {
	if plan, err = timeOfDay(d.PlanAt); err != nil {
		return 0, 0, fmt.Errorf("config: daily_life.plan_at must be HH:MM, have %q", d.PlanAt)
	}
	if journal, err = timeOfDay(d.JournalAt); err != nil || journal <= plan {
		return 0, 0, fmt.Errorf("config: daily_life.journal_at must be HH:MM after plan_at, have %q", d.JournalAt)
	}
	return plan, journal, nil
}

func timeOfDay(s string) (time.Duration, error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, err
	}
	return time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute, nil
}

type WorkingMemory struct {
	Turns  int           `yaml:"turns"` // 0 turns recall off
	Window time.Duration `yaml:"window"`
}

// Self is the person's folder, laid out like each life in lives/.
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
	Model    string `yaml:"model"`
	APIKey   string `yaml:"api_key"`
	BaseURL  string `yaml:"base_url"`
}

type Decision struct {
	Provider     `yaml:",inline"`
	QuestionSets string `yaml:"question_sets"`
}

type Reasoning struct {
	Provider `yaml:",inline"`
	Prompt   string `yaml:"prompt"`
	Effort   string `yaml:"effort"` // empty: the model's default
}

type Log struct {
	Level string `yaml:"level"`
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
		DailyLife: DailyLife{Enabled: true, Pace: 1, PlanAt: "08:00", JournalAt: "22:00", Activities: 4, BudgetUSD: 0.05,
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
	// The environment wins: in a container these must be all addresses, not localhost.
	if addr, ok := os.LookupEnv("MINIME_CONSOLE_ADDR"); ok {
		cfg.Console.Addr = addr
	}
	if addr, ok := os.LookupEnv("MINIME_PROFILE_ADDR"); ok {
		cfg.Portfolio.Addr = addr
	}
	if cfg.Database.URL == "" {
		return nil, errors.New("config: database.url is required")
	}
	return cfg, nil
}

// loadLiving reads living.yaml over the defaults and the config file. A
// missing file leaves them as they are.
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
	if c.DailyLife.Pace < 1 || c.DailyLife.Pace > 100 {
		return errors.New("config: daily_life.pace must be from 1 (a person's pace) to 100")
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
