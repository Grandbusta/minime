package minime

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFilesOnDiskWinOverTheBuiltIn(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if raw, err := fs.ReadFile(Files, "prompts/reply.md"); err != nil || len(raw) == 0 {
		t.Fatalf("built in: %v", err)
	}
	if sets, err := fs.Glob(Files, "decisions/*.yaml"); err != nil || len(sets) == 0 {
		t.Fatalf("built-in question sets: %v, %v", sets, err)
	}

	os.MkdirAll(filepath.Join(dir, "prompts"), 0o755)
	os.WriteFile(filepath.Join(dir, "prompts", "reply.md"), []byte("mine"), 0o600)
	if raw, _ := fs.ReadFile(Files, "prompts/reply.md"); string(raw) != "mine" {
		t.Errorf("the file on disk lost: %q", raw)
	}
	if raw, err := fs.ReadFile(Files, filepath.Join(dir, "prompts", "reply.md")); err != nil || string(raw) != "mine" {
		t.Errorf("an absolute path = %q, %v", raw, err)
	}
	if raw, _ := fs.ReadFile(Files, "configs/minime.yaml"); !strings.Contains(string(raw), "DATABASE_URL") {
		t.Error("the built-in config is missing")
	}
}
