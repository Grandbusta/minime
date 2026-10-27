package config

import (
	"os"
	"path/filepath"
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
