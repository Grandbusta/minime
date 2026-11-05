package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/joho/godotenv"
	"github.com/oklog/ulid/v2"

	"github.com/Grandbusta/minime"
	"github.com/Grandbusta/minime/internal/agentskill"
	"github.com/Grandbusta/minime/internal/config"
	"github.com/Grandbusta/minime/internal/memory"
	"github.com/Grandbusta/minime/internal/person"
	"github.com/Grandbusta/minime/internal/secret"
	"github.com/Grandbusta/minime/internal/self"
	"github.com/Grandbusta/minime/internal/setup"
	"github.com/Grandbusta/minime/internal/store"
)

// oldSelf is the person's folder from before they lived in their database:
// read once, when they move in.
const oldSelf = "self"

// openStore opens their database, with the key to their secrets. The first
// time, it moves them in from their old folder.
func openStore(ctx context.Context) (*store.Store, secret.Key, error) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return nil, secret.Key{}, person.ErrNoOne
	}
	st, err := store.Open(ctx, url)
	if err != nil {
		return nil, secret.Key{}, err
	}
	key, err := personKey(ctx, st)
	if err == nil {
		err = moveIn(ctx, st, key)
	}
	if err != nil {
		st.Close()
		return nil, secret.Key{}, err
	}
	return st, key, nil
}

// personKey is MINIME_KEY. Someone moving in from their old folder is given
// one, kept in .env beside DATABASE_URL.
func personKey(ctx context.Context, st *store.Store) (secret.Key, error) {
	if v := os.Getenv(secret.EnvKey); v != "" {
		return secret.ParseKey(v)
	}
	_, lives, err := st.Self(ctx, memory.SectionLife)
	if err != nil {
		return secret.Key{}, err
	}
	_, inFolder := os.Stat(filepath.Join(oldSelf, "life.yaml"))
	_, hasEnv := os.Stat(".env")
	if lives || inFolder != nil || hasEnv != nil {
		return secret.Key{}, fmt.Errorf("%s isn't set: it opens their secrets, and lives beside DATABASE_URL (in .env, or wherever "+
			"they run). A new one is made with `openssl rand -hex 32`", secret.EnvKey)
	}
	k := secret.NewKey()
	raw, err := os.ReadFile(".env")
	if err != nil {
		return secret.Key{}, err
	}
	if err := os.WriteFile(".env", []byte(setup.SetEnv(string(raw), secret.EnvKey, k)), 0o600); err != nil {
		return secret.Key{}, err
	}
	os.Setenv(secret.EnvKey, k)
	fmt.Fprintf(os.Stderr, "Made %s, the key to their secrets, and put it in .env. Keep it safe and with DATABASE_URL: "+
		"without it their secrets can't be read.\n", secret.EnvKey)
	return secret.ParseKey(k)
}

func moveIn(ctx context.Context, st *store.Store, key secret.Key) error {
	im, err := person.Import(ctx, st, key, oldSelf, time.Now().UTC())
	if err != nil || im.Name == "" {
		return err
	}
	fmt.Fprintf(os.Stderr, "%s moved into their database: their life, how they live, your judgment, secrets %s, skills %s.\n"+
		"%s/ is no longer read; their .env only needs DATABASE_URL and %s now.\n",
		im.Name, orDefault(strings.Join(im.Secrets, ", "), "none"), orDefault(strings.Join(im.Skills, ", "), "none"), oldSelf, secret.EnvKey)
	for _, s := range im.Skipped {
		fmt.Fprintf(os.Stderr, "Not a skill, left out: %s\n", s)
	}
	return nil
}

