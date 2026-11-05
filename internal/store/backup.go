package store

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// A backup is a zip: backup.json, then each table as CSV, in an order that
// loads without breaking a foreign key. Secrets are in it as stored, sealed.
const backupFormat = 1

type Backup struct {
	Format  int           `json:"format"`
	Whose   string        `json:"whose"`
	Version int64         `json:"version"` // the newest migration it was made at
	At      time.Time     `json:"at"`
	Tables  []BackupTable `json:"tables"`
}

type BackupTable struct {
	Name    string   `json:"name"`
	Columns []string `json:"columns"`
	Rows    int64    `json:"rows"`
}

func (b Backup) Rows() int64 {
	var n int64
	for _, t := range b.Tables {
		n += t.Rows
	}
	return n
}

// Backup writes everything as it is at one moment, so the agent can keep
// running while it's made.
func (s *Store) Backup(ctx context.Context, w io.Writer, at time.Time) (Backup, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return Backup{}, fmt.Errorf("backup: %w", err)
	}
	defer tx.Rollback(ctx)
	b := Backup{Format: backupFormat, At: at}
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(version_id), 0) FROM goose_db_version WHERE is_applied`).Scan(&b.Version); err != nil {
		return Backup{}, fmt.Errorf("backup: %w", err)
	}
	switch err := tx.QueryRow(ctx, `SELECT name FROM whose`).Scan(&b.Whose); {
	case errors.Is(err, pgx.ErrNoRows):
		return Backup{}, errors.New("backup: no one lives in this database yet")
	case err != nil:
		return Backup{}, fmt.Errorf("backup: %w", err)
	}
	if b.Tables, err = tables(ctx, tx); err != nil {
		return Backup{}, err
	}
	z := zip.NewWriter(w)
	for i, t := range b.Tables {
		f, err := z.Create(t.Name + ".csv")
		if err != nil {
			return Backup{}, err
		}
		tag, err := tx.Conn().PgConn().CopyTo(ctx, f, fmt.Sprintf("COPY %s TO STDOUT WITH (FORMAT csv)", t.sql()))
		if err != nil {
			return Backup{}, fmt.Errorf("backup %s: %w", t.Name, err)
		}
		b.Tables[i].Rows = tag.RowsAffected()
	}
	f, err := z.Create("backup.json")
	if err != nil {
		return Backup{}, err
	}
	if err := json.NewEncoder(f).Encode(b); err != nil {
		return Backup{}, err
	}
	return b, z.Close()
}

// Restore loads a backup into the database at url, which must have nothing
// in it yet: at the backup's version, then migrated to this minime's.
func Restore(ctx context.Context, url string, r io.ReaderAt, size int64) (Backup, error) {
	z, err := zip.NewReader(r, size)
	if err != nil {
		return Backup{}, fmt.Errorf("not a backup: %w", err)
	}
	var b Backup
	f, err := z.Open("backup.json")
	if err == nil {
		err = json.NewDecoder(f).Decode(&b)
		f.Close()
	}
	if err != nil || b.Format != backupFormat {
		return Backup{}, fmt.Errorf("not a minime backup (%v)", err)
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return Backup{}, fmt.Errorf("connect: %w", err)
	}
	defer pool.Close()
	p, done, err := migrator(pool)
	if err != nil {
		return Backup{}, err
	}
	defer done()
	sources := p.ListSources()
	if newest := sources[len(sources)-1].Version; b.Version > newest {
		return Backup{}, fmt.Errorf("the backup is from a newer minime (migration %d; this one knows %d): restore it with that one", b.Version, newest)
	}
	if _, err := p.UpTo(ctx, b.Version); err != nil {
		return Backup{}, fmt.Errorf("migrate: %w", err)
	}
	if v, err := p.GetDBVersion(ctx); err != nil || v != b.Version {
		return Backup{}, fmt.Errorf("this database is at migration %d, past the backup's %d: restore into a new, empty one (%v)", v, b.Version, err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return Backup{}, err
	}
	defer tx.Rollback(ctx)
	here, err := tables(ctx, tx)
	if err != nil {
		return Backup{}, err
	}
	for _, t := range b.Tables {
		if !slices.ContainsFunc(here, func(h BackupTable) bool { return h.Name == t.Name && slices.Equal(h.Columns, t.Columns) }) {
			return Backup{}, fmt.Errorf("the backup's %s doesn't match this database's, at the same migration", t.Name)
		}
		var used bool
		if err := tx.QueryRow(ctx, fmt.Sprintf("SELECT EXISTS (SELECT 1 FROM %s)", pgx.Identifier{t.Name}.Sanitize())).Scan(&used); err != nil {
			return Backup{}, err
		}
		if used {
			return Backup{}, fmt.Errorf("this database isn't empty (%s has rows): restore into a new one", t.Name)
		}
		f, err := z.Open(t.Name + ".csv")
		if err != nil {
			return Backup{}, fmt.Errorf("the backup is missing %s: %w", t.Name, err)
		}
		tag, err := tx.Conn().PgConn().CopyFrom(ctx, f, fmt.Sprintf("COPY %s FROM STDIN WITH (FORMAT csv)", t.sql()))
		f.Close()
		if err != nil {
			return Backup{}, fmt.Errorf("restore %s: %w", t.Name, err)
		}
		if tag.RowsAffected() != t.Rows {
			return Backup{}, fmt.Errorf("restore %s: %d rows, but the backup made %d", t.Name, tag.RowsAffected(), t.Rows)
		}
	}
	// A serial column's next number follows on from the rows loaded.
	rows, err := tx.Query(ctx, `SELECT s.oid::regclass::text, t.relname, a.attname FROM pg_class s
		JOIN pg_depend d ON d.objid = s.oid AND d.classid = 'pg_class'::regclass AND d.refclassid = 'pg_class'::regclass AND d.deptype IN ('a', 'i')
		JOIN pg_class t ON t.oid = d.refobjid
		JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = d.refobjsubid
		WHERE s.relkind = 'S' AND s.relnamespace = 'public'::regnamespace`)
	if err != nil {
		return Backup{}, err
	}
	var seqs [][3]string
	for rows.Next() {
		var s [3]string
		if err := rows.Scan(&s[0], &s[1], &s[2]); err != nil {
			return Backup{}, err
		}
		seqs = append(seqs, s)
	}
	if err := rows.Err(); err != nil {
		return Backup{}, err
	}
	for _, s := range seqs {
		if _, err := tx.Exec(ctx, fmt.Sprintf("SELECT setval($1, COALESCE((SELECT MAX(%s) FROM %s), 0) + 1, false)",
			pgx.Identifier{s[2]}.Sanitize(), pgx.Identifier{s[1]}.Sanitize()), s[0]); err != nil {
			return Backup{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Backup{}, err
	}
	if _, err := p.Up(ctx); err != nil {
		return Backup{}, fmt.Errorf("migrate: %w", err)
	}
	return b, nil
}

func (t BackupTable) sql() string {
	cols := make([]string, len(t.Columns))
	for i, c := range t.Columns {
		cols[i] = pgx.Identifier{c}.Sanitize()
	}
	return fmt.Sprintf("%s (%s)", pgx.Identifier{t.Name}.Sanitize(), strings.Join(cols, ", "))
}

// tables are minime's, with their columns, each after the tables it refers to.
func tables(ctx context.Context, tx pgx.Tx) ([]BackupTable, error) {
	rows, err := tx.Query(ctx, `SELECT c.relname::text, array_agg(a.attname::text ORDER BY a.attnum),
		COALESCE((SELECT array_agg(DISTINCT p.relname::text) FROM pg_constraint f JOIN pg_class p ON p.oid = f.confrelid
			WHERE f.conrelid = c.oid AND f.contype = 'f' AND f.confrelid <> c.oid), '{}')
		FROM pg_class c JOIN pg_attribute a ON a.attrelid = c.oid
		WHERE c.relnamespace = 'public'::regnamespace AND c.relkind = 'r' AND c.relname <> 'goose_db_version'
			AND a.attnum > 0 AND NOT a.attisdropped AND a.attgenerated = ''
		GROUP BY c.oid, c.relname ORDER BY c.relname`)
	if err != nil {
		return nil, fmt.Errorf("tables: %w", err)
	}
	defer rows.Close()
	var all []BackupTable
	refers := map[string][]string{}
	for rows.Next() {
		var t BackupTable
		var parents []string
		if err := rows.Scan(&t.Name, &t.Columns, &parents); err != nil {
			return nil, err
		}
		all = append(all, t)
		refers[t.Name] = parents
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var out []BackupTable
	placed := map[string]bool{}
	for len(out) < len(all) {
		before := len(out)
		for _, t := range all {
			if !placed[t.Name] && !slices.ContainsFunc(refers[t.Name], func(p string) bool { return !placed[p] }) {
				out, placed[t.Name] = append(out, t), true
			}
		}
		if len(out) == before {
			return nil, errors.New("tables: their foreign keys go round in a circle")
		}
	}
	return out, nil
}
