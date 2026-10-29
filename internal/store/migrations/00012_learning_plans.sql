-- +goose Up
-- A goal can be about growing a skill; such a goal gets a learning plan:
-- stages, each with what to learn, a practice piece and a "done when", and
-- resources to study for it.
ALTER TABLE goals ADD COLUMN skill TEXT NOT NULL DEFAULT '';

CREATE TABLE learning_plans (
    id         TEXT PRIMARY KEY,
    goal_id    TEXT NOT NULL UNIQUE REFERENCES goals (id),
    skill      TEXT NOT NULL,
    stage      INT NOT NULL DEFAULT 0,      -- the current stage, from 0
    status     TEXT NOT NULL,               -- active | done | dropped
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE plan_stages (
    plan_id   TEXT NOT NULL REFERENCES learning_plans (id),
    n         INT NOT NULL,                 -- 0, 1, 2, ...
    learn     TEXT NOT NULL,                -- what to learn
    practice  TEXT NOT NULL,                -- a piece to make that practises it
    done_when TEXT NOT NULL,
    done_at   TIMESTAMPTZ,
    PRIMARY KEY (plan_id, n)
);

CREATE TABLE resources (
    id       TEXT PRIMARY KEY,
    plan_id  TEXT NOT NULL REFERENCES learning_plans (id),
    stage    INT NOT NULL,
    ability  TEXT NOT NULL,                 -- how it's read: read_book, read_article, ...
    query    TEXT NOT NULL,                 -- what to look for
    why      TEXT NOT NULL DEFAULT '',
    status   TEXT NOT NULL,                 -- todo | done
    title    TEXT NOT NULL DEFAULT '',      -- what it turned out to be
    work_id  TEXT REFERENCES works (id),    -- its notes on it
    added_at TIMESTAMPTZ NOT NULL,
    done_at  TIMESTAMPTZ
);
CREATE INDEX resources_plan ON resources (plan_id, stage);

-- +goose Down
DROP TABLE resources;
DROP TABLE plan_stages;
DROP TABLE learning_plans;
ALTER TABLE goals DROP COLUMN skill;