// selfCommand is `minime self`: their life, how they live and your
// judgment, as YAML.
func selfCommand(ctx context.Context, st *store.Store, key secret.Key, cfgPath string, args []string, out io.Writer) error {
	const usage = "want: self export <section|folder>, import <file|folder>, edit <section>, history <section> or rollback <section> <version> " +
		"(sections: life, living, judgment)"
	if len(args) == 0 {
		return errors.New(usage)
	}
	section := func(i int) (string, error) {
		if len(args) <= i || !slices.Contains(memory.Sections, args[i]) {
			return "", errors.New(usage)
		}
		return args[i], nil
	}
	switch args[0] {
	case "export":
		if len(args) != 2 {
			return errors.New(usage)
		}
		if s, err := section(1); err == nil {
			v, _, err := st.Self(ctx, s)
			if err != nil {
				return err
			}
			_, err = io.WriteString(out, v.Doc)
			return err
		}
		return exportTo(ctx, st, args[1], out)
	case "import":
		if len(args) != 2 {
			return errors.New(usage)
		}
		return importFrom(ctx, st, key, cfgPath, args[1], out)
	case "edit":
		s, err := section(1)
		if err != nil {
			return err
		}
		return editSelf(ctx, st, key, cfgPath, s, out)
	case "history":
		s, err := section(1)
		if err != nil {
			return err
		}
		vs, err := st.SelfHistory(ctx, s, 50)
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "VERSION\tAT\tFROM\tLINES\t")
		for i, v := range vs {
			fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\n", v.ID, v.At.Local().Format("2006-01-02 15:04"), v.Source, strings.Count(v.Doc, "\n"),
				map[bool]string{true: "in use"}[i == 0])
		}
		return w.Flush()
	case "rollback":
		s, err := section(1)
		if err != nil || len(args) != 3 {
			return errors.New(usage)
		}
		vs, err := st.SelfHistory(ctx, s, 1000)
		if err != nil {
			return err
		}
		i := slices.IndexFunc(vs, func(v memory.SelfVersion) bool { return v.ID.String() == args[2] })
		switch {
		case i < 0:
			return fmt.Errorf("%s has no version %s (minime self history %s lists them)", s, args[2], s)
		case i == 0:
			return fmt.Errorf("%s is the version in use", args[2])
		}
		if err := saveSelf(ctx, st, key, cfgPath, memory.SelfRolledBack, map[string]string{s: vs[i].Doc}); err != nil {
			return err
		}
		fmt.Fprintf(out, "%s is back to the version from %s. It applies when they next start.\n", s, vs[i].At.Local().Format("2006-01-02 15:04"))
		return nil
	}
	return errors.New(usage)
}

// saveSelf checks docs (section → YAML) together, then saves them as new
// versions in one go.
func saveSelf(ctx context.Context, st *store.Store, key secret.Key, cfgPath, source string, docs map[string]string) error {
	life, ok := docs[memory.SectionLife]
	if !ok {
		v, _, err := st.Self(ctx, memory.SectionLife)
		if err != nil {
			return err
		}
		life = v.Doc
	}
	l, err := self.ParseLife([]byte(life))
	if err != nil {
		return err
	}
	if j, ok := docs[memory.SectionJudgment]; ok {
		if _, err := self.ParseJudgment([]byte(j)); err != nil {
			return err
		}
	}
	if living, ok := docs[memory.SectionLiving]; ok {
		if err := checkLiving(ctx, st, key, cfgPath, []byte(living), l.Timezone); err != nil {
			return err
		}
	}
	now, name := time.Now().UTC(), ""
	if _, ok := docs[memory.SectionLife]; ok {
		name = l.Name
	}
	var vs []memory.SelfVersion
	for i, s := range memory.Sections {
		if doc, ok := docs[s]; ok {
			vs = append(vs, memory.SelfVersion{ID: ulid.Make(), Section: s, Doc: doc, Source: source, At: now.Add(time.Duration(i) * time.Millisecond)})
		}
	}
	return st.SaveSelf(ctx, name, vs...)
}

// checkLiving checks it the way they're checked when they start.
func checkLiving(ctx context.Context, st *store.Store, key secret.Key, cfgPath string, living []byte, zone string) error {
	if _, err := person.Env(ctx, st, key); err != nil {
		return err
	}
	cfg, err := config.Load(minime.Files, cfgPath, living)
	if err != nil {
		return err
	}
	if cfg.Initiative.Timezone == "" {
		cfg.Initiative.Timezone = zone
	}
	if err := cfg.ValidateAgent(); err != nil {
		return err
	}
	_, err = cfg.Policy()
	return err
}

func editSelf(ctx context.Context, st *store.Store, key secret.Key, cfgPath, section string, out io.Writer) error {
	v, _, err := st.Self(ctx, section)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "minime-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, person.Files[section])
	if err := os.WriteFile(path, []byte(v.Doc), 0o600); err != nil {
		return err
	}
	editor := strings.Fields(orDefault(os.Getenv("VISUAL"), orDefault(os.Getenv("EDITOR"), "vi")))
	in := bufio.NewReader(os.Stdin)
	for {
		cmd := exec.CommandContext(ctx, editor[0], append(editor[1:], path)...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%s: %w", editor[0], err)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if string(raw) == v.Doc {
			fmt.Fprintln(out, "No change.")
			return nil
		}
		err = saveSelf(ctx, st, key, cfgPath, memory.SelfEdited, map[string]string{section: string(raw)})
		if err == nil {
			fmt.Fprintf(out, "Saved %s. It applies when they next start.\n", section)
			return nil
		}
		fmt.Fprintf(out, "That isn't saved: %v\nEdit again? [Y/n] ", err)
		if answer, _ := in.ReadString('\n'); strings.HasPrefix(strings.ToLower(strings.TrimSpace(answer)), "n") {
			return errors.New("nothing saved")
		}
	}
}

