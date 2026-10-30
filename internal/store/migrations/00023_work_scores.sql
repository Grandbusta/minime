-- +goose Up
-- A piece of music's notes (render/midi's Score), so it can be sent as MIDI;
-- null for a work that isn't music, or music written before scores.
ALTER TABLE works ADD COLUMN score JSONB;

-- +goose Down
ALTER TABLE works DROP COLUMN score;
