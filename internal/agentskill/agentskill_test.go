package agentskill

import (
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

func TestFromDir(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "cold-email", "SKILL.md"), "---\nname: cold-email\ndescription: Short first emails.\nlicense: MIT\nallowed-tools: [Read]\n---\n# Cold email\nLead with one real reason.\n")
	write(t, filepath.Join(dir, "cold-email", "examples.md"), "Hi Ada,")
	write(t, filepath.Join(dir, "cold-email", "scripts", "send.py"), "print('never run')")
	write(t, filepath.Join(dir, "bad-name", "SKILL.md"), "---\nname: Bad Name\ndescription: x\n---\nbody")
	write(t, filepath.Join(dir, "no-front", "SKILL.md"), "# just markdown")
	write(t, filepath.Join(dir, "no-description", "SKILL.md"), "---\nname: no-description\n---\nbody")
	write(t, filepath.Join(dir, "elsewhere", "SKILL.md"), "---\nname: cold-email\ndescription: x\n---\nbody")
	write(t, filepath.Join(dir, "not-a-skill", "notes.md"), "x")
	os.Symlink("/etc/passwd", filepath.Join(dir, "cold-email", "passwd"))

	s, err := FromDir(filepath.Join(dir, "cold-email"))
	if err != nil {
		t.Fatal(err)
	}
	if s.Body != "# Cold email\nLead with one real reason." || strings.Join(s.Others(), ",") != "examples.md,scripts/send.py" {
		t.Errorf("skill = %+v, files %v", s, s.Others())
	}
	if text, err := s.Read("./examples.md"); err != nil || text != "Hi Ada," {
		t.Errorf("read = %q, %v", text, err)
	}
	for _, bad := range []string{"../no-front/SKILL.md", "SKILL.md", "/etc/passwd", "passwd", "missing.md"} {
		if _, err := s.Read(bad); err == nil {
			t.Errorf("read %s", bad)
		}
	}
	for _, bad := range []string{"bad-name", "no-front", "no-description", "elsewhere", "not-a-skill"} {
		if _, err := FromDir(filepath.Join(dir, bad)); err == nil {
			t.Errorf("%s: a skill", bad)
		}
	}
}
