-- +goose Up
ALTER TABLE ob_tokens DROP COLUMN IF EXISTS thumbprint;

-- +goose Down
ALTER TABLE ob_tokens ADD COLUMN IF NOT EXISTS thumbprint text NOT NULL DEFAULT '';
