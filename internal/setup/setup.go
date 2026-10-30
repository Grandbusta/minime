// Package setup is `minime new`: it starts a person from one of the lives in
// lives/. It asks which life, the keys it needs (writing .env), catches the
// owner's Telegram ID from their first message to the bot, asks who they are
// to the person, and writes the person's folder (self/). Nothing is written
// until every answer is in.
package setup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"

	"minime/internal/self"
)

// Telegram is what setup needs from a bot (telegram.Client, per token).
type Telegram interface {
	// Bot checks a token and returns the bot's @username.
	Bot(ctx context.Context, token string) (string, error)
	// FirstMessage waits for someone to message the bot and returns who: their
	// user ID and first name. The message is marked read, so the person
	// doesn't answer it later.
	FirstMessage(ctx context.Context, token string, wait time.Duration) (int64, string, error)
}

// Database is what setup needs from a Postgres URL: whether it can connect,
// and whose memory it holds ("" if no one's).
type Database func(ctx context.Context, url string) (whose string, err error)

// LocalPostgres starts a Postgres in Docker for the person and returns its
// URL; nil when Docker isn't there.
type LocalPostgres func(ctx context.Context, name string) (string, error)

// Prompter asks the owner things: huh in a terminal, a script in tests.
type Prompter interface {
	// Input asks for text; current is offered (masked when secret) and kept
	// on an empty answer. There must be an answer.
	Input(title, current string, secret bool) (string, error)
	// Select asks which of options, and returns its index.
	Select(title string, options []string) (int, error)
	// Confirm asks yes or no.
	Confirm(title string, def bool) (bool, error)
}

type Setup struct {
	Ask Prompter
	Out io.Writer // what it tells the owner along the way

	Lives   string // where the lives are, e.g. "lives"
	Self    string // the person's folder, e.g. "self"
	Env     string // the .env file, e.g. ".env"
	Example string // what a new .env starts from, e.g. ".env.example"

	Telegram Telegram
	Database Database
	Postgres LocalPostgres // nil: none offered
	Now      func() time.Time
}

// Result is what setup decided: who was started, and the environment it wrote.
type Result struct {
	Name  string
	Env   map[string]string
	Start bool // start them now
}

// errStop means the owner chose to stop.
var errStop = errors.New("stopped; nothing was written")

// Run asks everything, then writes .env and the person's folder.
func (s *Setup) Run(ctx context.Context) (Result, error) {
	if s.Now == nil {
		s.Now = time.Now
	}
	s.say("Let's start a person.\n")

	// Someone already lives here?
	if old, err := self.Load(filepath.Join(s.Self, "life.yaml")); err == nil {
		s.say("\n%s already lives in %s/. Starting someone new moves %s's files to %s/previous/ (their memory stays in their database).\n",
			old.Name, s.Self, old.Name, s.Self)
		if ok, err := s.Ask.Confirm("Start someone new instead?", false); err != nil || !ok {
			return Result{}, errStop
		}
	}

	tmpl, err := s.pickLife()
	if err != nil {
		return Result{}, err
	}
	env, err := s.keys(ctx, tmpl)
	if err != nil {
		return Result{}, err
	}
	owner, err := s.owner(ctx, tmpl, env)
	if err != nil {
		return Result{}, err
	}
	files, err := tmpl.Render(owner)
	if err != nil {
		return Result{}, err
	}

	if err := s.writeEnv(env); err != nil {
		return Result{}, err
	}
	if err := s.writeSelf(files); err != nil {
		return Result{}, err
	}
	s.say("\n%s is ready: %s/ has their life, how they live, and a starting judgment for you to review in the console.\n", tmpl.Name, s.Self)
	start, err := s.Ask.Confirm(fmt.Sprintf("Start %s now?", tmpl.Name), true)
	return Result{Name: tmpl.Name, Env: env, Start: start && err == nil}, nil
}

