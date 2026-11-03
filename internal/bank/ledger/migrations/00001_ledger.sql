-- +goose Up
CREATE TABLE gl_accounts (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    code text NOT NULL UNIQUE,
    name text NOT NULL,
    kind text NOT NULL CHECK (kind IN ('asset', 'liability', 'income', 'expense', 'equity')),
    currency char(3) NOT NULL,
    customer boolean NOT NULL DEFAULT false,
    nuban text UNIQUE,
    product text,
    owner text,
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'pnd', 'frozen', 'closed')),
    kyc_tier int NOT NULL DEFAULT 3 CHECK (kyc_tier BETWEEN 1 AND 3),
    overdraft_limit bigint NOT NULL DEFAULT 0 CHECK (overdraft_limit >= 0),
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE balances (
    account_id bigint PRIMARY KEY REFERENCES gl_accounts (id),
    ledger bigint NOT NULL DEFAULT 0,
    events bigint NOT NULL DEFAULT 0
);

CREATE TABLE journal_entries (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    idempotency_key text NOT NULL UNIQUE,
    request_hash text NOT NULL,
    kind text NOT NULL,
    posting_date date NOT NULL,
    value_date date NOT NULL,
    posted_at timestamptz NOT NULL,
    narration text NOT NULL,
    session_id text,
    reverses bigint REFERENCES journal_entries (id),
    limited_account bigint REFERENCES gl_accounts (id),
    limited_amount bigint NOT NULL DEFAULT 0,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX journal_entries_limits ON journal_entries (limited_account, posting_date) WHERE limited_account IS NOT NULL;

CREATE UNIQUE INDEX journal_entries_one_reversal ON journal_entries (reverses) WHERE reverses IS NOT NULL;

CREATE TABLE journal_lines (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    entry_id bigint NOT NULL REFERENCES journal_entries (id),
    line_no int NOT NULL,
    account_id bigint NOT NULL REFERENCES gl_accounts (id),
    amount bigint NOT NULL CHECK (amount <> 0),
    currency char(3) NOT NULL,
    narration text NOT NULL,
    UNIQUE (entry_id, line_no)
);

CREATE INDEX journal_lines_account ON journal_lines (account_id, entry_id);

CREATE TABLE account_events (
    account_id bigint NOT NULL REFERENCES gl_accounts (id),
    seq bigint NOT NULL,
    entry_id bigint NOT NULL REFERENCES journal_entries (id),
    line_id bigint NOT NULL REFERENCES journal_lines (id),
    amount bigint NOT NULL,
    balance_after bigint NOT NULL,
    posted_at timestamptz NOT NULL,
    value_date date NOT NULL,
    narration text NOT NULL,
    session_id text,
    PRIMARY KEY (account_id, seq)
);

-- +goose StatementBegin
CREATE FUNCTION ledger_append_only() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'ledger: % is append-only', TG_TABLE_NAME;
END
$$;
-- +goose StatementEnd

CREATE TRIGGER journal_entries_append_only BEFORE UPDATE OR DELETE ON journal_entries FOR EACH ROW EXECUTE FUNCTION ledger_append_only();
CREATE TRIGGER journal_lines_append_only BEFORE UPDATE OR DELETE ON journal_lines FOR EACH ROW EXECUTE FUNCTION ledger_append_only();
CREATE TRIGGER account_events_append_only BEFORE UPDATE OR DELETE ON account_events FOR EACH ROW EXECUTE FUNCTION ledger_append_only();

-- +goose StatementBegin
CREATE FUNCTION ledger_entry_balanced() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    total numeric;
    n int;
BEGIN
    SELECT coalesce(sum(amount), 0), count(*) INTO total, n FROM journal_lines WHERE entry_id = NEW.entry_id;
    IF total <> 0 OR n < 2 THEN
        RAISE EXCEPTION 'ledger: entry % does not balance', NEW.entry_id;
    END IF;
    RETURN NULL;
END
$$;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER journal_lines_balanced AFTER INSERT ON journal_lines DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION ledger_entry_balanced();

-- +goose Down
DROP TABLE account_events;
DROP TABLE journal_lines;
DROP TABLE journal_entries;
DROP TABLE balances;
DROP TABLE gl_accounts;
DROP FUNCTION ledger_entry_balanced();
DROP FUNCTION ledger_append_only();
