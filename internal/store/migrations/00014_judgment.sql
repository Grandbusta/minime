-- +goose Up
-- How its owner decides: decisions they made (cases) and the rules drawn
-- from them (principles). Proposed ones wait for the owner's review; only
-- approved ones are used.
CREATE TABLE judgment_cases (
    id         TEXT PRIMARY KEY,
    key        TEXT NOT NULL UNIQUE,          -- the seed file's key; the id for others
    type       TEXT NOT NULL,                 -- general | engineering | product | autonomy | life
    situation  TEXT NOT NULL,
    options    TEXT[] NOT NULL DEFAULT '{}',
    choice     TEXT NOT NULL,
    reason     TEXT NOT NULL DEFAULT '',      -- the owner's own words
    status     TEXT NOT NULL,                 -- proposed | approved | rejected
    source     TEXT NOT NULL,                 -- seed | ...
    at         TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE principles (
    id         TEXT PRIMARY KEY,
    text       TEXT NOT NULL,
    types      TEXT[] NOT NULL DEFAULT '{}',  -- kinds of decision it applies to
    status     TEXT NOT NULL,                 -- proposed | approved | rejected
    source     TEXT NOT NULL,                 -- seed | ...
    seed       TEXT UNIQUE,                   -- its text in the seed file, so it's seeded once even after an edit
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL
);

CREATE TABLE principle_cases (
    principle_id TEXT NOT NULL REFERENCES principles (id) ON DELETE CASCADE,
    case_id      TEXT NOT NULL REFERENCES judgment_cases (id) ON DELETE CASCADE,
    PRIMARY KEY (principle_id, case_id)
);

-- +goose Down
DROP TABLE principle_cases;
DROP TABLE principles;
DROP TABLE judgment_cases;
