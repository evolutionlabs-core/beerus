-- +goose Up
CREATE TABLE consent_ledger (
    seq bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    bank text NOT NULL CHECK (bank <> ''),
    consent_id text NOT NULL CHECK (consent_id <> ''),
    client_id text NOT NULL,
    status text NOT NULL CHECK (status IN ('AwaitingAuthorisation', 'Authorised', 'Rejected', 'Revoked', 'Expired', 'Dormant', 'ReconsentRequired')),
    changed_at timestamptz NOT NULL,
    source_seq bigint NOT NULL CHECK (source_seq >= 0)
);

CREATE UNIQUE INDEX consent_ledger_source ON consent_ledger (bank, source_seq) WHERE source_seq > 0;
CREATE INDEX consent_ledger_bank ON consent_ledger (bank, seq);

-- +goose Down
DROP TABLE consent_ledger;
