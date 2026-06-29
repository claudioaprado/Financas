-- name: AddExchangeRate :one
INSERT INTO exchange_rate (from_currency, to_currency, effective_date, rate)
VALUES ($1, $2, $3, $4)
RETURNING id, from_currency, to_currency, effective_date, rate, created_at;

-- name: RateEffectiveAt :one
SELECT rate FROM exchange_rate
WHERE from_currency = $1 AND to_currency = $2 AND effective_date <= $3
ORDER BY effective_date DESC, id DESC
LIMIT 1;

-- name: ListExchangeRates :many
SELECT id, from_currency, to_currency, effective_date, rate, created_at
FROM exchange_rate
ORDER BY effective_date DESC, id DESC;
