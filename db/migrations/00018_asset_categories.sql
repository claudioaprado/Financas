-- +goose Up
-- Asset categories: owner-defined classes for securities (e.g. "Ações BR",
-- "FIIs", "Renda Fixa", "Cripto"), managed independently of the fixed
-- security.type enum. This migration adds the authored table only; assigning a
-- category to a security is a later change. name is UNIQUE and length-capped to
-- match the service guard (internal/validate.MaxNameLen = 200); the service is
-- the authority, this UNIQUE/CHECK is the backstop.

-- +goose StatementBegin
CREATE TABLE asset_category (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE CHECK (name <> '' AND char_length(name) <= 200),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE asset_category;
-- +goose StatementEnd
