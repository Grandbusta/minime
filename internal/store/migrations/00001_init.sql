-- +goose Up
-- What the agent perceived: one row per incoming message or event.
CREATE TABLE percepts (
    id          TEXT PRIMARY KEY,
    source      TEXT NOT NULL,
    external_id TEXT NOT NULL,
    thread      TEXT NOT NULL DEFAULT '', -- the conversation, e.g. the Telegram chat
    speaker     TEXT NOT NULL DEFAULT '', -- who said it, e.g. 'telegram:123'
    kind        TEXT NOT NULL,
    text        TEXT NOT NULL DEFAULT '',
    payload     JSONB,
    at          TIMESTAMPTZ NOT NULL,
    UNIQUE (source, external_id)
);

CREATE INDEX percepts_thread_idx ON percepts (source, thread, at DESC);

-- The decision log: every System 1 and System 2 decision, chained by parent_id.
CREATE TABLE decisions (
    id         TEXT PRIMARY KEY,
    parent_id  TEXT REFERENCES decisions (id),
    at         TIMESTAMPTZ NOT NULL,
    percept_id TEXT REFERENCES percepts (id),
    set_id     TEXT NOT NULL,
    tier       TEXT NOT NULL,
    provider   TEXT NOT NULL DEFAULT '',
    answers    JSONB NOT NULL DEFAULT '{}',
    action     TEXT NOT NULL,
    reply      TEXT, -- what the agent said
    gate       TEXT NOT NULL,
    latency_ms DOUBLE PRECISION NOT NULL DEFAULT 0,
    cost_usd   NUMERIC(14, 8) NOT NULL DEFAULT 0,
    outcome    JSONB,
    affect     JSONB
);

CREATE INDEX decisions_at_idx ON decisions (at DESC);
CREATE INDEX decisions_percept_idx ON decisions (percept_id);

-- Long-term memory: facts about the agent ('self') or a person (their key),
-- labelled with the life areas they are about (life_area question set).
CREATE TABLE facts (
    id         TEXT PRIMARY KEY,
    percept_id TEXT NOT NULL REFERENCES percepts (id),
    subject    TEXT NOT NULL DEFAULT '',
    text       TEXT NOT NULL,
    at         TIMESTAMPTZ NOT NULL,
    areas      TEXT[] NOT NULL DEFAULT '{}'
);

CREATE INDEX facts_at_idx ON facts (at DESC);
CREATE INDEX facts_areas_idx ON facts USING GIN (areas);
CREATE INDEX facts_subject_idx ON facts (subject, at DESC);

-- The people the agent knows, kept in its memory: who they are to it, which
-- circle of its life they belong to, and how it came to know them (born
-- knowing, met, or told). Learned while running; nothing here needs a restart.
CREATE TABLE people (
    key        TEXT PRIMARY KEY,         -- account, e.g. 'telegram:123'
    name       TEXT NOT NULL DEFAULT '',
    relation   TEXT NOT NULL DEFAULT '', -- e.g. 'Busta is Daniel''s father.'
    circle     TEXT NOT NULL DEFAULT '', -- family, friend, school, work, ... (person_circle question set)
    source     TEXT NOT NULL,            -- born | met | told
    first_seen TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

-- +goose Down
DROP TABLE people;
DROP TABLE facts;
DROP TABLE decisions;
DROP TABLE percepts;
