-- name: CreateTransaction :one
INSERT INTO transaction (type, from_account_id, to_account_id, from_amount, to_amount, occurred_on, description, category_id, security_id, quantity, price, fees)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
RETURNING id, type, from_account_id, to_account_id, from_amount, to_amount, occurred_on, description, created_at, category_id, import_hash, security_id, quantity, price, fees, fitid;

-- name: UpdateTransaction :execrows
UPDATE transaction
SET type = $2, from_account_id = $3, to_account_id = $4, from_amount = $5, to_amount = $6, occurred_on = $7, description = $8, category_id = $9
WHERE id = $1;

-- name: DeleteTransaction :execrows
DELETE FROM transaction WHERE id = $1;

-- name: ListAccountTransactions :many
SELECT id, type, from_account_id, to_account_id, from_amount, to_amount, occurred_on, description, created_at, category_id, import_hash, security_id, quantity, price, fees, fitid
FROM transaction
WHERE from_account_id = $1 OR to_account_id = $1
ORDER BY occurred_on DESC, id DESC;

-- name: ListTransactions :many
SELECT id, type, from_account_id, to_account_id, from_amount, to_amount, occurred_on, description, created_at, category_id, import_hash, security_id, quantity, price, fees, fitid
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
