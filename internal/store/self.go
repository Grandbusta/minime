package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/oklog/ulid/v2"

	"github.com/Grandbusta/minime/internal/memory"
)

// Start makes the database someone's: their first versions, secrets and
// skills, all or nothing. It returns false, changing nothing, if a life is
// already here; ErrNotTheirs if the database was claimed by someone else.
func (s *Store) Start(ctx context.Context, name string, vs []memory.SelfVersion, secrets map[string][]byte, skills []memory.AgentSkill,
	at time.Time) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("start: %w", err)
	}
	defer tx.Rollback(ctx)
	// Two starting at once: the second waits here, then finds the life.
	if _, err := tx.Exec(ctx, `LOCK TABLE self_versions IN EXCLUSIVE MODE`); err != nil {
		return false, fmt.Errorf("start: %w", err)
	}
	var lived bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM self_versions WHERE section = $1)`, memory.SectionLife).Scan(&lived); err != nil {
		return false, fmt.Errorf("start: %w", err)
	}
	if lived {
		return false, nil
	}
	var whose string
	switch err := tx.QueryRow(ctx, `SELECT name FROM whose`).Scan(&whose); {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return false, fmt.Errorf("start: %w", err)
	case whose != name:
		return false, fmt.Errorf("%w: %s's, not %s's; give %s their own database", ErrNotTheirs, whose, name, name)
	}
	if err := saveSelf(ctx, tx, name, vs); err != nil {
		return false, err
	}
	if err := setSecrets(ctx, tx, secrets, at); err != nil {
		return false, err
	}
	for _, sk := range skills {
		if err := saveSkill(ctx, tx, sk); err != nil {
			return false, err
		}
	}
	return true, tx.Commit(ctx)
}

// Self is the newest version of section; false if there is none.
func (s *Store) Self(ctx context.Context, section string) (memory.SelfVersion, bool, error) {
	vs, err := s.SelfHistory(ctx, section, 1)
	if err != nil || len(vs) == 0 {
		return memory.SelfVersion{}, false, err
	}
	return vs[0], true, nil
}

// SelfHistory is section's versions, newest first.
func (s *Store) SelfHistory(ctx context.Context, section string, limit int) ([]memory.SelfVersion, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, section, doc, source, at FROM self_versions WHERE section = $1
		ORDER BY at DESC, id DESC LIMIT $2`, section, limit)
	if err != nil {
		return nil, fmt.Errorf("query self: %w", err)
	}
	defer rows.Close()
	var out []memory.SelfVersion
	for rows.Next() {
		var v memory.SelfVersion
		var id string
		if err := rows.Scan(&id, &v.Section, &v.Doc, &v.Source, &v.At); err != nil {
			return nil, err
		}
		if v.ID, err = ulid.Parse(id); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// SaveSelf adds new versions. name is the person's name in a life among
// them ("" if none): whose memory this is follows it.
func (s *Store) SaveSelf(ctx context.Context, name string, vs ...memory.SelfVersion) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("save self: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := saveSelf(ctx, tx, name, vs); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func saveSelf(ctx context.Context, tx pgx.Tx, name string, vs []memory.SelfVersion) error {
	for _, v := range vs {
		if _, err := tx.Exec(ctx, `INSERT INTO self_versions (id, section, doc, source, at) VALUES ($1, $2, $3, $4, $5)`,
			v.ID.String(), v.Section, v.Doc, v.Source, v.At); err != nil {
			return fmt.Errorf("save self: %w", err)
		}
	}
	if name == "" || len(vs) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `INSERT INTO whose (name, since) VALUES ($1, $2) ON CONFLICT (one) DO UPDATE SET name = EXCLUDED.name`,
		name, vs[0].At); err != nil {
		return fmt.Errorf("save self: %w", err)
	}
	return nil
}

// Secrets are sealed values by name.
func (s *Store) Secrets(ctx context.Context) (map[string][]byte, error) {
	rows, err := s.pool.Query(ctx, `SELECT name, sealed FROM secrets`)
	if err != nil {
		return nil, fmt.Errorf("query secrets: %w", err)
	}
	defer rows.Close()
	out := map[string][]byte{}
	for rows.Next() {
		var name string
		var sealed []byte
		if err := rows.Scan(&name, &sealed); err != nil {
			return nil, err
		}
		out[name] = sealed
	}
	return out, rows.Err()
}

// SetSecrets sets them all or none, each replacing what was there.
func (s *Store) SetSecrets(ctx context.Context, sealed map[string][]byte, at time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("set secrets: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := setSecrets(ctx, tx, sealed, at); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func setSecrets(ctx context.Context, tx pgx.Tx, sealed map[string][]byte, at time.Time) error {
	for name, v := range sealed {
		if _, err := tx.Exec(ctx, `INSERT INTO secrets (name, sealed, set_at) VALUES ($1, $2, $3)
			ON CONFLICT (name) DO UPDATE SET sealed = EXCLUDED.sealed, set_at = EXCLUDED.set_at`, name, v, at); err != nil {
			return fmt.Errorf("set secret %s: %w", name, err)
		}
	}
	return nil
}

func (s *Store) UnsetSecret(ctx context.Context, name string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM secrets WHERE name = $1`, name)
	if err != nil {
		return false, fmt.Errorf("unset secret: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// AgentSkills are the skills it has, with their files, by name.
func (s *Store) AgentSkills(ctx context.Context) ([]memory.AgentSkill, error) {
	rows, err := s.pool.Query(ctx, `SELECT k.name, k.description, k.added_at, f.path, f.content
		FROM agent_skills k JOIN agent_skill_files f ON f.skill = k.name ORDER BY k.name, f.path`)
	if err != nil {
		return nil, fmt.Errorf("query skills: %w", err)
	}
	defer rows.Close()
	var out []memory.AgentSkill
	for rows.Next() {
		var sk memory.AgentSkill
		var path string
		var content []byte
		if err := rows.Scan(&sk.Name, &sk.Description, &sk.AddedAt, &path, &content); err != nil {
			return nil, err
		}
		if len(out) == 0 || out[len(out)-1].Name != sk.Name {
			sk.Files = map[string][]byte{}
			out = append(out, sk)
		}
		out[len(out)-1].Files[path] = content
	}
	return out, rows.Err()
}

// SaveAgentSkill replaces any skill of the same name, files and all.
func (s *Store) SaveAgentSkill(ctx context.Context, sk memory.AgentSkill) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("save skill: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := saveSkill(ctx, tx, sk); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func saveSkill(ctx context.Context, tx pgx.Tx, sk memory.AgentSkill) error {
	if _, err := tx.Exec(ctx, `DELETE FROM agent_skills WHERE name = $1`, sk.Name); err != nil {
		return fmt.Errorf("save skill: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO agent_skills (name, description, added_at) VALUES ($1, $2, $3)`,
		sk.Name, sk.Description, sk.AddedAt); err != nil {
		return fmt.Errorf("save skill: %w", err)
	}
	for path, content := range sk.Files {
		if _, err := tx.Exec(ctx, `INSERT INTO agent_skill_files (skill, path, content) VALUES ($1, $2, $3)`,
			sk.Name, path, content); err != nil {
			return fmt.Errorf("save skill file %s: %w", path, err)
		}
	}
	return nil
}

func (s *Store) RemoveAgentSkill(ctx context.Context, name string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM agent_skills WHERE name = $1`, name)
	if err != nil {
		return false, fmt.Errorf("remove skill: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}
