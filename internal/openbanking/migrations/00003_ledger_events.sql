-- +goose Up
ALTER TABLE ob_events ADD COLUMN IF NOT EXISTS target text NOT NULL DEFAULT 'connect';

CREATE INDEX IF NOT EXISTS ob_events_ledger ON ob_events (id) WHERE delivered_at IS NULL AND target = 'ledger';

-- +goose Down
DROP INDEX ob_events_ledger;
ALTER TABLE ob_events DROP COLUMN target;
