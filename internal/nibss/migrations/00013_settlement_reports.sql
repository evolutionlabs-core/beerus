-- +goose Up
CREATE INDEX settlement_sessions_posted ON settlement_sessions (posted_at, session_key) WHERE posted_at IS NOT NULL;

-- +goose Down
DROP INDEX settlement_sessions_posted;
