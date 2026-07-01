-- Monthly category budgets (Story 8.1 / FR-18). One target per category, keyed by
-- category_id (UNIQUE), so SetBudget upserts. ListBudgets joins category to return
-- its name + kind for the management view, ordered by kind then name (stable with
-- the categories list).

-- name: SetBudget :one
INSERT INTO budget (category_id, amount)
VALUES ($1, $2)
ON CONFLICT (category_id) DO UPDATE SET amount = EXCLUDED.amount
RETURNING id, category_id, amount, created_at;

-- name: ListBudgets :many
SELECT b.id, b.category_id, b.amount, c.name AS category_name, c.kind AS category_kind
FROM budget b
JOIN category c ON c.id = b.category_id
ORDER BY c.kind, c.name, c.id;

-- name: DeleteBudget :execrows
DELETE FROM budget WHERE category_id = $1;

-- name: ListCategorizedForBudget :many
-- Categorized income/expense transactions with occurred_on before an exclusive
-- upper bound (the first day of the month after the selected one), for the budget
-- view's actuals + carryover chain (Story 8.2 / FR-18). The native magnitude and
-- currency come from the money side of the row: income credits to_account,
-- expense debits from_account. The `type IN ('income','expense')` whitelist is the
-- guard that scopes this to budget spending: transfers and investment trades
-- (buy/sell/dividend) share this same transaction table under other type values
-- (migration 00010, AD-9), and the whitelist excludes them — do NOT loosen it to
-- `type != 'transfer'` or trades would leak into actuals. Ordered by date so the
-- domain sees a stable sequence.
SELECT
    t.category_id,
    t.type,
    t.occurred_on,
    CAST(CASE WHEN t.type = 'income' THEN t.to_amount ELSE t.from_amount END AS NUMERIC(19, 4)) AS amount,
    a.currency AS currency
FROM transaction t
JOIN account a
    ON a.id = CASE WHEN t.type = 'income' THEN t.to_account_id ELSE t.from_account_id END
WHERE t.category_id IS NOT NULL
    AND t.type IN ('income', 'expense')
    AND t.occurred_on < $1
ORDER BY t.occurred_on, t.id;
