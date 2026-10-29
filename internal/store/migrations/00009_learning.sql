-- +goose Up
-- Changes the owner made to the autonomy policy by chat ("you can send
-- reminders without asking"): every one kept, the newest per class wins over
-- the config.
CREATE TABLE autonomy_rules (
    id         TEXT PRIMARY KEY,
    class      TEXT NOT NULL,
    verdict    TEXT NOT NULL,               -- auto | ask | forbid
    set_by     TEXT NOT NULL,               -- who changed it, e.g. telegram:123
    percept_id TEXT REFERENCES percepts (id),
    at         TIMESTAMPTZ NOT NULL
);
CREATE INDEX autonomy_rules_class ON autonomy_rules (class, at DESC);

-- What a System 1 answer should have been, from a correction in chat or a
-- label in the console: replayed when a question set, threshold or model
-- changes. One per decision and question; a new label replaces the old.
CREATE TABLE labels (
    decision_id TEXT NOT NULL REFERENCES decisions (id),
    question    TEXT NOT NULL,
    value       TEXT NOT NULL,              -- a choice, or "true" / "false"
    source      TEXT NOT NULL,              -- chat | console
    at          TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (decision_id, question)
);

-- +goose Down
DROP TABLE labels;
DROP TABLE autonomy_rules;
