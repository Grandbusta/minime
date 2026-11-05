package person

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Grandbusta/minime/internal/memory"
	"github.com/Grandbusta/minime/internal/secret"
)

type fakeStore struct {
	versions []memory.SelfVersion
	secrets  map[string][]byte
	skills   []memory.AgentSkill
}

func (f *fakeStore) Start(_ context.Context, _ string, vs []memory.SelfVersion, secrets map[string][]byte, skills []memory.AgentSkill,
	_ time.Time) (bool, error) {
	if _, ok, _ := f.Self(context.Background(), memory.SectionLife); ok {
		return false, nil
	}
	f.versions, f.secrets, f.skills = append(f.versions, vs...), secrets, skills
	return true, nil
}

func (f *fakeStore) Self(_ context.Context, section string) (memory.SelfVersion, bool, error) {
	for i := len(f.versions) - 1; i >= 0; i-- {
		if f.versions[i].Section == section {
			return f.versions[i], true, nil
		}
	}
	return memory.SelfVersion{}, false, nil
}

func (f *fakeStore) Secrets(context.Context) (map[string][]byte, error) { return f.secrets, nil }

func (f *fakeStore) AgentSkills(context.Context) ([]memory.AgentSkill, error) { return f.skills, nil }

func write(t *testing.T, path, text string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestTheirFolderMovesIntoTheirDatabaseOnce(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	dir := filepath.Join(root, "self")
	write(t, filepath.Join(dir, "life.yaml"), "name: Tobi\nstory: [Tobi is 29.]\ntimezone: Africa/Lagos\n")
	write(t, filepath.Join(dir, "living.yaml"), "tools:\n  servers:\n    - name: drive\n      url: https://x\n      auth_env: DRIVE_TOKEN\n      tools: {search: read}\n")
	write(t, filepath.Join(dir, "skills", "cold-email", "SKILL.md"), "---\nname: cold-email\ndescription: Tobi's own way.\n---\nOwn.")
	write(t, filepath.Join(dir, "skills", "cold-email", "scripts", "send.py"), "print('never run')")
	t.Setenv("OPENAI_API_KEY", "sk-tobi-0123456789")
	t.Setenv("DRIVE_TOKEN", "drive-secret")
	t.Setenv("UNRELATED", "not theirs")

	key, _ := secret.ParseKey(secret.NewKey())
	st := &fakeStore{}
	im, err := Import(ctx, st, key, dir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if im.Name != "Tobi" || strings.Join(im.Secrets, ",") != "DRIVE_TOKEN,OPENAI_API_KEY" || strings.Join(im.Skills, ",") != "cold-email" {
		t.Fatalf("imported %+v", im)
	}
	for name, v := range st.secrets {
		if strings.Contains(string(v), "sk-tobi") || strings.Contains(string(v), "drive-secret") {
			t.Errorf("%s is stored readable", name)
		}
	}

	// The folder changes, but the database is who they are now.
	write(t, filepath.Join(dir, "life.yaml"), "name: Tobi\nstory: [Changed in the file.]\n")
	if again, err := Import(ctx, st, key, dir, time.Now()); err != nil || again.Name != "" || len(st.versions) != 2 {
		t.Fatalf("imported twice: %+v, %v", again, err)
	}

	os.Unsetenv("DRIVE_TOKEN")
	p, err := Load(ctx, st, key)
	if err != nil {
		t.Fatal(err)
	}
	if p.Life.Story[0] != "Tobi is 29." || !strings.Contains(string(p.Living), "DRIVE_TOKEN") {
		t.Errorf("life = %+v, living %q", p.Life, p.Living)
	}
	if os.Getenv("DRIVE_TOKEN") != "drive-secret" || !slices.Equal(p.FromEnv, []string{"OPENAI_API_KEY"}) {
		t.Errorf("env: DRIVE_TOKEN %q, set there already %v", os.Getenv("DRIVE_TOKEN"), p.FromEnv)
	}
	if len(p.Skills) != 1 || p.Skills[0].Body != "Own." || strings.Join(p.Skills[0].Others(), ",") != "scripts/send.py" {
		t.Errorf("skills = %+v", p.Skills)
	}

	other, _ := secret.ParseKey(secret.NewKey())
	if _, err := Load(ctx, st, other); !errors.Is(err, secret.ErrWrongKey) {
		t.Errorf("another key: %v", err)
	}
	if _, err := Load(ctx, &fakeStore{}, key); !errors.Is(err, ErrNoOne) {
		t.Errorf("an empty database: %v", err)
	}
}

func TestSecretNames(t *testing.T) {
	for _, name := range []string{"DATABASE_URL", "MINIME_KEY", "PATH", "openai_key", "1KEY", ""} {
		if CheckName(name) == nil {
			t.Errorf("%q allowed", name)
		}
	}
	if err := CheckName("DRIVE_MCP_TOKEN"); err != nil {
		t.Error(err)
	}
}
