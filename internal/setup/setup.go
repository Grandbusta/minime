// Package setup is `minime new`: it starts a person from one of the lives in
// lives/, in their own database. Nothing is written until every answer is in;
// then .env holds only where they live (DATABASE_URL) and the key to their
// secrets (MINIME_KEY).
package setup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"

	"github.com/Grandbusta/minime/internal/secret"
	"github.com/Grandbusta/minime/internal/self"
)

type BotName func(ctx context.Context, token string) (string, error)

// FirstMessage returns the sender's user ID and first name, and marks the
// message read so the person doesn't answer it later.
type FirstMessage func(ctx context.Context, token string, wait time.Duration) (int64, string, error)

// Database says whose memory a database holds ("" if no one's).
type Database func(ctx context.Context, url string) (whose string, err error)

type LocalPostgres func(ctx context.Context, name string) (string, error)

// Save makes the database at url theirs: files are their sections by file
// name, secrets sealed with key.
type Save func(ctx context.Context, url, key string, files map[string][]byte, secrets map[string]string) error

type Prompter interface {
	// Input asks for text; current is offered (masked when secret) and kept
	// on an empty answer. There must be an answer.
	Input(title, current string, secret bool) (string, error)
	Select(title string, options []string) (int, error)
	Confirm(title string, def bool) (bool, error)
}

type Setup struct {
	Ask Prompter
	Out io.Writer

	// Files has Lives, the folder of lives to start from, and Example, what
	// a new .env is written from. Env is on disk.
	Files   fs.FS
	Lives   string
	Example string
	Env     string

	BotName      BotName
	FirstMessage FirstMessage
	Database     Database
	Postgres     LocalPostgres // nil: none offered
	Save         Save
	Now          func() time.Time
}

type Result struct {
	Name string
	Env  map[string]string // all of .env now
	// Dropped are what the .env before had besides: no longer theirs.
	Dropped []string
	Start   bool
}

var errStop = errors.New("stopped; nothing was written")

func (s *Setup) Run(ctx context.Context) (Result, error) {
	if s.Now == nil {
		s.Now = time.Now
	}
	s.say("Let's start a person.\n")

	before := s.readEnv()
	have, whose := before, ""
	if url := have["DATABASE_URL"]; url != "" {
		whose, _ = s.Database(ctx, url)
	}
	if whose != "" {
		s.say("\n%s lives in the database .env points to. Starting someone new leaves %s as they are, and moves their .env to "+
			"%s: `cp %[3]s .env` brings them back.\n", whose, whose, s.Env+"."+strings.ToLower(whose))
		if ok, err := s.Ask.Confirm("Start someone new instead?", false); err != nil || !ok {
			return Result{}, errStop
		}
		have = map[string]string{} // their bot and their owner are theirs
	}

	tmpl, err := s.pickLife()
	if err != nil {
		return Result{}, err
	}
	env, err := s.keys(ctx, tmpl, have)
	if err != nil {
		return Result{}, err
	}
	owner, err := s.owner(ctx, tmpl, env, have)
	if err != nil {
		return Result{}, err
	}
	files, err := tmpl.Render(owner)
	if err != nil {
		return Result{}, err
	}

	url, key := env["DATABASE_URL"], secret.NewKey()
	delete(env, "DATABASE_URL")
	if err := s.Save(ctx, url, key, files, env); err != nil {
		return Result{}, err
	}
	kept := map[string]string{"DATABASE_URL": url, secret.EnvKey: key}
	if err := s.writeEnv(kept, whose); err != nil {
		return Result{}, err
	}
	var dropped []string
	for k := range before {
		if _, ok := kept[k]; !ok {
			dropped = append(dropped, k)
		}
	}
	s.say("\n%s is ready. Everything about them is in their database: their life, how they live, their keys, and a starting "+
		"judgment for you to review in the console. .env only says where that is (DATABASE_URL) and holds the key to their "+
		"secrets (%s): keep both safe.\n", tmpl.Name, secret.EnvKey)
	start, err := s.Ask.Confirm(fmt.Sprintf("Start %s now?", tmpl.Name), true)
	return Result{Name: tmpl.Name, Env: kept, Dropped: dropped, Start: start && err == nil}, nil
}

func (s *Setup) pickLife() (self.Template, error) {
	ts, err := self.Templates(s.Files, s.Lives)
	if err != nil {
		return self.Template{}, err
	}
	if len(ts) == 0 {
		return self.Template{}, fmt.Errorf("no lives in %s", s.Lives)
	}
	labels := make([]string, len(ts))
	for i, t := range ts {
		labels[i] = t.Name + ": " + t.About
	}
	n, err := s.Ask.Select("Who would you like to start?", labels)
	if err != nil {
		return self.Template{}, err
	}
	return ts[n], nil
}

