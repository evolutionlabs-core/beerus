-- +goose Up
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'institutions' AND column_name = 'certificate_identity') THEN
        ALTER TABLE institutions RENAME COLUMN certificate_identity TO caller_id;
    END IF;
END
$$;
-- +goose StatementEnd
UPDATE institutions SET credit_url = 'http://' || substr(credit_url, 9) WHERE credit_url LIKE 'https://%';
UPDATE registry_clients SET cert_cn = 'connect' WHERE cert_cn = 'beerus-connect';

-- +goose Down
UPDATE registry_clients SET cert_cn = 'beerus-connect' WHERE cert_cn = 'connect';
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'institutions' AND column_name = 'caller_id') THEN
        ALTER TABLE institutions RENAME COLUMN caller_id TO certificate_identity;
    END IF;
END
$$;
-- +goose StatementEnd
