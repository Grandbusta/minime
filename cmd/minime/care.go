package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/Grandbusta/minime"
	"github.com/Grandbusta/minime/internal/config"
	"github.com/Grandbusta/minime/internal/decision/questions"
	"github.com/Grandbusta/minime/internal/person"
	"github.com/Grandbusta/minime/internal/providers/openaiclient"
	s1openai "github.com/Grandbusta/minime/internal/providers/system1/openai"
	"github.com/Grandbusta/minime/internal/secret"
	"github.com/Grandbusta/minime/internal/store"
	"github.com/Grandbusta/minime/internal/tools"
)

// backup is `minime backup [file]`. The file is written whole or not at all.
func backup(ctx context.Context, args []string, out io.Writer) error {
	if len(args) > 1 {
		return errors.New("want: backup [file]")
	}
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return person.ErrNoOne
	}
	st, err := store.Open(ctx, url)
	if err != nil {
		return err
	}
	defer st.Close()
	whose, ok, err := st.Whose(ctx)
	if err != nil || !ok {
		return errors.Join(err, errors.New("no one lives in this database yet"))
	}
	now := time.Now().UTC()
	path := fmt.Sprintf("minime-%s-%s.zip", strings.ToLower(strings.Join(strings.Fields(whose), "-")), now.Local().Format("2006-01-02-1504"))
	if len(args) == 1 {
		path = args[0]
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".minime-backup-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	b, err := st.Backup(ctx, f, now)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	fmt.Fprintf(out, "Backed up %s to %s: %d rows in %d tables.\n"+
		"Their secrets are in it, sealed: keep %s safe too, apart from the backup, or they can't be opened.\n",
		b.Whose, path, b.Rows(), len(b.Tables), secret.EnvKey)
	return nil
}

// restore is `minime restore <file>`, into DATABASE_URL.
func restore(ctx context.Context, args []string, out io.Writer) error {
	if len(args) != 1 {
		return errors.New("want: restore <file>, into a new, empty database at DATABASE_URL")
	}
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return errors.New("DATABASE_URL isn't set: it's the new, empty database to restore into")
	}
	f, err := os.Open(args[0])
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	b, err := store.Restore(ctx, url, f, info.Size())
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "Restored %s as they were at %s: %d rows in %d tables.\n"+
		"Their secrets open only with the %s they had then. `minime doctor` checks it.\n",
		b.Whose, b.At.Local().Format("2 Jan 2006 15:04"), b.Rows(), len(b.Tables), secret.EnvKey)
	return nil
}

// doctor is `minime doctor`: everything a person needs to run, checked
// without changing anything but the database's migrations. It goes on past
// a problem where it can, and fails if there was one.
func doctor(ctx context.Context, cfgPath string, out io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	problems := 0
	ok := func(what, format string, a ...any) {
		fmt.Fprintf(out, "✓ %-10s %s\n", what, fmt.Sprintf(format, a...))
	}
	bad := func(what string, err error) {
		problems++
		fmt.Fprintf(out, "✗ %-10s %v\n", what, err)
	}
	stop := func() error { return fmt.Errorf("%d problem(s) found", problems) }

	url := os.Getenv("DATABASE_URL")
	if url == "" {
		bad("database", errors.New("DATABASE_URL isn't set (in .env, or wherever they run)"))
		return stop()
	}
	st, err := store.Open(ctx, url)
	if err != nil {
		bad("database", err)
		return stop()
	}
	defer st.Close()
	ok("database", "reachable and up to date")

	raw := os.Getenv(secret.EnvKey)
	if raw == "" {
		bad("key", fmt.Errorf("%s isn't set: it opens their secrets", secret.EnvKey))
		return stop()
	}
	key, err := secret.ParseKey(raw)
	if err != nil {
		bad("key", err)
		return stop()
	}
	p, err := person.Load(ctx, st, key)
	if errors.Is(err, person.ErrNoOne) {
		if _, serr := os.Stat(filepath.Join(oldSelf, "life.yaml")); serr == nil {
			err = fmt.Errorf("no one lives here yet: start them once to move them in from %s/", oldSelf)
		}
	}
	if err != nil {
		bad("person", err)
		return stop()
	}
	sealed, err := st.Secrets(ctx)
	if err != nil {
		bad("person", err)
		return stop()
	}
	ok("person", "%s, with %d secrets opened by %s and %d skills", p.Life.Name, len(sealed), secret.EnvKey, len(p.Skills))
	for _, u := range p.Unreadable {
		bad("skill", errors.New(u))
	}

	cfg, err := config.Load(minime.Files, cfgPath, p.Living)
	if err == nil {
		if cfg.Initiative.Timezone == "" {
			cfg.Initiative.Timezone = p.Life.Timezone
		}
		err = cfg.ValidateAgent()
	}
	if err != nil {
		bad("config", err)
		return stop()
	}
	_, perr := loadPrompt(cfg.Reasoning.Prompt)
	if _, err := questions.LoadDir(minime.Files, cfg.Decision.QuestionSets); err != nil || perr != nil {
		bad("files", errors.Join(err, perr))
	} else {
		ok("config", "%s, with its prompts and question sets", cfgPath)
	}

	type use struct{ what, model, key, url string }
	uses := []use{{"System 1", orDefault(cfg.Decision.Model, s1openai.DefaultModel), cfg.Decision.APIKey, cfg.Decision.BaseURL},
		{"System 2", cfg.Reasoning.Model, cfg.Reasoning.APIKey, cfg.Reasoning.BaseURL}}
	if e := cfg.Embedding; e.Provider == "openai" {
		uses = append(uses, use{"memory", orDefault(e.Model, embedModel), orDefault(e.APIKey, cfg.Reasoning.APIKey), e.BaseURL})
	}
	for _, u := range uses {
		client := openaiclient.New(u.key, u.url, 20*time.Second)
		if _, err := client.Models.Get(ctx, u.model); err != nil {
			bad("OpenAI", fmt.Errorf("%s (%s): %w", u.what, u.model, err))
		} else {
			ok("OpenAI", "%s: %s", u.what, u.model)
		}
	}

	b, err := bot.New(cfg.Telegram.Token, bot.WithSkipGetMe())
	var me *models.User
	if err == nil {
		me, err = b.GetMe(ctx)
	}
	if err != nil {
		bad("Telegram", fmt.Errorf("the bot token: %w", err))
	} else {
		ok("Telegram", "@%s", me.Username)
		if len(cfg.Telegram.OwnerIDs) == 0 {
			bad("Telegram", errors.New("no owner IDs: every message is ignored"))
		}
		for _, id := range cfg.Telegram.OwnerIDs {
			if _, err := b.GetChat(ctx, &bot.GetChatParams{ChatID: id}); err != nil {
				bad("Telegram", fmt.Errorf("owner %d can't be reached: open @%s and press Start (%w)", id, me.Username, err))
			} else {
				ok("Telegram", "owner %d can be reached", id)
			}
		}
	}

	for _, c := range tools.Connect(ctx, cfg.Tools, cfg.Tools.CallsPerDay, st, slog.New(slog.DiscardHandler)) {
		if c.Err != nil {
			bad("tools", fmt.Errorf("%s: %w", c.Server.Name, c.Err))
		} else {
			ok("tools", "%s: %d to read, %d to act", c.Server.Name, len(c.Tools), len(c.Actions))
		}
		c.Server.Close()
	}

	if problems > 0 {
		return stop()
	}
	fmt.Fprintf(out, "All well: %s is ready to run.\n", p.Life.Name)
	return nil
}
