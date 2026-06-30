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
