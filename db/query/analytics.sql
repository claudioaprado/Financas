-- Spending & cash-flow analytics (Story 8.3 / FR-19). Every income/expense
-- transaction in a date window, categorized OR NOT (uncategorized rows still
-- count toward cash-flow and group under the empty category name). The money side
-- picks the amount + account currency: income credits to_account, expense debits
-- from_account. The `type IN ('income','expense')` whitelist scopes this to the
-- spending universe: transfers and investment trades (buy/sell/dividend) share
-- this same transaction table under other type values (migration 00010, AD-9) and
-- are excluded by it — do NOT loosen it. Ordered by date for a stable sequence.

-- name: ListLedgerForAnalytics :many
SELECT
    t.type,
    t.occurred_on,
    CAST(CASE WHEN t.type = 'income' THEN t.to_amount ELSE t.from_amount END AS NUMERIC(19, 4)) AS amount,
    a.currency AS currency,
    c.name AS category_name
FROM transaction t
JOIN account a
    ON a.id = CASE WHEN t.type = 'income' THEN t.to_account_id ELSE t.from_account_id END
LEFT JOIN category c ON c.id = t.category_id
WHERE t.type IN ('income', 'expense')
    AND t.occurred_on >= $1
    AND t.occurred_on < $2
ORDER BY t.occurred_on, t.id;
