-- +goose Up
-- File-import idempotency (FR-13): a per-row natural-key hash of
-- (account_id, date, description, value). Manually-entered transactions keep
-- import_hash NULL (the partial unique index ignores them); imported rows store
-- the hash so re-importing the same file inserts nothing new.

-- +goose StatementBegin
ALTER TABLE transaction ADD COLUMN import_hash TEXT;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE UNIQUE INDEX transaction_import_hash
    ON transaction (import_hash)
    WHERE import_hash IS NOT NULL;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX transaction_import_hash;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE transaction DROP COLUMN import_hash;
-- +goose StatementEnd
