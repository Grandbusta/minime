package store

import (
	"context"
	"testing"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/Grandbusta/minime/internal/memory"
)

func TestAPersonInTheirDatabase(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond)
	life := memory.SelfVersion{ID: ulid.Make(), Section: memory.SectionLife, Doc: "name: Tobi\n", Source: memory.SelfNew, At: at}
	living := memory.SelfVersion{ID: ulid.Make(), Section: memory.SectionLiving, Doc: "# mine\n", Source: memory.SelfNew, At: at}
	skill := memory.AgentSkill{Name: "cold-email", Description: "First emails.", AddedAt: at,
		Files: map[string][]byte{"SKILL.md": []byte("---\n"), "scripts/send.py": {0xff, 0x00}}}
	if ok, err := s.Start(ctx, "Tobi", []memory.SelfVersion{life, living}, map[string][]byte{"OPENAI_API_KEY": {1, 2}}, []memory.AgentSkill{skill}, at); err != nil || !ok {
		t.Fatalf("start = %v, %v", ok, err)
	}
	if ok, err := s.Start(ctx, "Mei", []memory.SelfVersion{life}, nil, nil, at); err != nil || ok {
		t.Fatalf("started twice: %v, %v", ok, err)
	}
	if whose, _, _ := s.Whose(ctx); whose != "Tobi" {
		t.Errorf("whose = %q", whose)
	}

	edited := memory.SelfVersion{ID: ulid.Make(), Section: memory.SectionLife, Doc: "name: Tobi Ade\n", Source: memory.SelfEdited, At: at.Add(time.Second)}
	if err := s.SaveSelf(ctx, "Tobi Ade", edited); err != nil {
		t.Fatal(err)
	}
	if v, ok, err := s.Self(ctx, memory.SectionLife); err != nil || !ok || v.Doc != edited.Doc || v.Source != memory.SelfEdited {
		t.Errorf("newest life = %+v, %v", v, err)
	}
	if vs, _ := s.SelfHistory(ctx, memory.SectionLife, 10); len(vs) != 2 || vs[1].ID != life.ID {
		t.Errorf("history = %+v", vs)
	}
	if whose, _, _ := s.Whose(ctx); whose != "Tobi Ade" {
		t.Errorf("whose after a rename = %q", whose)
	}
	if _, ok, _ := s.Self(ctx, memory.SectionJudgment); ok {
		t.Error("a judgment no one saved")
	}

	if err := s.SetSecrets(ctx, map[string][]byte{"OPENAI_API_KEY": {3}, "DRIVE_TOKEN": {4}}, at); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.UnsetSecret(ctx, "DRIVE_TOKEN"); !ok {
		t.Error("unset found nothing")
	}
	if got, _ := s.Secrets(ctx); len(got) != 1 || got["OPENAI_API_KEY"][0] != 3 {
		t.Errorf("secrets = %v", got)
	}

	skill.Files = map[string][]byte{"SKILL.md": []byte("---\nnew")}
	if err := s.SaveAgentSkill(ctx, skill); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.AgentSkills(ctx); len(got) != 1 || len(got[0].Files) != 1 || string(got[0].Files["SKILL.md"]) != "---\nnew" {
		t.Errorf("skills after replacing = %+v", got)
	}
	if ok, _ := s.RemoveAgentSkill(ctx, "cold-email"); !ok {
		t.Error("remove found nothing")
	}
	if got, _ := s.AgentSkills(ctx); len(got) != 0 {
		t.Errorf("skills after removing = %+v", got)
	}
}
