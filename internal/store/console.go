package store

import (
	"context"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"

	"minime/internal/decision"
	"minime/internal/memory"
)

// Queries for the owner's console: everything about the agent, read-only.

// LogEntry is a decision with what it was about.
type LogEntry struct {
	decision.Decision
	Source, Kind, Text, Speaker string // the percept's; "" for a decision of the agent's own
}

// DecisionLog returns up to limit decisions after the given time, with
// their percepts: oldest first when following the log live, newest first
// for a page of history.
func (s *Store) DecisionLog(ctx context.Context, after time.Time, limit int, newestFirst bool) ([]LogEntry, error) {
	order := "ASC"
	if newestFirst {
		order = "DESC"
	}
	return s.queryLog(ctx, `SELECT `+decisionColumns+`, coalesce(p.source, ''), coalesce(p.kind, ''), coalesce(p.text, ''),
		coalesce(p.speaker, '') FROM decisions d LEFT JOIN percepts p ON p.id = d.percept_id
		WHERE d.at > $1 ORDER BY d.at `+order+`, d.id `+order+` LIMIT $2`, after, limit)
}

// DecisionChain returns the decision with id and every decision about the
// same percept (or, for one of the agent's own, its children), oldest first.
func (s *Store) DecisionChain(ctx context.Context, id ulid.ULID) ([]LogEntry, error) {
	return s.queryLog(ctx, `SELECT `+decisionColumns+`, coalesce(p.source, ''), coalesce(p.kind, ''), coalesce(p.text, ''),
		coalesce(p.speaker, '') FROM decisions d LEFT JOIN percepts p ON p.id = d.percept_id
		WHERE d.id = $1 OR d.parent_id = $1
		   OR d.percept_id = (SELECT percept_id FROM decisions WHERE id = $1)
		ORDER BY d.at, d.id`, id.String())
}

