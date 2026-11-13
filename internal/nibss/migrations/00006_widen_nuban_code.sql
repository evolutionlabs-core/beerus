-- +goose Up
ALTER TABLE institutions DROP CONSTRAINT IF EXISTS institutions_nuban_code_check;
ALTER TABLE institutions ADD CONSTRAINT institutions_nuban_code_check CHECK (nuban_code ~ '^([0-9]{3}|[0-9]{5}|[0-9]{6})$');

-- +goose Down
ALTER TABLE institutions DROP CONSTRAINT IF EXISTS institutions_nuban_code_check;
ALTER TABLE institutions ADD CONSTRAINT institutions_nuban_code_check CHECK (nuban_code ~ '^([0-9]{3}|[0-9]{6})$');
