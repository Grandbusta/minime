-- +goose Up
-- Whether the agent would put one of its own facts on its public page
-- (public_profile question set); NULL until it has decided.
ALTER TABLE facts ADD COLUMN public BOOLEAN;

-- The agent's public profile page, as it last wrote it: one row. inputs is a
-- hash of what it was written from, so it is rewritten only when that changes.
CREATE TABLE profile (
    id         INT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    page       JSONB NOT NULL,
    inputs     TEXT NOT NULL,
    written_at TIMESTAMPTZ NOT NULL
);

-- +goose Down
DROP TABLE profile;
ALTER TABLE facts DROP COLUMN public;
