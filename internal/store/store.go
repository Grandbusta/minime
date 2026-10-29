// Package store persists percepts, decisions and memory in PostgreSQL.
package store

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/oklog/ulid/v2"
	"github.com/pressly/goose/v3"

	"minime/internal/decision"
	"minime/internal/memory"
	"minime/internal/perception"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Store struct {
	pool *pgxpool.Pool
}

// Open connects to Postgres and applies any pending migrations.
func Open(ctx context.Context, url string) (*Store, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	if err := migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{pool: pool}, nil
}

func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	dir, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return err
	}
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	p, err := goose.NewProvider(goose.DialectPostgres, db, dir)
	if err != nil {
		return fmt.Errorf("migrations: %w", err)
	}
	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

func (s *Store) Close() { s.pool.Close() }

// SavePercept stores p and reports whether it was new; a percept already seen
// from the same source (same ExternalID) is not stored again.
func (s *Store) SavePercept(ctx context.Context, p perception.Percept) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO percepts (id, source, external_id, thread, speaker, kind, text, payload, at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (source, external_id) DO NOTHING`,
		p.ID.String(), p.Source, p.ExternalID, p.Thread, p.Speaker, p.Kind, p.Text, nullJSON(p.Payload), p.At)
	if err != nil {
		return false, fmt.Errorf("save percept: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Store) SaveDecision(ctx context.Context, d decision.Decision) error {
	answers, err := json.Marshal(d.Answers)
	if err != nil {
		return err
	}
	if d.Answers == nil {
		answers = []byte("{}")
	}
	var outcome []byte
	if d.Outcome != nil {
		if outcome, err = json.Marshal(d.Outcome); err != nil {
			return err
		}
	}
	var parent *string
	if d.ParentID != nil {
		id := d.ParentID.String()
		parent = &id
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO decisions (id, parent_id, at, percept_id, set_id, tier, provider, answers,
		                       action, reply, gate, latency_ms, cost_usd, outcome, affect)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
		d.ID.String(), parent, d.At, nullULID(d.PerceptID), d.SetID, string(d.Tier), d.Provider,
		answers, d.Action, nullString(d.Reply), string(d.Gate), float64(d.Latency)/float64(time.Millisecond),
		d.CostUSD, nullJSON(outcome), nullJSON(d.Affect))
	if err != nil {
		return fmt.Errorf("save decision: %w", err)
	}
	return nil
}

// RecentDecisions returns the newest n decisions, newest first.
func (s *Store) RecentDecisions(ctx context.Context, n int) ([]decision.Decision, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+decisionColumns+` FROM decisions d ORDER BY d.at DESC LIMIT $1`, n)
	if err != nil {
		return nil, fmt.Errorf("query decisions: %w", err)
	}
	defer rows.Close()
	var out []decision.Decision
	for rows.Next() {
		d, err := scanDecision(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

const decisionColumns = `d.id, d.parent_id, d.at, d.percept_id, d.set_id, d.tier, d.provider, d.answers,
	d.action, coalesce(d.reply, ''), d.gate, d.latency_ms, d.cost_usd::float8, d.outcome, d.affect`

// scanDecision reads decisionColumns, then into extra any columns after them.
func scanDecision(row pgx.Row, extra ...any) (decision.Decision, error) {
	var (
		d                        decision.Decision
		id                       string
		parentID, perceptID      *string
		tier, gate               string
		answers, outcome, affect []byte
		latencyMS                float64
	)
	dest := append([]any{&id, &parentID, &d.At, &perceptID, &d.SetID, &tier, &d.Provider,
		&answers, &d.Action, &d.Reply, &gate, &latencyMS, &d.CostUSD, &outcome, &affect}, extra...)
	if err := row.Scan(dest...); err != nil {
		return d, err
	}
	var err error
	if d.ID, err = ulid.Parse(id); err != nil {
		return d, err
	}
	if d.PerceptID, err = parseNullULID(perceptID); err != nil { // zero: a decision of the agent's own
		return d, err
	}
	if parentID != nil {
		pid, err := ulid.Parse(*parentID)
		if err != nil {
			return d, err
		}
		d.ParentID = &pid
	}
	d.Tier, d.Gate = decision.Tier(tier), decision.Gate(gate)
	d.Latency = time.Duration(latencyMS * float64(time.Millisecond))
	if err := json.Unmarshal(answers, &d.Answers); err != nil {
		return d, err
	}
	if outcome != nil {
		d.Outcome = &decision.Outcome{}
		if err := json.Unmarshal(outcome, d.Outcome); err != nil {
			return d, err
		}
	}
	d.Affect = affect
	return d, nil
}

// RecentExchanges returns up to limit messages in a conversation since
// the given time, oldest first, each with the reply it got, plus what the
// agent said first (reminders, check-ins). The percept being handled is left out.
func (s *Store) RecentExchanges(ctx context.Context, source, thread string, since time.Time,
	exclude ulid.ULID, limit int) ([]memory.Exchange, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT p.at, p.text, coalesce(r.reply, '')
		FROM percepts p
		LEFT JOIN LATERAL (
			SELECT d.reply FROM decisions d
			WHERE d.percept_id = p.id AND d.reply IS NOT NULL
			ORDER BY d.at DESC LIMIT 1
		) r ON true
		WHERE p.source = $1 AND p.thread = $2 AND p.at >= $3 AND p.id <> $4
		  AND (p.text <> '' OR p.kind IN ($6, $7))
		ORDER BY p.at DESC, p.id DESC
		LIMIT $5`, source, thread, since, exclude.String(), limit, perception.KindReminder, perception.KindCheckIn)
	if err != nil {
		return nil, fmt.Errorf("query recent exchanges: %w", err)
	}
	defer rows.Close()

	var out []memory.Exchange
	for rows.Next() {
		var e memory.Exchange
		if err := rows.Scan(&e.At, &e.Message, &e.Reply); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	slices.Reverse(out) // oldest first, the order a conversation is read in
	return out, nil
}

