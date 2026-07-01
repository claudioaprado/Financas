-- name: CreateAssetCategory :one
INSERT INTO asset_category (name)
VALUES ($1)
RETURNING id, name, created_at;

-- name: GetAssetCategory :one
SELECT id, name, created_at FROM asset_category WHERE id = $1;

-- name: ListAssetCategories :many
SELECT id, name, created_at FROM asset_category ORDER BY name, id;

-- name: RenameAssetCategory :execrows
UPDATE asset_category SET name = $2 WHERE id = $1;

-- name: DeleteAssetCategory :execrows
DELETE FROM asset_category WHERE id = $1;
