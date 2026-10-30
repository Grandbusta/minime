-- +goose Up
-- What a call puts at stake, and whether it can be undone: it asks about
-- what matters most first, and trust can follow stakes, not only type.
ALTER TABLE judgment_calls ADD COLUMN stakes TEXT NOT NULL DEFAULT '';        -- low | medium | high; '' unknown
ALTER TABLE judgment_calls ADD COLUMN reversible BOOLEAN NOT NULL DEFAULT TRUE;

-- +goose Down
ALTER TABLE judgment_calls DROP COLUMN reversible;
ALTER TABLE judgment_calls DROP COLUMN stakes;
