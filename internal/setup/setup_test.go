package setup

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/joho/godotenv"

	"github.com/Grandbusta/minime/internal/self"
)

func fakeBotName(_ context.Context, token string) (string, error) {
	if token != "123:good" {
		return "", errors.New("401 Unauthorized")
	}
	return "mei_bot", nil
}

func fakeFirstMessage(context.Context, string, time.Duration) (int64, string, error) {
	return 42, "Sam", nil
}

func fakeDatabase(_ context.Context, url string) (string, error) {
	switch url {
	case "postgres://elsas":
		return "Elsa", nil
	case "postgres://down":
		return "", errors.New("connection refused")
	}
	return "", nil
}

func newSetup(t *testing.T, answers string) (*Setup, *bytes.Buffer, string) {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".env.example"), []byte("# Copy to .env.\nTELEGRAM_BOT_TOKEN=\n# Your ID.\nTELEGRAM_OWNER_ID=\nDATABASE_URL=\n"), 0o600)
	out := &bytes.Buffer{}
	return &Setup{Ask: &script{in: bufio.NewReader(strings.NewReader(answers)), out: out}, Out: out, Lives: "../../lives", Self: filepath.Join(dir, "self"),
		Env: filepath.Join(dir, ".env"), Example: filepath.Join(dir, ".env.example"), BotName: fakeBotName, FirstMessage: fakeFirstMessage, Database: fakeDatabase,
		Now: func() time.Time { return time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC) }}, out, dir
}

func TestStartingMei(t *testing.T) {
	ts, _ := self.Templates("../../lives")
	mei, rel := 0, 0
	for i, tm := range ts {
		if tm.Key == "mei" {
			mei = i + 1
			for j, r := range tm.Relations {
				if r == "mentor" {
					rel = j + 1
				}
			}
		}
	}
	answers := strings.Join([]string{
		"9", itoa(mei), // a life: out of range, then Mei
		"123:bad", "123:good", // a bad token, then a good one
		"sk-$ecret&key", "", // the key; the model's default
		"postgres://elsas", "postgres://down", "postgres://meis", // Elsa's, unreachable, then free
		"",        // her name for you: Sam, from Telegram
		itoa(rel), // mentor
		"y",       // start her now
	}, "\n") + "\n"
	s, out, dir := newSetup(t, answers)
	res, err := s.Run(context.Background())
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if res.Name != "Mei" || !res.Start || res.Env["TELEGRAM_OWNER_ID"] != "42" || res.Env["DATABASE_URL"] != "postgres://meis" ||
		res.Env["OPENAI_MODEL"] != "gpt-6-luna" || len(res.Env["CONSOLE_TOKEN"]) < 16 {
		t.Errorf("result = %+v", res)
	}
	for _, want := range []string{"That token doesn't work", "Found your bot: @mei_bot", "holds Elsa's memory", "Can't connect",
		"Got it: Sam (user ID 42)", "their mentor"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output is missing %q:\n%s", want, out)
		}
	}

	raw, _ := os.ReadFile(filepath.Join(dir, ".env"))
	if !strings.Contains(string(raw), "# Your ID.\nTELEGRAM_OWNER_ID=42\n") || !strings.Contains(string(raw), "OPENAI_API_KEY=") {
		t.Errorf(".env =\n%s", raw)
	}
	vars, err := godotenv.Unmarshal(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if vars["OPENAI_API_KEY"] != "sk-$ecret&key" {
		t.Errorf("the key reads back as %q", vars["OPENAI_API_KEY"])
	}

	life, err := self.Load(filepath.Join(dir, "self", "life.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if life.Name != "Mei" || life.Family[0].Name != "Sam" || life.Family[0].Relation != "mentor" || life.Family[0].Account != "telegram:42" ||
		life.Family[0].Circle != "school" {
		t.Errorf("life = %+v", life.Family)
	}
	for _, f := range self.Files {
		if _, err := os.Stat(filepath.Join(dir, "self", f)); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
}

func TestSomeoneAlreadyLivesHere(t *testing.T) {
	s, _, dir := newSetup(t, "n\n")
	os.MkdirAll(filepath.Join(dir, "self"), 0o755)
	os.WriteFile(filepath.Join(dir, "self", "life.yaml"), []byte("name: Daniel\nstory: [Daniel is 18.]\n"), 0o600)
	if _, err := s.Run(context.Background()); !Stopped(err) {
		t.Fatalf("err = %v; want stopped", err)
	}
	if raw, _ := os.ReadFile(filepath.Join(dir, "self", "life.yaml")); !strings.Contains(string(raw), "Daniel") {
		t.Error("Daniel's life was touched")
	}
	if _, err := os.Stat(filepath.Join(dir, ".env")); err == nil {
		t.Error(".env was written")
	}
}

func TestReplacingSomeoneMovesThemAside(t *testing.T) {
	ts, _ := self.Templates("../../lives")
	answers := "y\n1\n123:good\nsk-x\n\npostgres://new\n\n1\nn\n"
	s, out, dir := newSetup(t, answers)
	os.MkdirAll(filepath.Join(dir, "self"), 0o755)
	os.WriteFile(filepath.Join(dir, "self", "life.yaml"), []byte("name: Old\nstory: [Old is old.]\n"), 0o600)
	res, err := s.Run(context.Background())
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if res.Start || res.Name != ts[0].Name {
		t.Errorf("result = %+v", res)
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "self", "previous", "2026-10-30-120000", "life.yaml")); err != nil || !strings.Contains(string(raw), "Old") {
		t.Errorf("the old life wasn't moved aside: %v", err)
	}
}

func TestSetEnv(t *testing.T) {
	text := "# c\nA=1\nexport B=2\n"
	got := SetEnv(SetEnv(SetEnv(text, "A", "x y"), "B", "plain"), "C", `it's "$5" \ ok`)
	if !strings.HasPrefix(got, "# c\nA=") || strings.Count(got, "\n") != 4 {
		t.Errorf("got %q: want the comment kept and A, B in place, C added", got)
	}
	vars, err := godotenv.Unmarshal(got)
	if err != nil || vars["A"] != "x y" || vars["B"] != "plain" || vars["C"] != `it's "$5" \ ok` {
		t.Errorf("reads back as %q, %v", vars, err)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// script answers setup's questions one line each, and echoes them to out.
type script struct {
	in  *bufio.Reader
	out *bytes.Buffer
}

func (s *script) line() (string, error) {
	l, err := s.in.ReadString('\n')
	if err != nil && l == "" {
		return "", errStop
	}
	return strings.TrimSpace(l), nil
}

func (s *script) Input(title, current string, _ bool) (string, error) {
	for {
		s.out.WriteString(title + "\n")
		l, err := s.line()
		if err != nil {
			return "", err
		}
		if l == "" {
			l = current
		}
		if l != "" {
			return l, nil
		}
	}
}

func (s *script) Select(title string, options []string) (int, error) {
	s.out.WriteString(title + "\n" + strings.Join(options, "\n") + "\n")
	for {
		l, err := s.line()
		if err != nil {
			return 0, err
		}
		if n, err := strconv.Atoi(l); err == nil && n >= 1 && n <= len(options) {
			return n - 1, nil
		}
	}
}

func (s *script) Confirm(title string, def bool) (bool, error) {
	s.out.WriteString(title + "\n")
	l, err := s.line()
	if err != nil {
		return false, err
	}
	switch strings.ToLower(l) {
	case "y", "yes":
		return true, nil
	case "n", "no":
		return false, nil
	}
	return def, nil
}
