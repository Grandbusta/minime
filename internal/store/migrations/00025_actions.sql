-- +goose Up
-- Tools that change things (an MCP tool that writes, sends or books): each
-- proposed, then approved by the gate or the owner, then run by the life
-- loop. args is the tool's arguments as JSON text, exactly as checked.
CREATE TABLE actions (
    id          TEXT PRIMARY KEY,
    tool        TEXT NOT NULL,
    class       TEXT NOT NULL,
    args        TEXT NOT NULL,
    why         TEXT NOT NULL DEFAULT '',
    status      TEXT NOT NULL,
    result      TEXT NOT NULL DEFAULT '',
    percept_id  TEXT,
    proposed_at TIMESTAMPTZ NOT NULL,
    done_at     TIMESTAMPTZ
);
CREATE INDEX actions_status ON actions (status, proposed_at);

-- +goose Down
DROP TABLE actions;
