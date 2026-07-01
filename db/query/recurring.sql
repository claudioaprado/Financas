-- Recurring transaction templates (Epic 9 / FR-20). One-row ledger convention:
-- income credits to_account_id, expense debits from_account_id, a transfer
-- populates both; to_amount is the cross-currency destination leg (0 otherwise).
-- next_due is the authored schedule cursor advanced on post/skip (UpdateRecurringNextDue).
-- GetRecurringForUpdate row-locks the template so a post/skip serializes against a
-- concurrent one (idempotent one-click posting — no double-post of an occurrence).

-- name: CreateRecurring :one
INSERT INTO recurring (
    type, from_account_id, to_account_id, amount, to_amount,
    category_id, cadence, interval_n, start_date, end_date, next_due, description
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
RETURNING id, type, from_account_id, to_account_id, amount, to_amount,
    category_id, cadence, interval_n, start_date, end_date, next_due, description, created_at;

-- name: GetRecurring :one
SELECT id, type, from_account_id, to_account_id, amount, to_amount,
    category_id, cadence, interval_n, start_date, end_date, next_due, description, created_at
FROM recurring WHERE id = $1;

-- name: GetRecurringForUpdate :one
SELECT id, type, from_account_id, to_account_id, amount, to_amount,
    category_id, cadence, interval_n, start_date, end_date, next_due, description, created_at
FROM recurring WHERE id = $1 FOR UPDATE;

-- name: ListRecurring :many
-- Every template with its account and category names joined for display, ordered
-- by next_due then id so the soonest-due surface first.
SELECT
    r.id, r.type, r.from_account_id, r.to_account_id, r.amount, r.to_amount,
    r.category_id, r.cadence, r.interval_n, r.start_date, r.end_date, r.next_due,
    r.description, r.created_at,
    fa.name AS from_account_name, fa.currency AS from_currency,
    ta.name AS to_account_name, ta.currency AS to_currency,
    c.name AS category_name, c.kind AS category_kind
FROM recurring r
LEFT JOIN account fa ON fa.id = r.from_account_id
LEFT JOIN account ta ON ta.id = r.to_account_id
LEFT JOIN category c ON c.id = r.category_id
ORDER BY r.next_due, r.id;

-- name: UpdateRecurring :execrows
UPDATE recurring SET
    type = $2, from_account_id = $3, to_account_id = $4, amount = $5, to_amount = $6,
    category_id = $7, cadence = $8, interval_n = $9, start_date = $10, end_date = $11,
    next_due = $12, description = $13
WHERE id = $1;

-- name: UpdateRecurringNextDue :execrows
UPDATE recurring SET next_due = $2 WHERE id = $1;

-- name: DeleteRecurring :execrows
DELETE FROM recurring WHERE id = $1;
