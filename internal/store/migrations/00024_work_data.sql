-- +goose Up
-- What a work carries beside its words is any kind of data now, named by
-- data_kind and defined by the file made from it; scores so far are "score".
ALTER TABLE works RENAME COLUMN score TO data;
ALTER TABLE works ADD COLUMN data_kind TEXT NOT NULL DEFAULT '';
UPDATE works SET data_kind = 'score' WHERE data IS NOT NULL;

-- +goose Down
ALTER TABLE works DROP COLUMN data_kind;
ALTER TABLE works RENAME COLUMN data TO score;