func (s *Store) queryLog(ctx context.Context, sql string, args ...any) ([]LogEntry, error) {
	rows, err := s.pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, fmt.Errorf("query decision log: %w", err)
	}
	defer rows.Close()
	var out []LogEntry
	for rows.Next() {
		var e LogEntry
		if e.Decision, err = scanDecision(rows, &e.Source, &e.Kind, &e.Text, &e.Speaker); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Vitals is how the agent's day is going: decisions, cost and speed since a time.
type Vitals struct {
	Decisions   int
	ByTier      map[string]int
	Escalations int // System 1 was unsure or only fairly sure
	CostUSD     float64
	AvgLatency  time.Duration // of decisions that took any time
}

func (s *Store) Vitals(ctx context.Context, since time.Time) (Vitals, error) {
	v := Vitals{ByTier: map[string]int{}}
	rows, err := s.pool.Query(ctx, `SELECT tier, count(*), count(*) FILTER (WHERE action IN ('ask', 'escalate')),
		coalesce(sum(cost_usd), 0)::float8, coalesce(avg(latency_ms) FILTER (WHERE latency_ms > 0), 0)::float8
		FROM decisions WHERE at >= $1 GROUP BY tier`, since)
	if err != nil {
		return v, fmt.Errorf("query vitals: %w", err)
	}
	defer rows.Close()
	var weighted float64
	for rows.Next() {
		var (
			tier       string
			n, esc     int
			cost, avgL float64
		)
		if err := rows.Scan(&tier, &n, &esc, &cost, &avgL); err != nil {
			return v, err
		}
		v.ByTier[tier], v.Decisions, v.Escalations, v.CostUSD = n, v.Decisions+n, v.Escalations+esc, v.CostUSD+cost
		weighted += avgL * float64(n)
	}
	if v.Decisions > 0 {
		v.AvgLatency = time.Duration(weighted / float64(v.Decisions) * float64(time.Millisecond))
	}
	return v, rows.Err()
}

// DayCost is what one day cost, in the given time zone.
type DayCost struct {
	Day     string // YYYY-MM-DD
	CostUSD float64
	Count   int
}

// CostByDay returns the cost of each day since the given time, in zone, oldest first.
func (s *Store) CostByDay(ctx context.Context, since time.Time, zone *time.Location) ([]DayCost, error) {
	rows, err := s.pool.Query(ctx, `SELECT to_char(at AT TIME ZONE $2, 'YYYY-MM-DD') AS day,
		coalesce(sum(cost_usd), 0)::float8, count(*) FROM decisions WHERE at >= $1 GROUP BY day ORDER BY day`,
		since, zone.String())
	if err != nil {
		return nil, fmt.Errorf("query cost by day: %w", err)
	}
	defer rows.Close()
	var out []DayCost
	for rows.Next() {
		var d DayCost
		if err := rows.Scan(&d.Day, &d.CostUSD, &d.Count); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// FactView is a fact with what the console shows about it.
type FactView struct {
	memory.Fact
	Public  *bool      // whether it may go on the profile; nil: not decided (or not its own)
	AskedAt *time.Time // when it checked in about it; nil: never
}

// AllFacts returns up to limit facts, newest first.
func (s *Store) AllFacts(ctx context.Context, limit int) ([]FactView, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, percept_id, subject, text, at, areas, public, asked_at
		FROM facts ORDER BY at DESC, id DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("query facts: %w", err)
	}
	defer rows.Close()
	var out []FactView
	for rows.Next() {
		var (
			f             FactView
			id, perceptID string
		)
		if err := rows.Scan(&id, &perceptID, &f.Subject, &f.Text, &f.At, &f.Areas, &f.Public, &f.AskedAt); err != nil {
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

// AllPeople returns everyone the agent knows, oldest acquaintance first.
func (s *Store) AllPeople(ctx context.Context, limit int) ([]memory.Person, error) {
	return s.queryPeople(ctx, `SELECT `+personColumns+` FROM people ORDER BY first_seen, key LIMIT $1`, limit)
}

// AllProjects returns up to limit projects, most recently worked on first.
func (s *Store) AllProjects(ctx context.Context, limit int) ([]memory.Project, error) {
	return s.queryProjects(ctx, `SELECT `+projectColumns+` FROM projects ORDER BY updated_at DESC, id LIMIT $1`, limit)
}

// RecentWorks returns up to limit works, newest first.
func (s *Store) RecentWorks(ctx context.Context, limit int) ([]memory.Work, error) {
	return s.queryWorks(ctx, `SELECT `+workColumns+` FROM works ORDER BY created_at DESC, id DESC LIMIT $1`, limit)
}

// Counts is how much the agent remembers.
type Counts struct {
	Facts, SelfFacts, People, Intentions, Projects, Works, Percepts, Decisions int
}

func (s *Store) Counts(ctx context.Context) (Counts, error) {
	var c Counts
	err := s.pool.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM facts), (SELECT count(*) FROM facts WHERE subject = $1), (SELECT count(*) FROM people),
		(SELECT count(*) FROM intentions WHERE status = 'pending'), (SELECT count(*) FROM projects WHERE status = 'active'),
		(SELECT count(*) FROM works), (SELECT count(*) FROM percepts), (SELECT count(*) FROM decisions)`, memory.SubjectSelf).
		Scan(&c.Facts, &c.SelfFacts, &c.People, &c.Intentions, &c.Projects, &c.Works, &c.Percepts, &c.Decisions)
	if err != nil {
		return c, fmt.Errorf("query counts: %w", err)
	}
	return c, nil
}

// Work returns the work with id, and whether there is one.
func (s *Store) Work(ctx context.Context, id ulid.ULID) (memory.Work, bool, error) {
	ws, err := s.queryWorks(ctx, `SELECT `+workColumns+` FROM works WHERE id = $1`, id.String())
	if err != nil || len(ws) == 0 {
		return memory.Work{}, false, err
	}
	return ws[0], true, nil
}
