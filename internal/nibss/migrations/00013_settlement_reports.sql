-- +goose Up
CREATE INDEX IF NOT EXISTS settlement_sessions_posted ON settlement_sessions (posted_at, session_key) WHERE posted_at IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS settlement_sessions_posted;
