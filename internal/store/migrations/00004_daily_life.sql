-- +goose Up
-- Prospective memory grows from reminders into intentions: what the agent
-- means to do later, for someone (a reminder, a promised task) or for itself
-- (an activity of its own day).
ALTER TABLE reminders RENAME TO intentions;
ALTER INDEX reminders_due_idx RENAME TO intentions_due_idx;
ALTER TABLE intentions RENAME COLUMN person TO subject;            -- 'self' or a person's key, like facts
ALTER TABLE intentions ADD COLUMN kind TEXT NOT NULL DEFAULT 'remind'; -- remind | task | activity
ALTER TABLE intentions ALTER COLUMN percept_id DROP NOT NULL;       -- NULL: it planned this itself
ALTER TABLE intentions ADD COLUMN project_id TEXT;
ALTER TABLE intentions ADD COLUMN result TEXT;                     -- what came of it
ALTER TABLE intentions ADD COLUMN done_at TIMESTAMPTZ;
UPDATE intentions SET status = 'done' WHERE status = 'sent';
UPDATE intentions SET status = 'missed' WHERE status = 'failed';

-- What takes the agent more than a day: a book, a beat, a topic. notes is
-- where it got to, so tomorrow continues from today.
CREATE TABLE projects (
    id         TEXT PRIMARY KEY,
    title      TEXT NOT NULL,
    kind       TEXT NOT NULL,             -- music, writing, reading, learning, ...
    status     TEXT NOT NULL,             -- active | done | dropped
    notes      TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

ALTER TABLE intentions ADD CONSTRAINT intentions_project_fk FOREIGN KEY (project_id) REFERENCES projects (id);

-- What the agent made: a journal entry, lyrics, a beat idea, notes. Real
-- text, so its profile's Work section can show it.
CREATE TABLE works (
    id           TEXT PRIMARY KEY,
    intention_id TEXT REFERENCES intentions (id),
    project_id   TEXT REFERENCES projects (id),
    kind         TEXT NOT NULL,           -- journal, lyrics, beat_idea, notes, ...
    title        TEXT NOT NULL,
    text         TEXT NOT NULL,
    public       BOOLEAN,                 -- whether it would show it on its profile; NULL: not decided
    created_at   TIMESTAMPTZ NOT NULL
);

CREATE INDEX works_created_idx ON works (created_at DESC);

-- +goose Down
DROP TABLE works;
ALTER TABLE intentions DROP CONSTRAINT intentions_project_fk;
DROP TABLE projects;
UPDATE intentions SET status = 'sent' WHERE status = 'done';
UPDATE intentions SET status = 'failed' WHERE status = 'missed';
DELETE FROM intentions WHERE kind <> 'remind';
ALTER TABLE intentions DROP COLUMN done_at;
ALTER TABLE intentions DROP COLUMN result;
ALTER TABLE intentions DROP COLUMN project_id;
ALTER TABLE intentions ALTER COLUMN percept_id SET NOT NULL;
ALTER TABLE intentions DROP COLUMN kind;
ALTER TABLE intentions RENAME COLUMN subject TO person;
ALTER INDEX intentions_due_idx RENAME TO reminders_due_idx;
ALTER TABLE intentions RENAME TO reminders;
