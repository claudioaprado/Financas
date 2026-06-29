-- name: CreateAccount :one
INSERT INTO account (name, type, currency)
VALUES ($1, $2, $3)
RETURNING id, name, type, currency, archived, created_at;

-- name: GetAccount :one
SELECT id, name, type, currency, archived, created_at
FROM account
WHERE id = $1;

-- name: RenameAccount :execrows
UPDATE account SET name = $2 WHERE id = $1;

-- name: SetAccountArchived :execrows
UPDATE account SET archived = $2 WHERE id = $1;

-- name: ListActiveAccounts :many
SELECT id, name, type, currency, archived, created_at
FROM account
WHERE NOT archived
ORDER BY name, id;

-- name: ListAllAccounts :many
SELECT id, name, type, currency, archived, created_at
FROM account
ORDER BY archived, name, id;
