-- +goose Up
CREATE TABLE IF NOT EXISTS ob_events (
    id bigserial PRIMARY KEY,
    consent_id text NOT NULL,
    bank text NOT NULL,
    client_id text NOT NULL,
    partner_id text NOT NULL DEFAULT '',
    status text NOT NULL,
    via text NOT NULL,
    at timestamptz NOT NULL,
    attempts integer NOT NULL DEFAULT 0,
    next_at timestamptz NOT NULL,
    delivered_at timestamptz
);

CREATE INDEX IF NOT EXISTS ob_events_due ON ob_events (next_at) WHERE delivered_at IS NULL;

-- +goose Down
DROP TABLE ob_events;
