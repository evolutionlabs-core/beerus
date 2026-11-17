-- +goose Up
CREATE SEQUENCE IF NOT EXISTS nip_send_seq;

CREATE TABLE IF NOT EXISTS nip_sends (
    session_id text PRIMARY KEY CHECK (session_id ~ '^[0-9]{30}$'),
    request jsonb NOT NULL,
    attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_tsq_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL
);

CREATE INDEX IF NOT EXISTS nip_sends_due ON nip_sends (next_tsq_at, session_id);

-- +goose Down
DROP TABLE IF EXISTS nip_sends;
DROP SEQUENCE IF EXISTS nip_send_seq;