// pickLife lists the lives and asks which one.
func (s *Setup) pickLife() (self.Template, error) {
	ts, err := self.Templates(s.Lives)
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

// keys asks for what .env needs, keeping what's there when the owner wants.
func (s *Setup) keys(ctx context.Context, tmpl self.Template) (map[string]string, error) {
	have := s.readEnv()
	env := map[string]string{}

	s.say("\nTelegram: %s talks with you through a bot. Make one with @BotFather (/newbot) and paste its token.\n", tmpl.Name)
	for {
		token, err := s.Ask.Input("Bot token", have["TELEGRAM_BOT_TOKEN"], true)
		if err != nil {
			return nil, err
		}
		username, err := s.Telegram.Bot(ctx, token)
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
		case whose != "" && whose != tmpl.Name:
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
	env["TELEGRAM_OWNER_ID"] = have["TELEGRAM_OWNER_ID"]
	return env, nil
}

// owner finds who the owner is: their Telegram ID (from their first message
// to the bot), what the person calls them, and who they are to them.
func (s *Setup) owner(ctx context.Context, tmpl self.Template, env map[string]string) (self.Owner, error) {
	firstName := ""
	if id := env["TELEGRAM_OWNER_ID"]; id != "" && s.confirm(fmt.Sprintf("Keep %s as your Telegram user ID?", id), true) {
		// kept
	} else {
		s.say("\nNow message @%s on Telegram (anything, like \"hi\") so I know it's you. Waiting...\n", env["bot"])
		for {
			id, name, err := s.Telegram.FirstMessage(ctx, env["TELEGRAM_BOT_TOKEN"], 3*time.Minute)
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

// writeEnv sets the keys in .env, keeping everything else in it (or in the
// example, for a new one).
func (s *Setup) writeEnv(env map[string]string) error {
	raw, err := os.ReadFile(s.Env)
	if errors.Is(err, os.ErrNotExist) {
		raw, err = os.ReadFile(s.Example)
		if errors.Is(err, os.ErrNotExist) {
			raw, err = nil, nil
		}
	}
	if err != nil {
		return err
	}
	text := string(raw)
	for _, k := range []string{"TELEGRAM_BOT_TOKEN", "TELEGRAM_OWNER_ID", "DATABASE_URL", "OPENAI_API_KEY", "OPENAI_MODEL", "CONSOLE_TOKEN"} {
		if v, ok := env[k]; ok {
			text = SetEnv(text, k, v)
		}
	}
	return os.WriteFile(s.Env, []byte(text), 0o600)
}

// SetEnv sets key to value in a .env file's text: on its line, or appended,
// keeping everything else (comments too). The line is written by godotenv,
// so it reads back exactly.
func SetEnv(text, key, value string) string {
	line, err := godotenv.Marshal(map[string]string{key: value})
	if err != nil { // only for a key it can't write, which these never are
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

// writeSelf writes the person's folder, moving anyone already there to
// previous/ first.
func (s *Setup) writeSelf(files map[string][]byte) error {
	if err := os.MkdirAll(s.Self, 0o755); err != nil {
		return err
	}
	var moved bool
	prev := filepath.Join(s.Self, "previous", s.Now().Format("2006-01-02-150405"))
	for _, f := range self.Files {
		path := filepath.Join(s.Self, f)
		if _, err := os.Stat(path); err != nil {
			continue
		}
		if err := os.MkdirAll(prev, 0o755); err != nil {
			return err
		}
		if err := os.Rename(path, filepath.Join(prev, f)); err != nil {
			return err
		}
		moved = true
	}
	if moved {
		s.say("\nThe person who lived here is in %s/.\n", prev)
	}
	for _, f := range self.Files {
		if err := os.WriteFile(filepath.Join(s.Self, f), files[f], 0o600); err != nil {
			return err
		}
	}
	return nil
}

// readEnv is what .env has now, by key.
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

// Stopped reports whether err is the owner choosing to stop.
func Stopped(err error) bool { return errors.Is(err, errStop) }
