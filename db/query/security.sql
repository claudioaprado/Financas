-- name: CreateSecurity :one
INSERT INTO security (symbol, name, type, quote_currency)
VALUES ($1, $2, $3, $4)
RETURNING id, symbol, name, type, quote_currency, created_at;

-- name: GetSecurity :one
SELECT id, symbol, name, type, quote_currency, created_at FROM security WHERE id = $1;

-- name: GetSecurityBySymbol :one
SELECT id, symbol, name, type, quote_currency, created_at FROM security WHERE symbol = $1;

-- name: ListSecurities :many
SELECT id, symbol, name, type, quote_currency, created_at FROM security ORDER BY symbol, id;
