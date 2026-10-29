-- +goose Up
-- Who decided whether a work may go on the agent's public page (works.public,
-- NULL until decided): 'system1' (the daily check, public_work question set)
-- or 'owner' (minime work show/hide), which the daily check never overrides.
-- Its journal and what it made for someone (a promise) are never public,
-- whatever these say.
ALTER TABLE works ADD COLUMN public_by TEXT;

-- +goose Down
ALTER TABLE works DROP COLUMN public_by;
