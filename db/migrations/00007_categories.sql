-- +goose Up
-- Categories (FR-7): income- or expense-typed labels the owner assigns to
-- income/expense transactions. The kind rule (income category only on Income,
-- expense only on Expense) is enforced in the service — there is no transaction
-- type column for the DB to check. category_id is nullable: income/expense may
-- be uncategorized, and transfers are always NULL.

-- +goose StatementBegin
CREATE TABLE category (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name       TEXT NOT NULL CHECK (name <> ''),
    kind       TEXT NOT NULL CHECK (kind IN ('income', 'expense')),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE transaction ADD COLUMN category_id BIGINT REFERENCES category (id);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX transaction_category ON transaction (category_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX transaction_category;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE transaction DROP COLUMN category_id;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE category;
-- +goose StatementEnd
