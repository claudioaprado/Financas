-- name: GetDisplayCurrency :one
SELECT display_currency FROM app_settings WHERE id = TRUE;

-- name: SetDisplayCurrency :exec
UPDATE app_settings SET display_currency = $1 WHERE id = TRUE;
