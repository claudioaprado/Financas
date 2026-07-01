-- +goose Up
-- OFX-import idempotency (FR-16): the bank's per-transaction id (FITID). Dedup is
-- FITID-ONLY and scoped to the owning account — a bank's FITID is unique only
-- within its own statement, so two accounts may legitimately share one. An
-- imported Income sets to_account, an Expense sets from_account (AD-9), so exactly
-- one side is set and COALESCE(from,to) is the deterministic owning account.
-- Manually-entered rows and transfers keep fitid NULL and are ignored by the
-- partial unique index. Content dedup by (date, description, value) is FORBIDDEN.

-- +goose StatementBegin
ALTER TABLE transaction ADD COLUMN fitid TEXT;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE UNIQUE INDEX transaction_account_fitid
    ON transaction (COALESCE(from_account_id, to_account_id), fitid)
    WHERE fitid IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX transaction_account_fitid;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE transaction DROP COLUMN fitid;
-- +goose StatementEnd