func (s *Store) SaveFact(ctx context.Context, f memory.Fact) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO facts (id, percept_id, subject, text, at, areas) VALUES ($1, $2, $3, $4, $5, $6)`,
		f.ID.String(), f.PerceptID.String(), f.Subject, f.Text, f.At, nonNil(f.Areas))
	if err != nil {
		return fmt.Errorf("save fact: %w", err)
	}
	return nil
}

const factColumns = `id, percept_id, subject, text, at, areas`

// RecentFacts returns up to limit facts, newest first.
func (s *Store) RecentFacts(ctx context.Context, limit int) ([]memory.Fact, error) {
	return s.queryFacts(ctx, `SELECT `+factColumns+` FROM facts
		ORDER BY at DESC, id DESC LIMIT $1`, limit)
}

// RecallCandidates returns up to limit facts about the given subjects: those
// sharing an area with areas first, then the rest, newest first within each group.
func (s *Store) RecallCandidates(ctx context.Context, subjects, areas []string, limit int) ([]memory.Fact, error) {
	return s.queryFacts(ctx, `SELECT `+factColumns+` FROM facts
		WHERE subject = ANY($1)
		ORDER BY (areas && $2) DESC, at DESC, id DESC LIMIT $3`, nonNil(subjects), nonNil(areas), limit)
}

// UnlabelledFacts returns up to limit facts that have no areas yet, oldest first.
func (s *Store) UnlabelledFacts(ctx context.Context, limit int) ([]memory.Fact, error) {
	return s.queryFacts(ctx, `SELECT `+factColumns+` FROM facts
		WHERE areas = '{}' ORDER BY at, id LIMIT $1`, limit)
}

func (s *Store) SetFactAreas(ctx context.Context, id ulid.ULID, areas []string) error {
	if _, err := s.pool.Exec(ctx, `UPDATE facts SET areas = $2 WHERE id = $1`, id.String(), nonNil(areas)); err != nil {
		return fmt.Errorf("set fact areas: %w", err)
	}
	return nil
}

// UndecidedPublicFacts returns up to limit facts about the agent itself whose
// place on its public page is not decided yet, oldest first.
func (s *Store) UndecidedPublicFacts(ctx context.Context, limit int) ([]memory.Fact, error) {
	return s.queryFacts(ctx, `SELECT `+factColumns+` FROM facts
		WHERE subject = $1 AND public IS NULL ORDER BY at, id LIMIT $2`, memory.SubjectSelf, limit)
}

// SetFactPublic records whether the agent would put a fact on its public page.
func (s *Store) SetFactPublic(ctx context.Context, id ulid.ULID, public bool) error {
	if _, err := s.pool.Exec(ctx, `UPDATE facts SET public = $2 WHERE id = $1`, id.String(), public); err != nil {
		return fmt.Errorf("set fact public: %w", err)
	}
	return nil
}

// PublicFacts returns up to limit of the newest facts about the agent itself
// that it would put on its public page, oldest first. Facts about people are
// never among them.
func (s *Store) PublicFacts(ctx context.Context, limit int) ([]memory.Fact, error) {
	facts, err := s.queryFacts(ctx, `SELECT `+factColumns+` FROM facts
		WHERE subject = $1 AND public ORDER BY at DESC, id DESC LIMIT $2`, memory.SubjectSelf, limit)
	slices.Reverse(facts)
	return facts, err
}

// Profile returns the public profile page as the agent last wrote it (JSON),
// what it was written from (a hash), and when; ok is false if it never has.
func (s *Store) Profile(ctx context.Context) (page []byte, inputs string, at time.Time, ok bool, err error) {
	err = s.pool.QueryRow(ctx, `SELECT page, inputs, written_at FROM profile WHERE id = 1`).Scan(&page, &inputs, &at)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", time.Time{}, false, nil
	}
	if err != nil {
		return nil, "", time.Time{}, false, fmt.Errorf("load profile: %w", err)
	}
	return page, inputs, at, true, nil
}

// SaveProfile replaces the public profile page.
func (s *Store) SaveProfile(ctx context.Context, page []byte, inputs string, at time.Time) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO profile (id, page, inputs, written_at) VALUES (1, $1, $2, $3)
		ON CONFLICT (id) DO UPDATE SET page = EXCLUDED.page, inputs = EXCLUDED.inputs, written_at = EXCLUDED.written_at`,
		string(page), inputs, at)
	if err != nil {
		return fmt.Errorf("save profile: %w", err)
	}
	return nil
}

