-- +goose Up
CREATE SEQUENCE bvn_seq MINVALUE 1 MAXVALUE 9999999 NO CYCLE;

CREATE TABLE bvns (
    bvn text PRIMARY KEY CHECK (bvn ~ '^[0-9]{11}$'),
    name text NOT NULL CHECK (name <> ''),
    phone text NOT NULL CHECK (phone ~ '^[0-9]+$'),
    dob date NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (name, phone, dob)
);

-- +goose Down
DROP TABLE bvns;
DROP SEQUENCE bvn_seq;
