-- +goose Up
-- Prospective memory: what people asked the agent to remind them of.
CREATE TABLE reminders (
    id         TEXT PRIMARY KEY,
    person     TEXT NOT NULL,                       -- who to remind, e.g. 'telegram:123'
    percept_id TEXT NOT NULL REFERENCES percepts (id), -- the message that asked for it
    text       TEXT NOT NULL,
    due_at     TIMESTAMPTZ NOT NULL,
    status     TEXT NOT NULL,                       -- pending | sent | failed
    created_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX reminders_due_idx ON reminders (due_at) WHERE status = 'pending';

-- When the agent last checked in with someone about a fact, so it asks once.
ALTER TABLE facts ADD COLUMN asked_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE facts DROP COLUMN asked_at;
DROP TABLE reminders;
