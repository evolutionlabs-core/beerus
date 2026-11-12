-- +goose Up
CREATE TABLE settlement_sessions (
    session_key text PRIMARY KEY CHECK (session_key <> ''),
    cutoff_at timestamptz NOT NULL,
    posted_at timestamptz
);

CREATE INDEX settlement_sessions_unposted ON settlement_sessions (cutoff_at, session_key) WHERE posted_at IS NULL;

-- +goose Down
DROP TABLE settlement_sessions;
