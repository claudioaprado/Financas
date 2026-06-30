-- Full-row export queries for Story 6.1 (authored-data backup). Each SELECT
-- lists exactly its table's columns and ORDERs BY id, so sqlc returns the
-- existing model structs (store.Account/Category/Security/ExchangeRate/Price/
-- Transaction) at full fidelity — primary key, all authored columns, and
-- created_at — for a deterministic, byte-stable snapshot that restore (6.2)
-- can re-insert parents-before-children.

-- name: ExportAccounts :many
SELECT id, name, type, currency, archived, created_at FROM account ORDER BY id;

-- name: ExportCategories :many
SELECT id, name, kind, created_at FROM category ORDER BY id;

-- name: ExportSecurities :many
SELECT id, symbol, name, type, quote_currency, created_at FROM security ORDER BY id;

-- name: ExportExchangeRates :many
SELECT id, from_currency, to_currency, effective_date, rate, created_at FROM exchange_rate ORDER BY id;

-- name: ExportPrices :many
SELECT id, security_id, effective_date, price, created_at FROM price ORDER BY id;

-- name: ExportTransactions :many
SELECT id, type, from_account_id, to_account_id, from_amount, to_amount, occurred_on,
       description, created_at, category_id, import_hash, security_id, quantity, price, fees
FROM transaction ORDER BY id;

-- Restore queries for Story 6.2 (recover from a 6.1 export). Restore is a
-- replace-all operation inside one transaction (AD-3): delete every authored row
-- child→parent, re-insert each row parent→child with its ORIGINAL primary key
-- and created_at (OVERRIDING SYSTEM VALUE — the PKs are GENERATED ALWAYS AS
-- IDENTITY), then reset each identity sequence past the restored ids. Derived
-- figures are never written; they recompute on read (AD-2).

-- name: RestoreDeleteTransactions :exec
DELETE FROM transaction;

-- name: RestoreDeletePrices :exec
DELETE FROM price;

-- name: RestoreDeleteExchangeRates :exec
DELETE FROM exchange_rate;

-- name: RestoreDeleteSecurities :exec
DELETE FROM security;

-- name: RestoreDeleteCategories :exec
DELETE FROM category;

-- name: RestoreDeleteAccounts :exec
DELETE FROM account;

-- name: RestoreInsertAccount :exec
INSERT INTO account (id, name, type, currency, archived, created_at)
OVERRIDING SYSTEM VALUE
VALUES ($1, $2, $3, $4, $5, $6);

-- name: RestoreInsertCategory :exec
INSERT INTO category (id, name, kind, created_at)
OVERRIDING SYSTEM VALUE
VALUES ($1, $2, $3, $4);

-- name: RestoreInsertSecurity :exec
INSERT INTO security (id, symbol, name, type, quote_currency, created_at)
OVERRIDING SYSTEM VALUE
VALUES ($1, $2, $3, $4, $5, $6);

-- name: RestoreInsertExchangeRate :exec
INSERT INTO exchange_rate (id, from_currency, to_currency, effective_date, rate, created_at)
OVERRIDING SYSTEM VALUE
VALUES ($1, $2, $3, $4, $5, $6);

-- name: RestoreInsertPrice :exec
INSERT INTO price (id, security_id, effective_date, price, created_at)
OVERRIDING SYSTEM VALUE
VALUES ($1, $2, $3, $4, $5);

-- name: RestoreInsertTransaction :exec
INSERT INTO transaction (id, type, from_account_id, to_account_id, from_amount, to_amount,
                         occurred_on, description, created_at, category_id, import_hash,
                         security_id, quantity, price, fees)
OVERRIDING SYSTEM VALUE
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15);

-- name: RestoreResetAccountSeq :exec
SELECT setval(pg_get_serial_sequence('account', 'id'),
              COALESCE((SELECT MAX(id) FROM account), 1),
              (SELECT MAX(id) FROM account) IS NOT NULL);

-- name: RestoreResetCategorySeq :exec
SELECT setval(pg_get_serial_sequence('category', 'id'),
              COALESCE((SELECT MAX(id) FROM category), 1),
              (SELECT MAX(id) FROM category) IS NOT NULL);

-- name: RestoreResetSecuritySeq :exec
SELECT setval(pg_get_serial_sequence('security', 'id'),
              COALESCE((SELECT MAX(id) FROM security), 1),
              (SELECT MAX(id) FROM security) IS NOT NULL);

-- name: RestoreResetExchangeRateSeq :exec
SELECT setval(pg_get_serial_sequence('exchange_rate', 'id'),
              COALESCE((SELECT MAX(id) FROM exchange_rate), 1),
              (SELECT MAX(id) FROM exchange_rate) IS NOT NULL);

-- name: RestoreResetPriceSeq :exec
SELECT setval(pg_get_serial_sequence('price', 'id'),
              COALESCE((SELECT MAX(id) FROM price), 1),
              (SELECT MAX(id) FROM price) IS NOT NULL);

-- name: RestoreResetTransactionSeq :exec
SELECT setval(pg_get_serial_sequence('transaction', 'id'),
              COALESCE((SELECT MAX(id) FROM transaction), 1),
              (SELECT MAX(id) FROM transaction) IS NOT NULL);
