-- +goose Up
CREATE TABLE registry_clients (
    client_id text PRIMARY KEY CHECK (client_id <> ''),
    name text NOT NULL CHECK (name <> ''),
    legal_name text NOT NULL CHECK (legal_name <> ''),
    registry_id text NOT NULL CHECK (registry_id <> ''),
    cac text NOT NULL,
    role text NOT NULL CHECK (role IN ('aggregator')),
    cert_cn text NOT NULL UNIQUE CHECK (cert_cn <> ''),
    jwk jsonb NOT NULL,
    redirect_uris jsonb NOT NULL,
    registered_at timestamptz NOT NULL
);

-- +goose Down
DROP TABLE registry_clients;
