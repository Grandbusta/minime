package setup

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/joho/godotenv"

	"github.com/Grandbusta/minime/internal/secret"
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

type saved struct {
	url, key string
	files    map[string][]byte
	secrets  map[string]string
}

func newSetup(t *testing.T, answers string) (*Setup, *bytes.Buffer, string, *saved) {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".env.example"), []byte("# Where they live.\nDATABASE_URL=\n# The key to their secrets.\nMINIME_KEY=\n"), 0o600)
	out, got := &bytes.Buffer{}, &saved{}
	save := func(_ context.Context, url, key string, files map[string][]byte, secrets map[string]string) error {
		*got = saved{url, key, files, secrets}
		return nil
	}
	return &Setup{Ask: &script{in: bufio.NewReader(strings.NewReader(answers)), out: out}, Out: out, Lives: "../../lives",
		Env: filepath.Join(dir, ".env"), Example: filepath.Join(dir, ".env.example"), BotName: fakeBotName, FirstMessage: fakeFirstMessage,
		Database: fakeDatabase, Save: save, Now: func() time.Time { return time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC) }}, out, dir, got
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
		"y",            // Elsa lives in the database .env points to: start someone new anyway
		"9", itoa(mei), // a life: out of range, then Mei
		"123:bad", "123:good", // a bad token, then a good one
		"sk-$ecret&key", "", // the key; the model's default
		"postgres://elsas", "postgres://down", "postgres://meis", // Elsa's, unreachable, then free
		"",        // her name for you: Sam, from Telegram
		itoa(rel), // mentor
		"y",       // start her now
	}, "\n") + "\n"
	s, out, dir, got := newSetup(t, answers)
	elsas := "DATABASE_URL=postgres://elsas\nTELEGRAM_BOT_TOKEN=elsas-bot\nTELEGRAM_OWNER_ID=7\n"
	os.WriteFile(filepath.Join(dir, ".env"), []byte(elsas), 0o600)
	res, err := s.Run(context.Background())
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if res.Name != "Mei" || !res.Start || res.Env["DATABASE_URL"] != "postgres://meis" || res.Env["MINIME_KEY"] != got.key ||
		!slices.Contains(res.Dropped, "TELEGRAM_BOT_TOKEN") {
		t.Errorf("result = %+v", res)
	}
	if got.url != "postgres://meis" || got.secrets["TELEGRAM_BOT_TOKEN"] != "123:good" || got.secrets["TELEGRAM_OWNER_ID"] != "42" ||
		got.secrets["OPENAI_API_KEY"] != "sk-$ecret&key" || got.secrets["OPENAI_MODEL"] != "gpt-6-luna" || len(got.secrets["CONSOLE_TOKEN"]) < 16 {
		t.Errorf("saved %s with %v", got.url, slices.Sorted(maps.Keys(got.secrets)))
	}
	if _, err := secret.ParseKey(got.key); err != nil {
		t.Error(err)
	}

	// .env says only where Mei lives and holds her key; Elsa's waits beside it.
	raw, _ := os.ReadFile(filepath.Join(dir, ".env"))
	vars, err := godotenv.Unmarshal(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(vars) != 2 || vars["DATABASE_URL"] != "postgres://meis" || vars["MINIME_KEY"] != got.key || !strings.Contains(string(raw), "# Where they live.") {
		t.Errorf(".env =\n%s", raw)
	}
	if old, _ := os.ReadFile(filepath.Join(dir, ".env.elsa")); string(old) != elsas {
		t.Errorf(".env.elsa = %q", old)
	}

	life, err := self.ParseLife(got.files["life.yaml"])
	if err != nil {
		t.Fatal(err)
	}
	if life.Name != "Mei" || life.Family[0].Name != "Sam" || life.Family[0].Relation != "mentor" || life.Family[0].Account != "telegram:42" ||
		life.Family[0].Circle != "school" {
		t.Errorf("life = %+v", life.Family)
	}
}

func TestSomeoneAlreadyLivesHere(t *testing.T) {
	s, _, dir, got := newSetup(t, "n\n")
	os.WriteFile(filepath.Join(dir, ".env"), []byte("DATABASE_URL=postgres://elsas\n"), 0o600)
	if _, err := s.Run(context.Background()); !Stopped(err) {
		t.Fatalf("err = %v; want stopped", err)
	}
	if raw, _ := os.ReadFile(filepath.Join(dir, ".env")); string(raw) != "DATABASE_URL=postgres://elsas\n" || got.url != "" {
		t.Errorf(".env = %q; saved to %q", raw, got.url)
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
