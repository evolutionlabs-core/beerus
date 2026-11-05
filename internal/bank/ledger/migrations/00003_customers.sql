-- +goose Up
CREATE TABLE customers (
    seq bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    id text NOT NULL UNIQUE,
    idempotency_key text NOT NULL UNIQUE,
    name text NOT NULL CHECK (name <> ''),
    phone text NOT NULL CHECK (phone <> ''),
    dob date NOT NULL,
    bvn text NOT NULL CHECK (bvn <> ''),
    username text UNIQUE CHECK (username <> ''),
    password_hash text,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'closed')),
    created_at timestamptz NOT NULL,
    closed_at timestamptz,
    CHECK ((username IS NULL) = (password_hash IS NULL)),
    CHECK ((status = 'closed') = (closed_at IS NOT NULL))
);

CREATE SEQUENCE nuban_serial MINVALUE 1 MAXVALUE 999999999 NO CYCLE;

CREATE TABLE account_opens (
    idempotency_key text PRIMARY KEY,
    account_id bigint NOT NULL UNIQUE REFERENCES gl_accounts (id),
    customer_id text NOT NULL REFERENCES customers (id),
    product text NOT NULL,
    opened_at timestamptz NOT NULL
);

ALTER TABLE gl_accounts ADD COLUMN closed_at timestamptz;

CREATE INDEX gl_accounts_owner ON gl_accounts (owner, id) WHERE customer;

-- +goose StatementBegin
CREATE FUNCTION ledger_no_delete() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'bank: % rows are never deleted', TG_TABLE_NAME;
END
$$;
-- +goose StatementEnd

CREATE TRIGGER customers_no_delete BEFORE DELETE ON customers FOR EACH ROW EXECUTE FUNCTION ledger_no_delete();
CREATE TRIGGER gl_accounts_no_delete BEFORE DELETE ON gl_accounts FOR EACH ROW EXECUTE FUNCTION ledger_no_delete();
CREATE TRIGGER account_opens_append_only BEFORE UPDATE OR DELETE ON account_opens FOR EACH ROW EXECUTE FUNCTION ledger_append_only();

-- +goose Down
DROP TRIGGER gl_accounts_no_delete ON gl_accounts;
DROP TABLE account_opens;
DROP TABLE customers;
DROP FUNCTION ledger_no_delete();
DROP SEQUENCE nuban_serial;
DROP INDEX gl_accounts_owner;
ALTER TABLE gl_accounts DROP COLUMN closed_at;
