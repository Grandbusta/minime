-- +goose Up
-- What each of the owner's decisions means, as a vector, so the agent can
-- find the ones most like what it faces. Cleared when the decision's text
-- changes; embedded_with is the model, so a new model embeds them again.
ALTER TABLE judgment_cases ADD COLUMN embedding REAL[];
ALTER TABLE judgment_cases ADD COLUMN embedded_with TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE judgment_cases DROP COLUMN embedded_with;
ALTER TABLE judgment_cases DROP COLUMN embedding;
