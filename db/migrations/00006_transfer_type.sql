-- +goose Up
-- Widen the transaction type to allow transfers (Story 3.3). A transfer is one
-- row populating BOTH from_account and to_account (AD-9); the ledger already has
-- those columns (00005), so this only relaxes the type CHECK.

-- +goose StatementBegin
ALTER TABLE transaction DROP CONSTRAINT IF EXISTS transaction_type_check;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE transaction ADD CONSTRAINT transaction_type_check
    CHECK (type IN ('income', 'expense', 'transfer'));
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE transaction DROP CONSTRAINT IF EXISTS transaction_type_check;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE transaction ADD CONSTRAINT transaction_type_check
    CHECK (type IN ('income', 'expense'));
-- +goose StatementEnd
