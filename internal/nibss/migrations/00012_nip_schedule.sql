-- +goose Up
CREATE TABLE nip_schedule (
    only_row boolean PRIMARY KEY DEFAULT true CHECK (only_row),
    sessions integer NOT NULL CHECK (sessions > 0)
);

-- +goose Down
DROP TABLE nip_schedule;
