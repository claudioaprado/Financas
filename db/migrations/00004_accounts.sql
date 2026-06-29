-- +goose Up
-- The owner's Accounts (FR-1): cash, credit, or investment, each in its own base
-- currency. Single-owner means NO tenant column (AD-7). NO balance column (AD-2):
-- account balances/Net Worth are DERIVED from the transaction ledger on read
-- (Epic 3/4), never stored here. Archiving preserves history but excludes the
-- account from default views and from current Net Worth (conventions). bigint
-- identity PK + timestamptz created_at per conventions.

-- +goose StatementBegin
CREATE TABLE account (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name       TEXT NOT NULL CHECK (name <> ''),
    type       TEXT NOT NULL CHECK (type IN ('cash', 'credit', 'investment')),
    currency   TEXT NOT NULL REFERENCES currency (code),
    archived   BOOLEAN NOT NULL DEFAULT FALSE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE account;
-- +goose StatementEnd
