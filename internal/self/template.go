package self

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Template is a folder laid out like a person's own, with {owner},
// {relation}, {account} and {circle} filled in for whoever starts it.
type Template struct {
	Key       string   `yaml:"-"` // its directory's name
	Dir       string   `yaml:"-"`
	Name      string   `yaml:"name"`
	About     string   `yaml:"about"`
	Relations []string `yaml:"relations"`
}

var Files = []string{"life.yaml", "living.yaml", "judgment.yaml"}

type Owner struct {
	Name     string
	Relation string // one of the template's Relations
	Account  string
}

func Templates(dir string) ([]Template, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read templates: %w", err)
	}
	var out []Template
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		t, err := LoadTemplate(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

func LoadTemplate(dir string) (Template, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "template.yaml"))
	if err != nil {
		return Template{}, fmt.Errorf("read template: %w", err)
	}
	var t Template
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&t); err != nil {
		return Template{}, fmt.Errorf("%s/template.yaml: %w", dir, err)
	}
	t.Key, t.Dir, t.Name, t.About = filepath.Base(dir), dir, strings.TrimSpace(t.Name), strings.TrimSpace(t.About)
	t.Relations = nonEmpty(t.Relations)
	if t.Name == "" || t.About == "" || len(t.Relations) == 0 {
		return Template{}, fmt.Errorf("%s/template.yaml: needs a name, about and at least one relation", dir)
	}
	for _, f := range Files {
		if _, err := os.Stat(filepath.Join(dir, f)); err != nil {
			return Template{}, fmt.Errorf("%s: %s is missing", dir, f)
		}
	}
	return t, nil
}

// Render returns the folder's files by name, with the owner and the circle
// their relation is from filled in.
func (t Template) Render(o Owner) (map[string][]byte, error) {
	o.Name, o.Relation, o.Account = strings.TrimSpace(o.Name), strings.TrimSpace(o.Relation), strings.TrimSpace(o.Account)
	switch {
	case o.Name == "" || strings.ContainsAny(o.Name, "\"\\\n{}"):
		return nil, errors.New("your name can't be empty or have quotes, backslashes or braces in it")
	case !slices.Contains(t.Relations, o.Relation):
		return nil, fmt.Errorf("to %s you can be: %s", t.Name, strings.Join(t.Relations, ", "))
	case strings.ContainsAny(o.Account, "\"\\\n{} "):
		return nil, fmt.Errorf("account %q isn't one", o.Account)
	}
	fill := strings.NewReplacer("{owner}", o.Name, "{relation}", o.Relation, "{account}", o.Account, "{circle}", CircleOf(o.Relation))
	out := map[string][]byte{}
	for _, f := range Files {
		raw, err := os.ReadFile(filepath.Join(t.Dir, f))
		if err != nil {
			return nil, fmt.Errorf("read template: %w", err)
		}
		out[f] = []byte(fill.Replace(string(raw)))
	}
	return out, nil
}
