-- +goose Up
ALTER TABLE consent_ledger ADD COLUMN incarnation text NOT NULL DEFAULT '';

DROP INDEX consent_ledger_source;
CREATE UNIQUE INDEX consent_ledger_source ON consent_ledger (bank, incarnation, source_seq) WHERE source_seq > 0;

-- +goose Down
DROP INDEX consent_ledger_source;
CREATE UNIQUE INDEX consent_ledger_source ON consent_ledger (bank, source_seq) WHERE source_seq > 0;
ALTER TABLE consent_ledger DROP COLUMN incarnation;
