-- +goose Up
-- What the agent learned about doing a skill well: from its owner's
-- corrections, its own failures, or studying how others do it. Active lessons
-- go into its practice; its weekly review retires the ones it has absorbed.
CREATE TABLE lessons (
    id         TEXT PRIMARY KEY,
    skill      TEXT NOT NULL REFERENCES skills (name),
    text       TEXT NOT NULL,
    source     TEXT NOT NULL,              -- owner | self | others
    work_id    TEXT REFERENCES works (id), -- the work that taught it, if any
    at         TIMESTAMPTZ NOT NULL,
    retired_at TIMESTAMPTZ
);
CREATE INDEX lessons_active ON lessons (skill, at DESC) WHERE retired_at IS NULL;

-- When the agent asked its owner to review a work, so their answer is tied to it.
ALTER TABLE works ADD COLUMN review_asked_at TIMESTAMPTZ;

-- +goose Down
ALTER TABLE works DROP COLUMN review_asked_at;
DROP TABLE lessons;
