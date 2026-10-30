-- +goose Up
-- Whose memory this is: one person per database, so two never mix.
CREATE TABLE whose (
    one   BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (one),
    name  TEXT NOT NULL,
    since TIMESTAMPTZ NOT NULL
);

-- +goose Down
DROP TABLE whose;
