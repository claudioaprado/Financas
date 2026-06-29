-- name: AddPrice :one
INSERT INTO price (security_id, effective_date, price)
VALUES ($1, $2, $3)
RETURNING id, security_id, effective_date, price, created_at;

-- name: PriceEffectiveAt :one
SELECT price FROM price
WHERE security_id = $1 AND effective_date <= $2
ORDER BY effective_date DESC, id DESC
LIMIT 1;

-- name: LatestPrices :many
-- $1 is cast to a calendar DATE so "effective on or before today" is a stable
-- date-to-date comparison (no sub-day/timezone boundary flapping when an instant
-- is passed).
SELECT DISTINCT ON (security_id) security_id, effective_date, price
FROM price
WHERE effective_date <= $1::date
ORDER BY security_id, effective_date DESC, id DESC;

-- name: ListPrices :many
SELECT p.id, p.security_id, s.symbol, s.quote_currency, p.effective_date, p.price, p.created_at
FROM price p
JOIN security s ON s.id = p.security_id
ORDER BY p.effective_date DESC, p.id DESC;