// importFrom saves a section's file, or a folder laid out like each life
// in lives/ (with any skills/), as new versions.
func importFrom(ctx context.Context, st *store.Store, key secret.Key, cfgPath, from string, out io.Writer) error {
	info, err := os.Stat(from)
	if err != nil {
		return err
	}
	docs := map[string]string{}
	var skills []agentskill.Skill
	if info.IsDir() {
		for s, f := range person.Files {
			if raw, err := os.ReadFile(filepath.Join(from, f)); err == nil {
				docs[s] = string(raw)
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		var skipped []string
		if skills, skipped = person.SkillsIn(filepath.Join(from, "skills")); len(skipped) > 0 {
			return fmt.Errorf("not skills: %s", strings.Join(skipped, "; "))
		}
	} else {
		for s, f := range person.Files {
			if filepath.Base(from) == f {
				raw, err := os.ReadFile(from)
				if err != nil {
					return err
				}
				docs[s] = string(raw)
			}
		}
		if len(docs) == 0 {
			return fmt.Errorf("%s isn't life.yaml, living.yaml or judgment.yaml", from)
		}
	}
	if len(docs) > 0 {
		if err := saveSelf(ctx, st, key, cfgPath, memory.SelfImported, docs); err != nil {
			return err
		}
	}
	for _, s := range skills {
		if err := st.SaveAgentSkill(ctx, memory.AgentSkill{Name: s.Name, Description: s.Description, Files: s.Files, AddedAt: time.Now().UTC()}); err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "Imported sections: %d; skills: %d. They apply when they next start.\n", len(docs), len(skills))
	return nil
}

// exportTo writes everything but their secrets into a new folder, laid out
// so `minime self import` takes it back.
func exportTo(ctx context.Context, st *store.Store, dir string, out io.Writer) error {
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		return fmt.Errorf("%s isn't empty", dir)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for _, s := range memory.Sections {
		v, ok, err := st.Self(ctx, s)
		if err != nil {
			return err
		}
		if ok {
			if err := os.WriteFile(filepath.Join(dir, person.Files[s]), []byte(v.Doc), 0o600); err != nil {
				return err
			}
		}
	}
	skills, err := st.AgentSkills(ctx)
	if err != nil {
		return err
	}
	for _, sk := range skills {
		for p, content := range sk.Files {
			path := filepath.Join(dir, "skills", sk.Name, filepath.FromSlash(p))
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return err
			}
			if err := os.WriteFile(path, content, 0o600); err != nil {
				return err
			}
		}
	}
	fmt.Fprintf(out, "Wrote their life, how they live, your judgment and their skills (%d) to %s. Their secrets stay in their database.\n", len(skills), dir)
	return nil
}

// secretCommand is `minime secret`. Values are only ever written: list shows
// enough to tell which one is set, never enough to use it.
func secretCommand(ctx context.Context, st *store.Store, key secret.Key, args []string, out io.Writer) error {
	const usage = "want: secret list, set <NAME>, unset <NAME> or rekey"
	if len(args) == 0 {
		return errors.New(usage)
	}
	switch {
	case args[0] == "list" && len(args) == 1:
		sealed, err := st.Secrets(ctx)
		if err != nil {
			return err
		}
		names := slices.Clone(person.Known)
		for name := range sealed {
			if !slices.Contains(names, name) {
				names = append(names, name)
			}
		}
		slices.Sort(names)
		w := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		for _, name := range names {
			shown := "not set"
			if v, ok := sealed[name]; ok {
				value, err := key.Open(name, v)
				if err != nil {
					return err
				}
				shown = secret.Mask(value)
			}
			if _, set := os.LookupEnv(name); set {
				shown += " (the environment sets it too, and wins)"
			}
			fmt.Fprintf(w, "%s\t%s\n", name, shown)
		}
		return w.Flush()
	case args[0] == "set" && len(args) == 2:
		name := args[1]
		if err := person.CheckName(name); err != nil {
			return err
		}
		value, err := readSecret(name)
		if err != nil {
			return err
		}
		if err := st.SetSecrets(ctx, map[string][]byte{name: key.Seal(name, value)}, time.Now().UTC()); err != nil {
			return err
		}
		fmt.Fprintf(out, "%s is set. It applies when they next start.\n", name)
		if _, set := os.LookupEnv(name); set {
			fmt.Fprintf(out, "The environment (or .env) sets %s too, and that wins: remove it there to use this one.\n", name)
		}
		return nil
	case args[0] == "unset" && len(args) == 2:
		ok, err := st.UnsetSecret(ctx, args[1])
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%s isn't set", args[1])
		}
		fmt.Fprintf(out, "%s is gone.\n", args[1])
		return nil
	case args[0] == "rekey" && len(args) == 1:
		return rekey(ctx, st, key, out)
	}
	return errors.New(usage)
}

