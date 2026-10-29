-- +goose Up
CREATE TABLE account_feed (
    account_id bigint NOT NULL REFERENCES gl_accounts (id),
    seq bigint NOT NULL,
    line_key text NOT NULL,
    pending boolean NOT NULL,
    amount bigint NOT NULL,
    appeared_at timestamptz NOT NULL,
    booked_at timestamptz,
    value_date date NOT NULL,
    narration text NOT NULL,
    counterparty jsonb,
    session_id text,
    bank_ref text,
    channel text NOT NULL,
    balance_after bigint,
    entry_id bigint REFERENCES journal_entries (id),
    PRIMARY KEY (account_id, seq),
    UNIQUE (account_id, line_key, pending)
);

CREATE INDEX account_feed_times ON account_feed (account_id, appeared_at);

CREATE TABLE feed_heads (
    account_id bigint PRIMARY KEY REFERENCES gl_accounts (id),
    seq bigint NOT NULL DEFAULT 0,
    watermark timestamptz
);

CREATE TRIGGER account_feed_append_only BEFORE UPDATE OR DELETE ON account_feed FOR EACH ROW EXECUTE FUNCTION ledger_append_only();

-- +goose Down
DROP TABLE feed_heads;
DROP TABLE account_feed;
