package store

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/Grandbusta/minime/internal/memory"
)

// newDatabase is a new, empty database beside the test one, dropped after.
func newDatabase(t *testing.T, test *Store) string {
	t.Helper()
	name := "minime_" + strings.ToLower(ulid.Make().String())
	if _, err := test.pool.Exec(context.Background(), "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { test.pool.Exec(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)") })
	u, _ := url.Parse(test.pool.Config().ConnString())
	u.Path = "/" + name
	return u.String()
}

func TestABackupRestoresIntoANewDatabase(t *testing.T) {
	test := openTestStore(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond)
	s, err := Open(ctx, newDatabase(t, test))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	life := memory.SelfVersion{ID: ulid.Make(), Section: memory.SectionLife, Doc: "name: Tobi\n", Source: memory.SelfNew, At: at}
	skill := memory.AgentSkill{Name: "cold-email", AddedAt: at, Files: map[string][]byte{"SKILL.md": []byte("---\n"), "logo.png": {0x89, 0x00, '\n', ','}}}
	if _, err := s.Start(ctx, "Tobi", []memory.SelfVersion{life}, map[string][]byte{"OPENAI_API_KEY": {0, '"', '\n'}}, []memory.AgentSkill{skill}, at); err != nil {
		t.Fatal(err)
	}
	if err := s.LogWeb(ctx, "search", "a \"quoted\", line\nbreak", 0.01, at); err != nil {
		t.Fatal(err)
	}
	var file bytes.Buffer
	made, err := s.Backup(ctx, &file, at)
	if err != nil {
		t.Fatal(err)
	}

	into := newDatabase(t, test)
	restored, err := Restore(ctx, into, bytes.NewReader(file.Bytes()), int64(file.Len()))
	if err != nil || restored.Whose != made.Whose {
		t.Fatalf("restore = %+v, %v", restored, err)
	}
	r, err := Open(ctx, into)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	again, err := r.Backup(ctx, &bytes.Buffer{}, at)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again.Tables, made.Tables) {
		t.Errorf("restored tables differ:\n%+v\nwant\n%+v", again.Tables, made.Tables)
	}
	for _, read := range []func(*Store) (any, error){
		func(s *Store) (any, error) { return s.Secrets(ctx) },
		func(s *Store) (any, error) { return s.AgentSkills(ctx) },
		func(s *Store) (any, error) { return s.SelfHistory(ctx, memory.SectionLife, 100) },
	} {
		want, _ := read(s)
		if got, err := read(r); err != nil || fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("restored %v (%v), want %v", got, err, want)
		}
	}
	if err := r.LogWeb(ctx, "page", "after", 0, at); err != nil {
		t.Errorf("a new row after the restore: %v", err)
	}
	if _, err := Restore(ctx, into, bytes.NewReader(file.Bytes()), int64(file.Len())); err == nil {
		t.Error("restored over someone")
	}
}
