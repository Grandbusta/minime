// Package config loads the agent's YAML configuration.
package config

import (
	"errors"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"

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

// WorkingMemory is how much of the current conversation System 2 sees.
type WorkingMemory struct {
	Turns  int           `yaml:"turns"`  // earlier exchanges; 0 turns recall off
	Window time.Duration `yaml:"window"` // e.g. 2h: older exchanges are left out
}

// Self is who the agent is.
type Self struct {
	Life string `yaml:"life"` // file with the agent's name and life story
}

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
		Self:          Self{Life: "self/life.yaml"},
		Telegram:      Telegram{PollTimeoutSeconds: 30},
		Decision:      Decision{QuestionSets: "decisions"},
		Reasoning:     Reasoning{Prompt: "prompts/reply.md", Effort: "low"},
		WorkingMemory: WorkingMemory{Turns: 8, Window: 30 * time.Minute},
		Memory:        Memory{RecallCandidates: 30},
		Log:           Log{Level: "info"},
	}
	if err := yaml.Unmarshal([]byte(os.ExpandEnv(string(raw))), cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	if cfg.Database.URL == "" {
		return nil, errors.New("config: database.url is required")
	}
	return cfg, nil
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
	return nil
}
