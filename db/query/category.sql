-- name: CreateCategory :one
INSERT INTO category (name, kind)
VALUES ($1, $2)
RETURNING id, name, kind, created_at;

-- name: GetCategory :one
SELECT id, name, kind, created_at FROM category WHERE id = $1;

-- name: ListCategories :many
SELECT id, name, kind, created_at FROM category ORDER BY kind, name, id;

-- name: DeleteCategory :execrows
DELETE FROM category WHERE id = $1;

-- name: ClearCategoryFromTransactions :execrows
UPDATE transaction SET category_id = NULL WHERE category_id = $1;

-- name: CategoryUsageCounts :many
SELECT category_id, COUNT(*) AS n
FROM transaction
WHERE category_id IS NOT NULL
GROUP BY category_id;

-- name: ListCategoryTransactions :many
SELECT id, type, from_account_id, to_account_id, from_amount, to_amount, occurred_on, description, created_at, category_id, import_hash, security_id, quantity, price, fees
FROM transaction
WHERE category_id = $1
ORDER BY occurred_on DESC, id DESC;
