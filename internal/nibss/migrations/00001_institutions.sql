-- +goose Up
CREATE TABLE institutions (
    id text PRIMARY KEY,
    name text NOT NULL,
    nip_code text NOT NULL UNIQUE CHECK (nip_code ~ '^[0-9]{6}$'),
    nuban_code text NOT NULL CHECK (nuban_code ~ '^([0-9]{3}|[0-9]{6})$'),
    type text NOT NULL,
    outside boolean NOT NULL,
    status text NOT NULL CHECK (status IN ('active', 'suspended', 'terminating', 'removed')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE institutions;
