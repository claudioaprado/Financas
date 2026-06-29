-- name: ListCurrencies :many
SELECT code, name FROM currency ORDER BY code;
