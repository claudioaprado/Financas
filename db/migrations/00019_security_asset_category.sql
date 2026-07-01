-- +goose Up
-- Assign an owner-defined asset category to a security. Nullable (a security may
-- be uncategorized) and ON DELETE SET NULL, so deleting a category simply
-- unassigns it from its securities rather than blocking the delete.

-- +goose StatementBegin
ALTER TABLE security ADD COLUMN asset_category_id BIGINT REFERENCES asset_category (id) ON DELETE SET NULL;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX security_asset_category ON security (asset_category_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX security_asset_category;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE security DROP COLUMN asset_category_id;
-- +goose StatementEnd
