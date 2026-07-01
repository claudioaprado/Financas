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
       description, created_at, category_id, import_hash, security_id, quantity, price, fees, fitid, note
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
                         security_id, quantity, price, fees, fitid, note)
OVERRIDING SYSTEM VALUE
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17);

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

-- Phase-2 authored tables (Epics 8-10) added to the backup round-trip: budgets,
-- auto-categorization rules, recurring templates, and tags + their links. Each
-- SELECT lists exactly its table's columns (model-struct order) so sqlc returns
-- the existing store models; restore deletes them child→parent and re-inserts
-- parent→child with original PKs (identity insert), then resets the sequences.

-- name: ExportBudgets :many
SELECT id, category_id, amount, created_at FROM budget ORDER BY id;

-- name: ExportCategoryRules :many
SELECT id, match_text, category_id, created_at FROM category_rule ORDER BY id;

-- name: ExportRecurring :many
SELECT id, type, from_account_id, to_account_id, amount, to_amount, category_id,
       cadence, interval_n, start_date, end_date, next_due, description, created_at
FROM recurring ORDER BY id;

-- name: ExportTags :many
SELECT id, name, created_at FROM tag ORDER BY id;

-- name: ExportTransactionTags :many
SELECT transaction_id, tag_id FROM transaction_tag ORDER BY transaction_id, tag_id;

-- name: RestoreDeleteTransactionTags :exec
DELETE FROM transaction_tag;

-- name: RestoreDeleteTags :exec
DELETE FROM tag;

-- name: RestoreDeleteBudgets :exec
DELETE FROM budget;

-- name: RestoreDeleteCategoryRules :exec
DELETE FROM category_rule;

-- name: RestoreDeleteRecurring :exec
DELETE FROM recurring;

-- name: RestoreInsertBudget :exec
INSERT INTO budget (id, category_id, amount, created_at)
OVERRIDING SYSTEM VALUE
VALUES ($1, $2, $3, $4);

-- name: RestoreInsertCategoryRule :exec
INSERT INTO category_rule (id, match_text, category_id, created_at)
OVERRIDING SYSTEM VALUE
VALUES ($1, $2, $3, $4);

-- name: RestoreInsertRecurring :exec
INSERT INTO recurring (id, type, from_account_id, to_account_id, amount, to_amount, category_id,
                       cadence, interval_n, start_date, end_date, next_due, description, created_at)
OVERRIDING SYSTEM VALUE
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14);

-- name: RestoreInsertTag :exec
INSERT INTO tag (id, name, created_at)
OVERRIDING SYSTEM VALUE
VALUES ($1, $2, $3);

-- name: RestoreInsertTransactionTag :exec
INSERT INTO transaction_tag (transaction_id, tag_id)
VALUES ($1, $2);

-- name: RestoreResetBudgetSeq :exec
SELECT setval(pg_get_serial_sequence('budget', 'id'),
              COALESCE((SELECT MAX(id) FROM budget), 1),
              (SELECT MAX(id) FROM budget) IS NOT NULL);

-- name: RestoreResetCategoryRuleSeq :exec
SELECT setval(pg_get_serial_sequence('category_rule', 'id'),
              COALESCE((SELECT MAX(id) FROM category_rule), 1),
              (SELECT MAX(id) FROM category_rule) IS NOT NULL);

-- name: RestoreResetRecurringSeq :exec
SELECT setval(pg_get_serial_sequence('recurring', 'id'),
              COALESCE((SELECT MAX(id) FROM recurring), 1),
              (SELECT MAX(id) FROM recurring) IS NOT NULL);

-- name: RestoreResetTagSeq :exec
SELECT setval(pg_get_serial_sequence('tag', 'id'),
              COALESCE((SELECT MAX(id) FROM tag), 1),
              (SELECT MAX(id) FROM tag) IS NOT NULL);
