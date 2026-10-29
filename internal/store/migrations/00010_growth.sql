-- +goose Up
-- Who the agent wants to become: its dreams, the goals that serve them, and
-- its skills, which grow from evidence (scored works, the owner's feedback).
CREATE TABLE dreams (
    id       TEXT PRIMARY KEY,
    text     TEXT NOT NULL,
    status   TEXT NOT NULL,                 -- active | dropped
    source   TEXT NOT NULL,                 -- life (its creator) | self (it chose it)
    since    TIMESTAMPTZ NOT NULL,
    ended_at TIMESTAMPTZ
);

CREATE TABLE goals (
    id         TEXT PRIMARY KEY,
    dream_id   TEXT NOT NULL REFERENCES dreams (id),
    text       TEXT NOT NULL,
    done_when  TEXT NOT NULL,               -- how it'll know it's done
    due        TIMESTAMPTZ NOT NULL,
    status     TEXT NOT NULL,               -- active | done | dropped
    progress   TEXT NOT NULL DEFAULT '',    -- where it stands, from the last review
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

-- A project can serve a goal.
ALTER TABLE projects ADD COLUMN goal_id TEXT REFERENCES goals (id);

CREATE TABLE skills (
    name       TEXT PRIMARY KEY,
    level      DOUBLE PRECISION NOT NULL,   -- 1 to 10
    shown_by   TEXT[] NOT NULL DEFAULT '{}', -- kinds of work that show it
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE skill_evidence (
    id      TEXT PRIMARY KEY,
    skill   TEXT NOT NULL REFERENCES skills (name),
    work_id TEXT REFERENCES works (id),     -- the work it scored; NULL for feedback on no one work
    score   DOUBLE PRECISION NOT NULL,      -- 1 to 10
    source  TEXT NOT NULL,                  -- system1 | owner
    note    TEXT NOT NULL DEFAULT '',
    at      TIMESTAMPTZ NOT NULL
);
CREATE INDEX skill_evidence_skill ON skill_evidence (skill, at DESC);

-- +goose Down
DROP TABLE skill_evidence;
DROP TABLE skills;
ALTER TABLE projects DROP COLUMN goal_id;
DROP TABLE goals;
DROP TABLE dreams;
