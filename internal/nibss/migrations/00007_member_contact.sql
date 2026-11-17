-- +goose Up
ALTER TABLE institutions ADD COLUMN IF NOT EXISTS credit_url text NOT NULL DEFAULT '';
ALTER TABLE institutions ADD COLUMN IF NOT EXISTS certificate_identity text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE institutions DROP COLUMN IF EXISTS certificate_identity;
ALTER TABLE institutions DROP COLUMN IF EXISTS credit_url;
