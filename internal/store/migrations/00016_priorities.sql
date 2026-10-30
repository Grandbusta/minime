-- +goose Up
-- Besides principles, its owner's priorities (what matters most, in order:
-- the higher wins when principles clash) and boundaries (never, whatever the
-- gain), kept and reviewed the same way.
ALTER TABLE principles ADD COLUMN kind TEXT NOT NULL DEFAULT 'principle'; -- principle | priority | boundary
ALTER TABLE principles ADD COLUMN rank INT NOT NULL DEFAULT 0;            -- a priority's place, 1 first

-- +goose Down
ALTER TABLE principles DROP COLUMN rank;
ALTER TABLE principles DROP COLUMN kind;
