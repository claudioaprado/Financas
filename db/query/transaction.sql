-- name: CreateTransaction :one
INSERT INTO transaction (type, from_account_id, to_account_id, from_amount, to_amount, occurred_on, description, category_id, security_id, quantity, price, fees)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
RETURNING id, type, from_account_id, to_account_id, from_amount, to_amount, occurred_on, description, created_at, category_id, import_hash, security_id, quantity, price, fees, fitid, note;

-- name: UpdateTransaction :execrows
UPDATE transaction
SET type = $2, from_account_id = $3, to_account_id = $4, from_amount = $5, to_amount = $6, occurred_on = $7, description = $8, category_id = $9
WHERE id = $1;

-- name: DeleteTransaction :execrows
DELETE FROM transaction WHERE id = $1;

-- name: ListAccountTransactions :many
SELECT id, type, from_account_id, to_account_id, from_amount, to_amount, occurred_on, description, created_at, category_id, import_hash, security_id, quantity, price, fees, fitid, note
FROM transaction
WHERE from_account_id = $1 OR to_account_id = $1
ORDER BY occurred_on DESC, id DESC;

-- name: ListTransactions :many
SELECT id, type, from_account_id, to_account_id, from_amount, to_amount, occurred_on, description, created_at, category_id, import_hash, security_id, quantity, price, fees, fitid, note
FROM transaction
ORDER BY occurred_on DESC, id DESC;

-- name: CreateImportedTransaction :execrows
INSERT INTO transaction (type, from_account_id, to_account_id, from_amount, to_amount, occurred_on, description, import_hash, category_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: ListAccountImportHashes :many
SELECT import_hash
FROM transaction
WHERE import_hash IS NOT NULL AND (from_account_id = $1 OR to_account_id = $1);

-- name: CreateOFXTransaction :execrows
-- OFX import (FR-16): an Income/Expense row carrying the bank's FITID (or NULL
-- when the STMTTRN has none). category_id and import_hash default NULL — OFX
-- dedup is FITID-only, never the tab importer's content hash.
INSERT INTO transaction (type, from_account_id, to_account_id, from_amount, to_amount, occurred_on, description, fitid, category_id)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9);

-- name: ListAccountFitids :many
SELECT fitid
FROM transaction
WHERE fitid IS NOT NULL AND (from_account_id = $1 OR to_account_id = $1);

-- name: ListTransactionKindsByIDs :many
-- The (id, type) of the given transactions, for validating a bulk-categorize
-- selection (Story 10.1): every selected row must exist and be income/expense of
-- the category's kind. Transfers/trades are returned with their type so the
-- service can reject them (they are not categorizable, AD-9).
SELECT id, type FROM transaction WHERE id = ANY($1::bigint[]);

-- name: BulkSetCategory :execrows
-- Assign one category to many transactions in a single statement (Story 10.1,
-- one tx AD-3). The type guard scopes the write to rows matching the category's
-- kind, so a transfer/trade id can never be categorized even if submitted.
UPDATE transaction SET category_id = $1 WHERE id = ANY($2::bigint[]) AND type = $3;

-- Notes & tags (Story 10.2 / FR-21). Presentation metadata only.

-- name: GetTransaction :one
SELECT id, type, from_account_id, to_account_id, from_amount, to_amount, occurred_on, description, created_at, category_id, import_hash, security_id, quantity, price, fees, fitid, note
FROM transaction WHERE id = $1;

-- name: SetTransactionNote :execrows
UPDATE transaction SET note = $2 WHERE id = $1;

-- name: UpsertTag :one
-- Create-on-use: return the tag whether it already existed or is newly inserted.
INSERT INTO tag (name) VALUES ($1)
ON CONFLICT (name) DO UPDATE SET name = EXCLUDED.name
RETURNING id, name, created_at;

-- name: AddTransactionTag :exec
INSERT INTO transaction_tag (transaction_id, tag_id)
VALUES ($1, $2)
ON CONFLICT DO NOTHING;

-- name: DeleteTransactionTags :exec
-- Clear a transaction's tag links (the annotate use-case replaces the whole set).
DELETE FROM transaction_tag WHERE transaction_id = $1;

-- name: ListTransactionTags :many
-- One transaction's tag names, alphabetical (detail/edit view).
SELECT t.id, t.name
FROM tag t
JOIN transaction_tag tt ON tt.tag_id = t.id
WHERE tt.transaction_id = $1
ORDER BY t.name;

-- name: ListTagsForTransactions :many
-- Tag names for many transactions at once (register/detail display), so the
-- service maps them without an N+1 per row.
SELECT tt.transaction_id, t.name
FROM transaction_tag tt
JOIN tag t ON t.id = tt.tag_id
WHERE tt.transaction_id = ANY($1::bigint[])
ORDER BY tt.transaction_id, t.name;
