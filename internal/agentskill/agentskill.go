// Package agentskill loads know-how in the Agent Skills format: a folder
// with a SKILL.md (YAML frontmatter with a name and a description, then the
// instructions in Markdown) and any files it points to. A skill is trusted
// like a prompt, since its owner installed it; it grants no permissions, and
// scripts in it are never run, only read.
package agentskill

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const (
	maxFiles    = 100
	maxFileSize = 50_000 // bytes of a file it reads
)

type Skill struct {
	Name        string
	Description string
	Body        string
	Dir         string
	Files       []string // its other files, relative to Dir, slash-separated
}

var name = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// Load reads every skill in dirs, in order: a skill named like one before it
// replaces it. A folder that isn't a skill is logged and left out; a missing
// dir is no skills.
func Load(dirs []string, log *slog.Logger) []Skill {
	byName := map[string]Skill{}
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			log.Warn("reading skills failed", "dir", dir, "err", err)
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			s, err := load(filepath.Join(dir, e.Name()))
			if errors.Is(err, fs.ErrNotExist) {
				continue // a folder without a SKILL.md
			}
			if err != nil {
				log.Warn("skill left out", "dir", filepath.Join(dir, e.Name()), "err", err)
				continue
			}
			byName[s.Name] = s
		}
	}
	out := make([]Skill, 0, len(byName))
	for _, s := range byName {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func load(dir string) (Skill, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		return Skill{}, err
	}
	front, body, ok := frontmatter(raw)
	if !ok {
		return Skill{}, errors.New("SKILL.md has no frontmatter (--- name, description ---)")
	}
	var meta struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if err := yaml.Unmarshal(front, &meta); err != nil {
		return Skill{}, fmt.Errorf("SKILL.md frontmatter: %w", err)
	}
	meta.Name, meta.Description = strings.TrimSpace(meta.Name), strings.TrimSpace(meta.Description)
	switch {
	case !name.MatchString(meta.Name) || len(meta.Name) > 64:
		return Skill{}, fmt.Errorf("name %q must be lowercase words joined by hyphens, at most 64 characters", meta.Name)
	case meta.Name != filepath.Base(dir):
		return Skill{}, fmt.Errorf("name %q must match its folder, %q", meta.Name, filepath.Base(dir))
	case meta.Description == "" || utf8.RuneCountInString(meta.Description) > 1024:
		return Skill{}, errors.New("description is required, at most 1024 characters")
	}
	s := Skill{Name: meta.Name, Description: meta.Description, Body: strings.TrimSpace(string(body)), Dir: dir}
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || len(s.Files) == maxFiles {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 { // never a way out of the folder
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		if rel = filepath.ToSlash(rel); rel != "SKILL.md" {
			s.Files = append(s.Files, rel)
		}
		return nil
	})
	return s, err
}

// frontmatter splits a file that starts with a --- line into what's
// between it and the next --- line, and the rest.
func frontmatter(raw []byte) (front, body []byte, ok bool) {
	raw = bytes.TrimPrefix(raw, []byte("\uFEFF"))
	rest, ok := bytes.CutPrefix(raw, []byte("---\n"))
	if !ok {
		rest, ok = bytes.CutPrefix(raw, []byte("---\r\n"))
	}
	if !ok {
		return nil, nil, false
	}
	front, body, ok = bytes.Cut(rest, []byte("\n---"))
	if !ok {
		return nil, nil, false
	}
	_, body, _ = bytes.Cut(body, []byte("\n"))
	return front, body, true
}

// Read is one of its files: only those listed, so nothing outside it.
func (s Skill) Read(file string) (string, error) {
	file = path.Clean(strings.TrimPrefix(strings.TrimSpace(file), "./"))
	if !slices.Contains(s.Files, file) {
		return "", fmt.Errorf("%s has no file %q (it has %s)", s.Name, file, strings.Join(s.Files, ", "))
	}
	raw, err := os.ReadFile(filepath.Join(s.Dir, filepath.FromSlash(file)))
	if err != nil {
		return "", err
	}
	if len(raw) > maxFileSize {
		raw = raw[:maxFileSize]
	}
	return string(raw), nil
}
