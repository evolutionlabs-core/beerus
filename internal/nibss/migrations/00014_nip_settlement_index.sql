-- +goose Up
CREATE INDEX IF NOT EXISTS nip_sessions_settlement ON nip_sessions (settlement_session);

-- +goose Down
DROP INDEX IF EXISTS nip_sessions_settlement;
