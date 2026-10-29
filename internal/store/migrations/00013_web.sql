-- +goose Up
-- Pages its web searches found: it may read only these, so a link on a page
-- can never send it anywhere.
CREATE TABLE web_found (
    url      TEXT PRIMARY KEY,
    title    TEXT NOT NULL,
    snippet  TEXT NOT NULL DEFAULT '',
    query    TEXT NOT NULL,                -- the search that found it
    found_at TIMESTAMPTZ NOT NULL
);

-- Every search and page read, for the daily caps and what it cost.
CREATE TABLE web_log (
    id       BIGSERIAL PRIMARY KEY,
    kind     TEXT NOT NULL,                -- search | page
    what     TEXT NOT NULL,                -- the query or the address
    cost_usd NUMERIC(14, 8) NOT NULL DEFAULT 0,
    at       TIMESTAMPTZ NOT NULL
);
CREATE INDEX web_log_kind_at ON web_log (kind, at DESC);

-- +goose Down
DROP TABLE web_log;
DROP TABLE web_found;
