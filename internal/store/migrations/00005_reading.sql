-- +goose Up
-- What the agent read: a public-domain book or a Wikipedia article, kept so a
-- book is downloaded once and read over many days.
CREATE TABLE sources (
    id         TEXT PRIMARY KEY,
    kind       TEXT NOT NULL,          -- book | article
    title      TEXT NOT NULL,
    author     TEXT NOT NULL DEFAULT '',
    url        TEXT NOT NULL UNIQUE,
    text       TEXT NOT NULL,
    chars      INT NOT NULL,
    fetched_at TIMESTAMPTZ NOT NULL
);

-- A reading project follows one book, and position is how far into it it got.
ALTER TABLE projects ADD COLUMN source_id TEXT REFERENCES sources (id);
ALTER TABLE projects ADD COLUMN position INT NOT NULL DEFAULT 0;

-- What an activity means to read: a book to find, or a topic to look up.
ALTER TABLE intentions ADD COLUMN read_kind TEXT NOT NULL DEFAULT '';  -- '' | book | article
ALTER TABLE intentions ADD COLUMN read_query TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE intentions DROP COLUMN read_query;
ALTER TABLE intentions DROP COLUMN read_kind;
ALTER TABLE projects DROP COLUMN position;
ALTER TABLE projects DROP COLUMN source_id;
DROP TABLE sources;
