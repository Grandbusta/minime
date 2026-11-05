-- +goose Up
-- The person themself, so their database is all of them. Each edit of a
-- section is a new version; the newest is the one used, and old ones stay
-- for history and rollback. doc is the YAML as written, comments and all.
CREATE TABLE self_versions (
    id      TEXT PRIMARY KEY,
    section TEXT NOT NULL,              -- life | living | judgment
    doc     TEXT NOT NULL,
    source  TEXT NOT NULL,              -- import | new | edit | rollback
    at      TIMESTAMPTZ NOT NULL
);
CREATE INDEX self_versions_section ON self_versions (section, at DESC, id DESC);

-- Their keys and settings that aren't for anyone to read, sealed with
-- MINIME_KEY (AES-GCM) before they reach the database. A new value
-- replaces the old: no history.
CREATE TABLE secrets (
    name   TEXT PRIMARY KEY,
    sealed BYTEA NOT NULL,
    set_at TIMESTAMPTZ NOT NULL
);

-- Know-how they've been given (Agent Skills): every file of each skill,
-- SKILL.md included. Files are stored but never run.
CREATE TABLE agent_skills (
    name        TEXT PRIMARY KEY,
    description TEXT NOT NULL,
    added_at    TIMESTAMPTZ NOT NULL
);

CREATE TABLE agent_skill_files (
    skill   TEXT NOT NULL REFERENCES agent_skills (name) ON DELETE CASCADE,
    path    TEXT NOT NULL,              -- slash-separated, inside the skill
    content BYTEA NOT NULL,
    PRIMARY KEY (skill, path)
);

-- +goose Down
DROP TABLE agent_skill_files;
DROP TABLE agent_skills;
DROP TABLE secrets;
DROP TABLE self_versions;
