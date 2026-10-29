-- +goose Up
-- What the agent asked its owner before doing (the autonomy gate). The owner
-- approves or denies; unanswered, a request is denied when it expires.
-- An intention waiting on one has status 'awaiting'; a work waiting to be
-- published has public NULL and public_by 'awaiting'.
CREATE TABLE approvals (
    id         TEXT PRIMARY KEY,
    class      TEXT NOT NULL,                -- reach_out | commit | publish | ...
    person     TEXT NOT NULL DEFAULT '',     -- who it concerns; '' if no one
    what       TEXT NOT NULL,                -- what it asks, as the owner sees it
    text       TEXT NOT NULL DEFAULT '',     -- reach_out: the message it would send
    kind       TEXT NOT NULL DEFAULT '',     -- reach_out: reminder | check_in | delivery
    ref        TEXT NOT NULL DEFAULT '',     -- the intention or work it is about
    status     TEXT NOT NULL,                -- pending | approved | denied | expired
    asked_at   TIMESTAMPTZ NOT NULL,
    sent_at    TIMESTAMPTZ,                  -- when the owner was asked
    decided_at TIMESTAMPTZ,
    message    JSONB                         -- where the request was sent, to update it after
);
CREATE INDEX approvals_pending ON approvals (asked_at) WHERE status = 'pending';

-- +goose Down
DROP TABLE approvals;
