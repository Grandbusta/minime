-- +goose Up
-- What each work is about, as a vector, so the agent can find the one a
-- message means. embedded_with is the model, so a new model embeds them again.
ALTER TABLE works ADD COLUMN embedding REAL[];
ALTER TABLE works ADD COLUMN embedded_with TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE works DROP COLUMN embedded_with;
ALTER TABLE works DROP COLUMN embedding;
