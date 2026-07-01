-- name: CreateSecurity :one
INSERT INTO security (symbol, name, type, quote_currency, asset_category_id)
VALUES ($1, $2, $3, $4, $5)
RETURNING id, symbol, name, type, quote_currency, created_at, asset_category_id;

-- name: GetSecurity :one
SELECT id, symbol, name, type, quote_currency, created_at, asset_category_id FROM security WHERE id = $1;

-- name: GetSecurityBySymbol :one
SELECT id, symbol, name, type, quote_currency, created_at, asset_category_id FROM security WHERE symbol = $1;

-- name: ListSecurities :many
SELECT id, symbol, name, type, quote_currency, created_at, asset_category_id FROM security ORDER BY symbol, id;

-- name: SetSecurityCategory :execrows
UPDATE security SET asset_category_id = $2 WHERE id = $1;
