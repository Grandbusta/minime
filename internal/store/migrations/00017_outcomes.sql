-- +goose Up
-- How a call turned out, looked at later: what happened, and whether it was
-- the decision or luck. Its weekly review judges it; the owner may correct it.
ALTER TABLE judgment_calls ADD COLUMN outcome TEXT NOT NULL DEFAULT '';      -- went_well | mixed | went_badly
ALTER TABLE judgment_calls ADD COLUMN verdict TEXT NOT NULL DEFAULT '';      -- good_call | lucky | unlucky | bad_call
ALTER TABLE judgment_calls ADD COLUMN outcome_note TEXT NOT NULL DEFAULT ''; -- what happened
ALTER TABLE judgment_calls ADD COLUMN outcome_by TEXT NOT NULL DEFAULT '';   -- self | owner
ALTER TABLE judgment_calls ADD COLUMN outcome_at TIMESTAMPTZ;
-- The owner's decision that a call became, with how it turned out.
ALTER TABLE judgment_cases ADD COLUMN outcome TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE judgment_cases DROP COLUMN outcome;
ALTER TABLE judgment_calls DROP COLUMN outcome_at;
ALTER TABLE judgment_calls DROP COLUMN outcome_by;
ALTER TABLE judgment_calls DROP COLUMN outcome_note;
ALTER TABLE judgment_calls DROP COLUMN verdict;
ALTER TABLE judgment_calls DROP COLUMN outcome;
