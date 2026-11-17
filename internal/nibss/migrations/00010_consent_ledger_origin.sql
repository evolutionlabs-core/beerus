-- +goose Up
ALTER TABLE consent_ledger ADD COLUMN partner_id text NOT NULL DEFAULT '';
ALTER TABLE consent_ledger ADD COLUMN via text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE consent_ledger DROP COLUMN via;
ALTER TABLE consent_ledger DROP COLUMN partner_id;
