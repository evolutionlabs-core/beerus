-- +goose Up
CREATE TABLE nip_sessions (
    session_id text PRIMARY KEY CHECK (session_id <> ''),
    from_code text NOT NULL REFERENCES institutions (nip_code),
    to_code text NOT NULL REFERENCES institutions (nip_code),
    from_nuban text NOT NULL CHECK (from_nuban ~ '^[0-9]{10}$'),
    to_nuban text NOT NULL CHECK (to_nuban ~ '^[0-9]{10}$'),
    amount_minor bigint NOT NULL CHECK (amount_minor > 0),
    narration text NOT NULL,
    status text NOT NULL CHECK (status = 'pending' OR status ~ '^[0-9]{2}$'),
    outcome text NOT NULL CHECK (outcome IN ('', 'credited', 'failed', 'unknown', 'reversed')),
    tsq_attempts integer NOT NULL DEFAULT 0 CHECK (tsq_attempts >= 0),
    next_tsq_at timestamptz,
    created_at timestamptz NOT NULL,
    resolved_at timestamptz,
    settlement_session text NOT NULL DEFAULT '',
    closure boolean NOT NULL DEFAULT false,
    CHECK ((status = 'pending') = (resolved_at IS NULL)),
    CHECK (settlement_session = '' OR status = '00')
);

CREATE INDEX nip_sessions_due ON nip_sessions (next_tsq_at, created_at, session_id) WHERE status = 'pending';
CREATE INDEX nip_sessions_unsettled ON nip_sessions (resolved_at, session_id) WHERE status = '00' AND settlement_session = '';

-- +goose Down
DROP TABLE nip_sessions;
