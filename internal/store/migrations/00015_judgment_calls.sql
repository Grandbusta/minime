-- +goose Up
-- Calls: decisions it made where it predicted what its owner would choose.
-- The owner answers some on Telegram and grades others in the console; how
-- often they agree, by type, is how it earns a type of decision.
CREATE TABLE judgment_calls (
    id            TEXT PRIMARY KEY,
    type          TEXT NOT NULL,                 -- focus | start | new_goal | drop_goal | publish | commit | reach_out | dream
    mode          TEXT NOT NULL,                 -- after (it acted; graded later) | before (waits for the answer)
    situation     TEXT NOT NULL,
    options       TEXT[] NOT NULL,
    pick          INT NOT NULL,                  -- its pick, an index into options
    why           TEXT NOT NULL DEFAULT '',
    confidence    DOUBLE PRECISION NOT NULL DEFAULT 0, -- how sure it is the owner would agree, 0 to 1; 0 unknown
    ref           TEXT,                          -- the goal or approval it is about
    status        TEXT NOT NULL,                 -- logged | asked | answered | expired
    waiting       TEXT NOT NULL DEFAULT '',      -- reason | answer: what the owner's next message is
    waiting_since TIMESTAMPTZ,
    owner_pick    INT,                           -- -1: their own answer
    owner_choice  TEXT NOT NULL DEFAULT '',
    owner_reason  TEXT NOT NULL DEFAULT '',
    agreed        BOOLEAN,
    answered_by   TEXT NOT NULL DEFAULT '',      -- telegram | console | approval
    message       JSONB,                         -- where it was asked
    case_id       TEXT REFERENCES judgment_cases (id),
    created_at    TIMESTAMPTZ NOT NULL,
    asked_at      TIMESTAMPTZ,
    answered_at   TIMESTAMPTZ
);
CREATE INDEX judgment_calls_status ON judgment_calls (status, created_at DESC);
CREATE INDEX judgment_calls_answered ON judgment_calls (type, answered_at DESC) WHERE agreed IS NOT NULL;

-- A goal can wait for the owner's answer before it's set.
-- (goals.status gains "proposed".)

-- +goose Down
DROP TABLE judgment_calls;
