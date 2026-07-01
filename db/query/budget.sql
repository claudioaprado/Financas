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
