-- +goose Up
CREATE TABLE IF NOT EXISTS nip_answers (
    session_id text PRIMARY KEY CHECK (session_id ~ '^[0-9]{30}$'),
    answer text NOT NULL CHECK (answer IN ('credited', 'refused')),
    response_code text NOT NULL CHECK (response_code ~ '^[0-9]{2}$'),
    request jsonb,
    answered_at timestamptz NOT NULL,
    CHECK ((answer = 'credited') = (response_code = '00'))
);

-- +goose Down
DROP TABLE IF EXISTS nip_answers;
