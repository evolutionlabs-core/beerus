-- +goose Up
ALTER TABLE institutions ADD COLUMN IF NOT EXISTS net_debit_cap_minor bigint CHECK (net_debit_cap_minor >= 0);
ALTER TABLE institutions ADD COLUMN IF NOT EXISTS collateral_minor bigint NOT NULL DEFAULT 0 CHECK (collateral_minor >= 0);

-- +goose Down
ALTER TABLE institutions DROP COLUMN IF EXISTS collateral_minor;
ALTER TABLE institutions DROP COLUMN IF EXISTS net_debit_cap_minor;
