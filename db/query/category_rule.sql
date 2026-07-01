-- Auto-categorization rules (Story 7.2 / FR-17). A rule inherits its category's
-- kind, so ListCategoryRules joins category to return kind + name for the matcher
-- and the management UI, ordered by id (insertion order ⇒ first-match-wins).

-- name: CreateCategoryRule :one
INSERT INTO category_rule (match_text, category_id)
VALUES ($1, $2)
RETURNING id, match_text, category_id, created_at;

-- name: ListCategoryRules :many
SELECT r.id, r.match_text, r.category_id, c.name AS category_name, c.kind AS category_kind
FROM category_rule r
JOIN category c ON c.id = r.category_id
ORDER BY r.id;

-- name: DeleteCategoryRule :execrows
DELETE FROM category_rule WHERE id = $1;