// readSecret asks without echoing it on a terminal, or reads it whole from
// a pipe.
func readSecret(name string) (string, error) {
	if info, err := os.Stdin.Stat(); err == nil && info.Mode()&os.ModeCharDevice != 0 {
		return setup.Terminal{}.Input(name, "", true)
	}
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, 64<<10))
	if err != nil {
		return "", err
	}
	v := string(bytes.TrimSpace(raw))
	if v == "" {
		return "", errors.New("no value on stdin")
	}
	return v, nil
}

// rekey seals every secret again with a new key. The new key goes in .env
// if their key is there; otherwise it's shown once, to put where they run.
func rekey(ctx context.Context, st *store.Store, key secret.Key, out io.Writer) error {
	sealed, err := st.Secrets(ctx)
	if err != nil {
		return err
	}
	k := secret.NewKey()
	next, _ := secret.ParseKey(k)
	for name, v := range sealed {
		value, err := key.Open(name, v)
		if err != nil {
			return err
		}
		sealed[name] = next.Seal(name, value)
	}
	if err := st.SetSecrets(ctx, sealed, time.Now().UTC()); err != nil {
		return err
	}
	if env, err := godotenv.Read(".env"); err == nil && env[secret.EnvKey] != "" {
		raw, err := os.ReadFile(".env")
		if err == nil {
			err = os.WriteFile(".env", []byte(setup.SetEnv(string(raw), secret.EnvKey, k)), 0o600)
		}
		if err == nil {
			fmt.Fprintf(out, "Their %d secrets are sealed with a new key, now in .env. The old key opens nothing.\n", len(sealed))
			return nil
		}
		fmt.Fprintf(out, "Writing .env failed (%v).\n", err)
	}
	fmt.Fprintf(out, "Their %d secrets are sealed with a new key. Set it where they run, in place of the old one, which opens nothing now:\n%s=%s\n",
		len(sealed), secret.EnvKey, k)
	return nil
}

// skillCommand is `minime skill`: the know-how they've been given.
func skillCommand(ctx context.Context, st *store.Store, args []string, out io.Writer) error {
	const usage = "want: skill list, add <folder> or remove <name>"
	if len(args) == 0 {
		return errors.New(usage)
	}
	switch {
	case args[0] == "list" && len(args) == 1:
		skills, err := st.AgentSkills(ctx)
		if err != nil {
			return err
		}
		if len(skills) == 0 {
			fmt.Fprintln(out, "No skills yet: minime skill add <folder> gives them one.")
		}
		for _, s := range skills {
			fmt.Fprintf(out, "%s (%d files): %s\n", s.Name, len(s.Files), s.Description)
		}
		return nil
	case args[0] == "add" && len(args) == 2:
		s, err := agentskill.FromDir(args[1])
		if err != nil {
			return fmt.Errorf("%s: %w", args[1], err)
		}
		if err := st.SaveAgentSkill(ctx, memory.AgentSkill{Name: s.Name, Description: s.Description, Files: s.Files, AddedAt: time.Now().UTC()}); err != nil {
			return err
		}
		fmt.Fprintf(out, "They have %s now, with %d files (stored, never run). It applies when they next start.\n", s.Name, len(s.Files))
		return nil
	case args[0] == "remove" && len(args) == 2:
		ok, err := st.RemoveAgentSkill(ctx, args[1])
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("they have no skill %q", args[1])
		}
		fmt.Fprintf(out, "%s is gone.\n", args[1])
		return nil
	}
	return errors.New(usage)
}
