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
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const (
	maxFiles    = 100
	maxStored   = 2 << 20  // bytes of one file kept
	maxTotal    = 10 << 20 // bytes of a whole skill kept
	maxFileSize = 50_000   // bytes of a file it reads
)

type Skill struct {
	Name        string
	Description string
	Body        string
	// Files are all of it by slash-separated path, SKILL.md included.
	Files map[string][]byte
}

var name = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// FromDir reads a skill's folder, which must be named like the skill.
func FromDir(dir string) (Skill, error) {
	files, total := map[string][]byte{}, 0
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 { // never a way out of the folder
			return nil
		}
		if len(files) == maxFiles {
			return fmt.Errorf("more than %d files", maxFiles)
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if total += len(raw); len(raw) > maxStored || total > maxTotal {
			return fmt.Errorf("too big: at most %d MB a file and %d MB in all", maxStored>>20, maxTotal>>20)
		}
		rel, _ := filepath.Rel(dir, p)
		files[filepath.ToSlash(rel)] = raw
		return nil
	})
	if err != nil {
		return Skill{}, err
	}
	s, err := Parse(files)
	if err != nil {
		return Skill{}, err
	}
	if s.Name != filepath.Base(dir) {
		return Skill{}, fmt.Errorf("name %q must match its folder, %q", s.Name, filepath.Base(dir))
	}
	return s, nil
}

// Parse makes a skill of its files; fs.ErrNotExist if there's no SKILL.md.
func Parse(files map[string][]byte) (Skill, error) {
	raw, ok := files["SKILL.md"]
	if !ok {
		return Skill{}, fmt.Errorf("SKILL.md: %w", fs.ErrNotExist)
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
	case meta.Description == "" || utf8.RuneCountInString(meta.Description) > 1024:
		return Skill{}, errors.New("description is required, at most 1024 characters")
	}
	return Skill{Name: meta.Name, Description: meta.Description, Body: strings.TrimSpace(string(body)), Files: files}, nil
}

// Others are its files besides SKILL.md, sorted.
func (s Skill) Others() []string {
	var out []string
	for f := range s.Files {
		if f != "SKILL.md" {
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out
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

// Read is one of its files besides SKILL.md. One that isn't text (an
// image, a PDF, a font) is only described: its bytes would mean nothing
// read as words.
func (s Skill) Read(file string) (string, error) {
	file = path.Clean(strings.TrimPrefix(strings.TrimSpace(file), "./"))
	raw, ok := s.Files[file]
	if !ok || file == "SKILL.md" {
		return "", fmt.Errorf("%s has no file %q (it has %s)", s.Name, file, strings.Join(s.Others(), ", "))
	}
	if !utf8.Valid(raw) || bytes.IndexByte(raw, 0) >= 0 {
		kind, _, _ := strings.Cut(http.DetectContentType(raw), ";")
		return fmt.Sprintf("%s isn't text (%s, %s): it can't be read as words, only used as it is.", file, kind, size(len(raw))), nil
	}
	if len(raw) > maxFileSize {
		raw = raw[:maxFileSize]
	}
	return strings.ToValidUTF8(string(raw), ""), nil
}

func size(n int) string {
	if n < 1<<10 {
		return fmt.Sprintf("%d bytes", n)
	}
	return fmt.Sprintf("%d KB", (n+1<<10-1)>>10)
}
