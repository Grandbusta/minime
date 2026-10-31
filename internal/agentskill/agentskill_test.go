package agentskill

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoad(t *testing.T) {
	shared, own := t.TempDir(), t.TempDir()
	write(t, filepath.Join(shared, "cold-email", "SKILL.md"), "---\nname: cold-email\ndescription: Short first emails.\nlicense: MIT\nallowed-tools: [Read]\n---\n# Cold email\nLead with one real reason.\n")
	write(t, filepath.Join(shared, "cold-email", "examples.md"), "Hi Ada,")
	write(t, filepath.Join(shared, "cold-email", "scripts", "send.py"), "print('never run')")
	write(t, filepath.Join(shared, "bad-name", "SKILL.md"), "---\nname: Bad Name\ndescription: x\n---\nbody")
	write(t, filepath.Join(shared, "no-front", "SKILL.md"), "# just markdown")
	write(t, filepath.Join(shared, "no-description", "SKILL.md"), "---\nname: no-description\n---\nbody")
	write(t, filepath.Join(shared, "not-a-skill", "notes.md"), "x")
	write(t, filepath.Join(own, "cold-email", "SKILL.md"), "---\nname: cold-email\ndescription: My own way of writing first emails.\n---\nMine.")

	got := Load([]string{shared, filepath.Join(shared, "missing"), own}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if len(got) != 1 || got[0].Description != "My own way of writing first emails." || got[0].Body != "Mine." {
		t.Fatalf("skills = %+v", got)
	}
	s := Load([]string{shared}, slog.New(slog.NewTextHandler(io.Discard, nil)))[0]
	if s.Body != "# Cold email\nLead with one real reason." || strings.Join(s.Files, ",") != "examples.md,scripts/send.py" {
		t.Errorf("skill = %+v", s)
	}
	if text, err := s.Read("./examples.md"); err != nil || text != "Hi Ada," {
		t.Errorf("read = %q, %v", text, err)
	}
	for _, bad := range []string{"../no-front/SKILL.md", "SKILL.md", "/etc/passwd", "missing.md"} {
		if _, err := s.Read(bad); err == nil {
			t.Errorf("read %s", bad)
		}
	}
}