// keys are their secrets, and DATABASE_URL; have is what .env has now.
func (s *Setup) keys(ctx context.Context, tmpl self.Template, have map[string]string) (map[string]string, error) {
	env := map[string]string{}

	s.say("\nTelegram: %s talks with you through a bot. Make one with @BotFather (/newbot) and paste its token.\n", tmpl.Name)
	for {
		token, err := s.Ask.Input("Bot token", have["TELEGRAM_BOT_TOKEN"], true)
		if err != nil {
			return nil, err
		}
		username, err := s.BotName(ctx, token)
		if err != nil {
			s.say("  That token doesn't work (%v). Try again.\n", err)
			continue
		}
		s.say("  Found your bot: @%s\n", username)
		env["TELEGRAM_BOT_TOKEN"], env["bot"] = token, username
		break
	}

	s.say("\nOpenAI: %s thinks with OpenAI's models (decisions, replies, web search).\n", tmpl.Name)
	key, err := s.Ask.Input("OpenAI API key", have["OPENAI_API_KEY"], true)
	if err != nil {
		return nil, err
	}
	env["OPENAI_API_KEY"] = key
	if env["OPENAI_MODEL"], err = s.Ask.Input("Model", orDefault(have["OPENAI_MODEL"], "gpt-6-luna"), false); err != nil {
		return nil, err
	}

	s.say("\nDatabase: %s's memory lives in Postgres, one database per person.\n", tmpl.Name)
	for {
		url := have["DATABASE_URL"]
		if url == "" && s.Postgres != nil && s.confirm("You have no database set. Start one on this machine with Docker?", true) {
			if url, err = s.Postgres(ctx, tmpl.Key); err != nil {
				s.say("  Couldn't start it (%v).\n", err)
				url = ""
			} else {
				s.say("  Started.\n")
			}
		}
		if url, err = s.Ask.Input("Postgres URL (postgres://user:password@host:5432/db)", url, false); err != nil {
			return nil, err
		}
		whose, err := s.Database(ctx, url)
		switch {
		case err != nil:
			s.say("  Can't connect to it (%v). Try again.\n", err)
			have["DATABASE_URL"] = ""
			continue
		case whose != "":
			s.say("  That database holds %s's memory. %s needs their own: give another one.\n", whose, tmpl.Name)
			have["DATABASE_URL"] = ""
			continue
		}
		env["DATABASE_URL"] = url
		break
	}

	env["CONSOLE_TOKEN"] = have["CONSOLE_TOKEN"]
	if len(env["CONSOLE_TOKEN"]) < 16 { // for the console, anywhere but localhost
		b := make([]byte, 24)
		rand.Read(b)
		env["CONSOLE_TOKEN"] = hex.EncodeToString(b)
	}
	return env, nil
}

func (s *Setup) owner(ctx context.Context, tmpl self.Template, env, have map[string]string) (self.Owner, error) {
	firstName := ""
	if id := have["TELEGRAM_OWNER_ID"]; id != "" && s.confirm(fmt.Sprintf("Keep %s as your Telegram user ID?", id), true) {
		env["TELEGRAM_OWNER_ID"] = id
	} else {
		s.say("\nNow message @%s on Telegram (anything, like \"hi\") so I know it's you. Waiting...\n", env["bot"])
		for {
			id, name, err := s.FirstMessage(ctx, env["TELEGRAM_BOT_TOKEN"], 3*time.Minute)
			if err == nil {
				env["TELEGRAM_OWNER_ID"], firstName = strconv.FormatInt(id, 10), name
				s.say("  Got it: %s (user ID %d). Only you will be able to talk to %s.\n", orDefault(name, "you"), id, tmpl.Name)
				break
			}
			if !s.confirm(fmt.Sprintf("No message yet (%v). Keep waiting?", err), true) {
				return self.Owner{}, errStop
			}
		}
	}
	delete(env, "bot")

	name, err := s.Ask.Input(fmt.Sprintf("What should %s call you?", tmpl.Name), firstName, false)
	if err != nil {
		return self.Owner{}, err
	}
	labels := make([]string, len(tmpl.Relations))
	for i, r := range tmpl.Relations {
		labels[i] = "their " + r
	}
	n, err := s.Ask.Select(fmt.Sprintf("Who are you to %s?", tmpl.Name), labels)
	if err != nil {
		return self.Owner{}, err
	}
	return self.Owner{Name: name, Relation: tmpl.Relations[n], Account: "telegram:" + env["TELEGRAM_OWNER_ID"]}, nil
}

// writeEnv writes a new .env from the example. One already there is moved
// aside first: to .env.<whose> if someone lived there, so they can come back.
func (s *Setup) writeEnv(env map[string]string, whose string) error {
	if _, err := os.Stat(s.Env); err == nil {
		aside := s.Env + ".previous"
		if whose != "" {
			aside = s.Env + "." + strings.ToLower(whose)
		}
		if _, err := os.Stat(aside); err == nil {
			aside += "-" + s.Now().Format("2006-01-02-150405")
		}
		if err := os.Rename(s.Env, aside); err != nil {
			return err
		}
	}
	raw, err := fs.ReadFile(s.Files, s.Example)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	text := string(raw)
	for _, k := range []string{"DATABASE_URL", secret.EnvKey} {
		text = SetEnv(text, k, env[k])
	}
	return os.WriteFile(s.Env, []byte(text), 0o600)
}

// SetEnv keeps everything else in text, comments too. The line is written by
// godotenv, so it reads back exactly.
func SetEnv(text, key, value string) string {
	line, err := godotenv.Marshal(map[string]string{key: value})
	if err != nil {
		line = key + "=" + value
	}
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		if k, _, ok := strings.Cut(strings.TrimPrefix(strings.TrimSpace(l), "export "), "="); ok && strings.TrimSpace(k) == key {
			lines[i] = line
			return strings.Join(lines, "\n")
		}
	}
	if text != "" && !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return text + line + "\n"
}

func (s *Setup) readEnv() map[string]string {
	have, err := godotenv.Read(s.Env)
	if err != nil {
		return map[string]string{}
	}
	return have
}

func (s *Setup) say(format string, args ...any) { fmt.Fprintf(s.Out, format, args...) }

// confirm asks yes or no; stopping (Ctrl+C) is no.
func (s *Setup) confirm(question string, def bool) bool {
	ok, err := s.Ask.Confirm(question, def)
	return ok && err == nil
}

func orDefault(s, def string) string {
	if s = strings.TrimSpace(s); s != "" {
		return s
	}
	return def
}

func Stopped(err error) bool { return errors.Is(err, errStop) }
