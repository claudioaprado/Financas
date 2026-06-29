-- +goose Up
-- The transaction ledger (FR-6) — the single source of truth from which account
-- balances, holdings, valuation, and net worth are DERIVED on read (AD-2). One
-- canonical row shape (AD-9): a transaction debits from_account and credits
-- to_account. Income credits an account (to-only); expense debits it (from-only);
-- transfers (Story 3.3) populate both sides; investment columns arrive in Epic 4.
-- Amounts are non-negative magnitudes at NUMERIC(19,4); direction comes from
-- placement chosen by `type`, never a raw sign (AD-4). Each leg's currency is its
-- account's native currency (AD-5) — no currency column.

-- +goose StatementBegin
CREATE TABLE transaction (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    type            TEXT NOT NULL CHECK (type IN ('income', 'expense')),
    from_account_id BIGINT REFERENCES account (id),
    to_account_id   BIGINT REFERENCES account (id),
    from_amount     NUMERIC(19, 4) NOT NULL DEFAULT 0 CHECK (from_amount >= 0),
    to_amount       NUMERIC(19, 4) NOT NULL DEFAULT 0 CHECK (to_amount >= 0),
    occurred_on     DATE NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (from_account_id IS NOT NULL OR to_account_id IS NOT NULL)
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX transaction_from_account ON transaction (from_account_id);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX transaction_to_account ON transaction (to_account_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE transaction;
-- +goose StatementEnd
