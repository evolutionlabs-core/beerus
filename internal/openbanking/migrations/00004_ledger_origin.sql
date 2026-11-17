-- +goose Up
ALTER TABLE ob_events ADD COLUMN IF NOT EXISTS rejected text NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS ob_ledger_origin (
    only_row boolean PRIMARY KEY DEFAULT true CHECK (only_row),
    incarnation text NOT NULL CHECK (incarnation <> '')
);

INSERT INTO ob_ledger_origin (incarnation)
SELECT 'db' || oid::text FROM pg_database WHERE datname = current_database()
ON CONFLICT DO NOTHING;

-- +goose Down
DROP TABLE ob_ledger_origin;
ALTER TABLE ob_events DROP COLUMN rejected;
