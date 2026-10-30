-- +goose Up
-- Its owner's taste: what they like and don't in its work, beyond how good
-- it is. From their comments on Telegram and their reactions in the console.
CREATE TABLE tastes (
    id         TEXT PRIMARY KEY,
    kind       TEXT NOT NULL DEFAULT '',     -- the kind of work it's about (beat_idea, ...); '' any
    likes      BOOLEAN NOT NULL,             -- a like, or a dislike
    text       TEXT NOT NULL,                -- the taste, in general terms
    work_id    TEXT REFERENCES works (id),   -- the work that showed it; NULL if none
    source     TEXT NOT NULL,                -- telegram | console
    at         TIMESTAMPTZ NOT NULL,
    retired_at TIMESTAMPTZ                   -- no longer true
);
CREATE INDEX tastes_active ON tastes (at DESC) WHERE retired_at IS NULL;

-- +goose Down
DROP TABLE tastes;
