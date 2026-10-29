package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "minime.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadExpandsEnvAndAppliesDefaults(t *testing.T) {
	t.Setenv("TEST_DB_URL", "postgres://x")
	t.Setenv("TEST_OWNER", "42")
	path := writeConfig(t, `
database:
  url: ${TEST_DB_URL}
telegram:
  token: abc
  owner_ids: [${TEST_OWNER}]
decision:
  provider: openai
  api_key: sk-x
reasoning:
  provider: openai
  model: gpt-x
  api_key: sk-x
pricing:
  gpt-x: {input: 0.10, output: 0.50, cached_input: 0.01, cache_write: 0.125}
working_memory:
  window: 90m
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database.URL != "postgres://x" {
		t.Errorf("database.url = %q", cfg.Database.URL)
	}
	if len(cfg.Telegram.OwnerIDs) != 1 || cfg.Telegram.OwnerIDs[0] != 42 {
		t.Errorf("owner_ids = %v", cfg.Telegram.OwnerIDs)
	}
	if w := cfg.WorkingMemory; w.Window != 90*time.Minute || w.Turns != 8 {
		t.Errorf("working memory = %+v (window from file, turns default)", w)
	}
	if p := cfg.Pricing["gpt-x"]; p.Input != 0.10 || p.Output != 0.50 || p.CachedInput != 0.01 || p.CacheWrite != 0.125 {
		t.Errorf("pricing = %+v", cfg.Pricing)
	}
	if cfg.Decision.Provider.Provider != "openai" || cfg.Decision.APIKey != "sk-x" {
		t.Errorf("decision = %+v", cfg.Decision)
	}
	if cfg.Telegram.PollTimeoutSeconds != 30 || cfg.Log.Level != "info" || cfg.Decision.QuestionSets != "decisions" ||
		cfg.Reasoning.Prompt != "prompts/reply.md" || cfg.Reasoning.Effort != "low" {
		t.Errorf("defaults not applied: %+v", cfg)
	}
	if err := cfg.ValidateAgent(); err != nil {
		t.Errorf("ValidateAgent: %v", err)
	}
}

func TestLoadRequiresDatabaseURL(t *testing.T) {
	if _, err := Load(writeConfig(t, "telegram:\n  token: abc\n")); err == nil {
		t.Fatal("expected error for missing database.url")
	}
}

func TestValidateAgentRequiresToken(t *testing.T) {
	cfg, err := Load(writeConfig(t, "database:\n  url: postgres://x\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.ValidateAgent(); err == nil {
		t.Fatal("expected error for missing telegram.token")
	}
}

func TestAbilityNames(t *testing.T) {
	for body, want := range map[string]string{
		"":                           "read_book,read_article",
		"\n  abilities: [read_book]": "read_book",
		"\n  abilities: []":          "",
		"\n  reading: false":         "",
		"\n  reading: true":          "read_book,read_article",
		"\n  reading: false\n  abilities: [read_article]": "read_article",
	} {
		cfg, err := Load(writeConfig(t, "database: {url: x}\ndaily_life:\n  enabled: true"+body+"\n"))
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(cfg.DailyLife.AbilityNames(), ","); got != want {
			t.Errorf("%q: abilities = %q, want %q", body, got, want)
		}
	}
}

func TestPolicy(t *testing.T) {
	cfg, err := Load(writeConfig(t, "database: {url: x}\ntelegram:\n  owner_ids: [42, 7]\nautonomy:\n  classes: {publish: auto}\n"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := cfg.Policy()
	if err != nil {
		t.Fatal(err)
	}
	if p.Owner != "telegram:42" || p.AskExpires != 24*time.Hour || p.Classes["publish"] != "auto" || p.Classes["commit"] != "ask" {
		t.Errorf("policy = %+v", p)
	}
	for _, body := range []string{"autonomy:\n  classes: {reply: forbid}\n", "autonomy:\n  classes: {fly: auto}\n",
		"autonomy:\n  ask_expires: -1h\n"} {
		cfg, err := Load(writeConfig(t, "database: {url: x}\n"+body))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := cfg.Policy(); err == nil {
			t.Errorf("%q: no error", body)
		}
	}
}

func TestShippedConfigPolicy(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("TELEGRAM_OWNER_ID", "42")
	cfg, err := Load("../../configs/minime.yaml")
	if err != nil {
		t.Fatal(err)
	}
	p, err := cfg.Policy()
	if err != nil {
		t.Fatal(err)
	}
	if p.Owner != "telegram:42" || p.Classes["commit"] != "ask" || p.Classes["spend"] != "forbid" {
		t.Errorf("policy = %+v", p)
	}
}
