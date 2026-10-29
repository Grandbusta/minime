-- +goose Up
-- An activity uses an ability by name (read_book, read_article, journal, ...)
-- with what the plan gave it, instead of columns for each kind of thing it does.
ALTER TABLE intentions ADD COLUMN ability TEXT NOT NULL DEFAULT '';
ALTER TABLE intentions ADD COLUMN args JSONB NOT NULL DEFAULT '{}';
UPDATE intentions SET ability = 'read_' || read_kind, args = jsonb_build_object('query', read_query)
    WHERE read_kind <> '';
UPDATE intentions SET ability = 'journal'
    WHERE subject = 'self' AND kind = 'activity' AND text = 'Write today''s journal entry.';
ALTER TABLE intentions DROP COLUMN read_kind;
ALTER TABLE intentions DROP COLUMN read_query;

-- +goose Down
ALTER TABLE intentions ADD COLUMN read_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE intentions ADD COLUMN read_query TEXT NOT NULL DEFAULT '';
UPDATE intentions SET read_kind = substr(ability, 6), read_query = coalesce(args ->> 'query', '')
    WHERE ability IN ('read_book', 'read_article');
ALTER TABLE intentions DROP COLUMN args;
ALTER TABLE intentions DROP COLUMN ability;
