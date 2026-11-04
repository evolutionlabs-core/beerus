-- +goose Up
CREATE TABLE IF NOT EXISTS ob_consents (
    id text PRIMARY KEY,
    client_id text NOT NULL,
    partner_id text NOT NULL DEFAULT '',
    partner_name text NOT NULL DEFAULT '',
    partner_legal text NOT NULL DEFAULT '',
    purpose text NOT NULL DEFAULT '',
    customer text NOT NULL DEFAULT '',
    status text NOT NULL,
    scopes text[] NOT NULL DEFAULT '{}',
    accounts text[] NOT NULL DEFAULT '{}',
    transaction_from date,
    analytics boolean NOT NULL DEFAULT false,
    created_at timestamptz NOT NULL,
    authorised_at timestamptz,
    expires_at timestamptz,
    last_used_at timestamptz,
    revoked_via text NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS ob_consents_customer ON ob_consents (customer);

CREATE TABLE IF NOT EXISTS ob_tokens (
    hash text PRIMARY KEY,
    kind text NOT NULL,
    consent_id text NOT NULL REFERENCES ob_consents (id),
    client_id text NOT NULL,
    thumbprint text NOT NULL,
    family text NOT NULL,
    parent text NOT NULL DEFAULT '',
    created_at timestamptz NOT NULL,
    expires_at timestamptz NOT NULL,
    rotated_at timestamptz,
    revoked_at timestamptz
);

CREATE INDEX IF NOT EXISTS ob_tokens_consent ON ob_tokens (consent_id);
CREATE INDEX IF NOT EXISTS ob_tokens_parent ON ob_tokens (parent);
CREATE INDEX IF NOT EXISTS ob_tokens_family ON ob_tokens (family);

CREATE TABLE IF NOT EXISTS ob_access (
    id bigserial PRIMARY KEY,
    consent_id text NOT NULL REFERENCES ob_consents (id),
    account text NOT NULL,
    at timestamptz NOT NULL
);

CREATE INDEX IF NOT EXISTS ob_access_consent ON ob_access (consent_id, id);

ALTER TABLE ob_consents ADD COLUMN IF NOT EXISTS scopes text[] NOT NULL DEFAULT '{}';

-- +goose Down
DROP TABLE ob_access;
DROP TABLE ob_tokens;
DROP TABLE ob_consents;