func (s *Store) queryFacts(ctx context.Context, sql string, args ...any) ([]memory.Fact, error) {
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("query facts: %w", err)
	}
	defer rows.Close()

	var out []memory.Fact
	for rows.Next() {
		var (
			f             memory.Fact
			id, perceptID string
		)
		if err := rows.Scan(&id, &perceptID, &f.Subject, &f.Text, &f.At, &f.Areas); err != nil {
			return nil, err
		}
		if f.ID, err = ulid.Parse(id); err != nil {
			return nil, err
		}
		if f.PerceptID, err = ulid.Parse(perceptID); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

const intentionColumns = `id, subject, kind, percept_id, project_id, text, ability, args, due_at, status,
	coalesce(result, ''), created_at, done_at`

// SaveIntention stores something the agent means to do later.
func (s *Store) SaveIntention(ctx context.Context, i memory.Intention) error {
	args, err := json.Marshal(orEmpty(i.Args))
	if err != nil {
		return fmt.Errorf("save intention: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO intentions (id, subject, kind, percept_id, project_id, text, ability, args, due_at, status,
		                        result, created_at, done_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
		i.ID.String(), i.Subject, i.Kind, nullULID(i.PerceptID), nullULID(i.ProjectID), i.Text, i.Ability, args,
		i.DueAt, i.Status, nullString(i.Result), i.CreatedAt, nullTime(i.DoneAt))
	if err != nil {
		return fmt.Errorf("save intention: %w", err)
	}
	return nil
}

// DueIntentions returns up to limit pending intentions due by now, earliest first.
func (s *Store) DueIntentions(ctx context.Context, now time.Time, limit int) ([]memory.Intention, error) {
	return s.queryIntentions(ctx, `SELECT `+intentionColumns+` FROM intentions
		WHERE status = $1 AND due_at <= $2 ORDER BY due_at, id LIMIT $3`, memory.IntentionPending, now, limit)
}

// IntentionsDue returns the intentions due in [from, to), whatever their status, earliest first.
func (s *Store) IntentionsDue(ctx context.Context, from, to time.Time) ([]memory.Intention, error) {
	return s.queryIntentions(ctx, `SELECT `+intentionColumns+` FROM intentions
		WHERE due_at >= $1 AND due_at < $2 ORDER BY due_at, id`, from, to)
}

// FinishIntention records how an intention ended (done, missed, cancelled) and what came of it.
func (s *Store) FinishIntention(ctx context.Context, id ulid.ULID, status, result string, at time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE intentions SET status = $2, result = $3, done_at = $4 WHERE id = $1`,
		id.String(), status, nullString(result), at)
	if err != nil {
		return fmt.Errorf("finish intention: %w", err)
	}
	return nil
}

func orEmpty(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

// PostponeIntention moves a pending intention to a later time.
func (s *Store) PostponeIntention(ctx context.Context, id ulid.ULID, due time.Time) error {
	if _, err := s.pool.Exec(ctx, `UPDATE intentions SET due_at = $2 WHERE id = $1`, id.String(), due); err != nil {
		return fmt.Errorf("postpone intention: %w", err)
	}
	return nil
}

func (s *Store) queryIntentions(ctx context.Context, sql string, args ...any) ([]memory.Intention, error) {
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("query intentions: %w", err)
	}
	defer rows.Close()
	var out []memory.Intention
	for rows.Next() {
		var (
			i                    memory.Intention
			id                   string
			perceptID, projectID *string
			doneAt               *time.Time
			args                 []byte
		)
		if err := rows.Scan(&id, &i.Subject, &i.Kind, &perceptID, &projectID, &i.Text, &i.Ability, &args,
			&i.DueAt, &i.Status, &i.Result, &i.CreatedAt, &doneAt); err != nil {
			return nil, err
		}
		if i.ID, err = ulid.Parse(id); err != nil {
			return nil, err
		}
		if i.PerceptID, err = parseNullULID(perceptID); err != nil {
			return nil, err
		}
		if i.ProjectID, err = parseNullULID(projectID); err != nil {
			return nil, err
		}
		if doneAt != nil {
			i.DoneAt = *doneAt
		}
		if err := json.Unmarshal(args, &i.Args); err != nil {
			return nil, fmt.Errorf("intention %s args: %w", id, err)
		}
		if len(i.Args) == 0 {
			i.Args = nil
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// projectSelect reads projects with the book each one follows, if any.
const projectSelect = `SELECT p.id, p.title, p.kind, p.status, p.notes, p.source_id, p.position, p.created_at, p.updated_at,
	s.kind, s.title, s.author, s.chars FROM projects p LEFT JOIN sources s ON s.id = p.source_id`

// SaveProject stores a project, replacing what was known about it.
func (s *Store) SaveProject(ctx context.Context, p memory.Project) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO projects (id, title, kind, status, notes, source_id, position, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (id) DO UPDATE SET title = EXCLUDED.title, kind = EXCLUDED.kind, status = EXCLUDED.status,
			notes = EXCLUDED.notes, source_id = EXCLUDED.source_id, position = EXCLUDED.position, updated_at = EXCLUDED.updated_at`,
		p.ID.String(), p.Title, p.Kind, p.Status, p.Notes, nullULID(p.SourceID), p.Position, p.CreatedAt, p.UpdatedAt)
	if err != nil {
		return fmt.Errorf("save project: %w", err)
	}
	return nil
}

// Project returns the project with id, and whether there is one.
func (s *Store) Project(ctx context.Context, id ulid.ULID) (memory.Project, bool, error) {
	ps, err := s.queryProjects(ctx, projectSelect+` WHERE p.id = $1`, id.String())
	if err != nil || len(ps) == 0 {
		return memory.Project{}, false, err
	}
	return ps[0], true, nil
}

// ActiveProjects returns up to limit active projects, most recently worked on first.
func (s *Store) ActiveProjects(ctx context.Context, limit int) ([]memory.Project, error) {
	return s.queryProjects(ctx, projectSelect+` WHERE p.status = $1 ORDER BY p.updated_at DESC, p.id LIMIT $2`,
		memory.ProjectActive, limit)
}

func (s *Store) queryProjects(ctx context.Context, sql string, args ...any) ([]memory.Project, error) {
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("query projects: %w", err)
	}
	defer rows.Close()
	var out []memory.Project
	for rows.Next() {
		var (
			p                      memory.Project
			id                     string
			sourceID               *string
			sKind, sTitle, sAuthor *string
			sChars                 *int
		)
		if err := rows.Scan(&id, &p.Title, &p.Kind, &p.Status, &p.Notes, &sourceID, &p.Position, &p.CreatedAt, &p.UpdatedAt,
			&sKind, &sTitle, &sAuthor, &sChars); err != nil {
			return nil, err
		}
		if p.ID, err = ulid.Parse(id); err != nil {
			return nil, err
		}
		if p.SourceID, err = parseNullULID(sourceID); err != nil {
			return nil, err
		}
		if sTitle != nil {
			p.Source = &memory.Reading{Kind: *sKind, Title: *sTitle, Author: *sAuthor, Chars: *sChars}
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SaveSource stores a book or article it fetched and returns its ID: the one
// already stored if the same URL was fetched before.
func (s *Store) SaveSource(ctx context.Context, src memory.Source) (ulid.ULID, error) {
	var id string
	err := s.pool.QueryRow(ctx, `
		INSERT INTO sources (id, kind, title, author, url, text, chars, fetched_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (url) DO UPDATE SET url = EXCLUDED.url RETURNING id`,
		src.ID.String(), src.Kind, src.Title, src.Author, src.URL, src.Text, len(src.Text), src.FetchedAt).Scan(&id)
	if err != nil {
		return ulid.ULID{}, fmt.Errorf("save source: %w", err)
	}
	return ulid.Parse(id)
}

// SourceText returns the text of a book or article it fetched.
func (s *Store) SourceText(ctx context.Context, id ulid.ULID) (string, error) {
	var text string
	if err := s.pool.QueryRow(ctx, `SELECT text FROM sources WHERE id = $1`, id.String()).Scan(&text); err != nil {
		return "", fmt.Errorf("load source: %w", err)
	}
	return text, nil
}

const workColumns = `id, intention_id, project_id, kind, title, text, created_at`

// SaveWork stores something the agent made.
func (s *Store) SaveWork(ctx context.Context, w memory.Work) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO works (id, intention_id, project_id, kind, title, text, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		w.ID.String(), nullULID(w.IntentionID), nullULID(w.ProjectID), w.Kind, w.Title, w.Text, w.CreatedAt)
	if err != nil {
		return fmt.Errorf("save work: %w", err)
	}
	return nil
}

// WorksBetween returns what the agent made in [from, to), oldest first.
func (s *Store) WorksBetween(ctx context.Context, from, to time.Time) ([]memory.Work, error) {
	return s.queryWorks(ctx, `SELECT `+workColumns+` FROM works
		WHERE created_at >= $1 AND created_at < $2 ORDER BY created_at, id`, from, to)
}

// ProjectWorks returns up to limit of the newest works in a project, oldest first.
func (s *Store) ProjectWorks(ctx context.Context, project ulid.ULID, limit int) ([]memory.Work, error) {
	ws, err := s.queryWorks(ctx, `SELECT `+workColumns+` FROM works
		WHERE project_id = $1 ORDER BY created_at DESC, id DESC LIMIT $2`, project.String(), limit)
	slices.Reverse(ws)
	return ws, err
}

// LatestWork returns the newest work of kind, and whether there is one.
func (s *Store) LatestWork(ctx context.Context, kind string) (memory.Work, bool, error) {
	ws, err := s.queryWorks(ctx, `SELECT `+workColumns+` FROM works
		WHERE kind = $1 ORDER BY created_at DESC, id DESC LIMIT 1`, kind)
	if err != nil || len(ws) == 0 {
		return memory.Work{}, false, err
	}
	return ws[0], true, nil
}

func (s *Store) queryWorks(ctx context.Context, sql string, args ...any) ([]memory.Work, error) {
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("query works: %w", err)
	}
	defer rows.Close()
	var out []memory.Work
	for rows.Next() {
		var (
			w                      memory.Work
			id                     string
			intentionID, projectID *string
		)
		if err := rows.Scan(&id, &intentionID, &projectID, &w.Kind, &w.Title, &w.Text, &w.CreatedAt); err != nil {
			return nil, err
		}
		if w.ID, err = ulid.Parse(id); err != nil {
			return nil, err
		}
		if w.IntentionID, err = parseNullULID(intentionID); err != nil {
			return nil, err
		}
		if w.ProjectID, err = parseNullULID(projectID); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// CostSince returns what the decisions with these actions cost since the given time.
func (s *Store) CostSince(ctx context.Context, since time.Time, actions []string) (float64, error) {
	var cost float64
	err := s.pool.QueryRow(ctx, `SELECT coalesce(sum(cost_usd), 0)::float8 FROM decisions
		WHERE at >= $1 AND action = ANY($2)`, since, actions).Scan(&cost)
	if err != nil {
		return 0, fmt.Errorf("query cost: %w", err)
	}
	return cost, nil
}

// Conversations returns, for each person who has messaged the agent, the
// thread of their latest message and when anything was last said in it.
func (s *Store) Conversations(ctx context.Context) ([]memory.Conversation, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (p.speaker) p.speaker, p.source, p.thread, p.payload,
		       (SELECT max(q.at) FROM percepts q WHERE q.source = p.source AND q.thread = p.thread)
		FROM percepts p
		WHERE p.kind = 'message' AND p.speaker <> '' AND p.thread <> ''
		ORDER BY p.speaker, p.at DESC`)
	if err != nil {
		return nil, fmt.Errorf("query conversations: %w", err)
	}
	defer rows.Close()
	var out []memory.Conversation
	for rows.Next() {
		var c memory.Conversation
		if err := rows.Scan(&c.Person, &c.Source, &c.Thread, &c.Payload, &c.LastActivity); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// LastSpokeFirst returns when the agent last spoke first to person with a
// percept of kind (a reminder or a check-in), and whether it ever did.
func (s *Store) LastSpokeFirst(ctx context.Context, person, kind string) (time.Time, bool, error) {
	var at *time.Time
	err := s.pool.QueryRow(ctx, `SELECT max(at) FROM percepts WHERE speaker = $1 AND kind = $2`, person, kind).Scan(&at)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("query last %s: %w", kind, err)
	}
	if at == nil {
		return time.Time{}, false, nil
	}
	return *at, true, nil
}

// UnaskedFacts returns up to limit facts about person since the given time
// that the agent has not checked in about yet, newest first.
func (s *Store) UnaskedFacts(ctx context.Context, person string, since time.Time, limit int) ([]memory.Fact, error) {
	return s.queryFacts(ctx, `SELECT `+factColumns+` FROM facts
		WHERE subject = $1 AND at >= $2 AND asked_at IS NULL ORDER BY at DESC, id DESC LIMIT $3`, person, since, limit)
}

// MarkFactsAsked records that the agent checked in about these facts.
func (s *Store) MarkFactsAsked(ctx context.Context, ids []ulid.ULID, at time.Time) error {
	keys := make([]string, len(ids))
	for i, id := range ids {
		keys[i] = id.String()
	}
	if _, err := s.pool.Exec(ctx, `UPDATE facts SET asked_at = $2 WHERE id = ANY($1)`, keys, at); err != nil {
		return fmt.Errorf("mark facts asked: %w", err)
	}
	return nil
}

const personColumns = `key, name, relation, circle, source, first_seen, updated_at`

// Person returns the person with key, and whether the agent knows them.
func (s *Store) Person(ctx context.Context, key string) (memory.Person, bool, error) {
	people, err := s.queryPeople(ctx, `SELECT `+personColumns+` FROM people WHERE key = $1`, key)
	if err != nil || len(people) == 0 {
		return memory.Person{}, false, err
	}
	return people[0], true, nil
}

// KnownPeople returns everyone whose relation to the agent is known, oldest acquaintance first.
func (s *Store) KnownPeople(ctx context.Context, limit int) ([]memory.Person, error) {
	return s.queryPeople(ctx, `SELECT `+personColumns+` FROM people WHERE relation <> ''
		ORDER BY first_seen, key LIMIT $1`, limit)
}

// SavePerson stores p, replacing what was known about them.
func (s *Store) SavePerson(ctx context.Context, p memory.Person) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO people (key, name, relation, circle, source, first_seen, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (key) DO UPDATE SET name = EXCLUDED.name, relation = EXCLUDED.relation,
			circle = EXCLUDED.circle, source = EXCLUDED.source, updated_at = EXCLUDED.updated_at`,
		p.Key, p.Name, p.Relation, p.Circle, p.Source, p.FirstSeen, p.UpdatedAt)
	if err != nil {
		return fmt.Errorf("save person: %w", err)
	}
	return nil
}

// AddPersonIfNew stores p only if the agent does not know them yet, and
// reports whether it did. Used for what the agent was born knowing: memory,
// once formed, wins over the file it came from.
func (s *Store) AddPersonIfNew(ctx context.Context, p memory.Person) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO people (key, name, relation, circle, source, first_seen, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT (key) DO NOTHING`,
		p.Key, p.Name, p.Relation, p.Circle, p.Source, p.FirstSeen, p.UpdatedAt)
	if err != nil {
		return false, fmt.Errorf("add person: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Store) queryPeople(ctx context.Context, sql string, args ...any) ([]memory.Person, error) {
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("query people: %w", err)
	}
	defer rows.Close()
	var out []memory.Person
	for rows.Next() {
		var p memory.Person
		if err := rows.Scan(&p.Key, &p.Name, &p.Relation, &p.Circle, &p.Source, &p.FirstSeen, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// nonNil keeps a nil slice from becoming SQL NULL in a NOT NULL array column.
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// nullULID maps the zero ULID to SQL NULL.
func nullULID(id ulid.ULID) any {
	if id == (ulid.ULID{}) {
		return nil
	}
	return id.String()
}

// parseNullULID parses a nullable ULID column; NULL is the zero ULID.
func parseNullULID(s *string) (ulid.ULID, error) {
	if s == nil {
		return ulid.ULID{}, nil
	}
	return ulid.Parse(*s)
}

// nullTime maps the zero time to SQL NULL.
func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullJSON maps empty JSON to SQL NULL.
func nullJSON(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return string(b)
}
