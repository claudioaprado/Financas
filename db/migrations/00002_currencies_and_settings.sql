-- +goose Up
-- First domain schema: the supported currencies (ISO-4217) and the single-owner
-- application settings (the chosen Display Currency). The Display Currency is a
-- presentation choice only and never alters stored amounts (AD-5).

-- +goose StatementBegin
CREATE TABLE currency (
    code TEXT PRIMARY KEY,
    name TEXT NOT NULL
);
-- +goose StatementEnd

-- +goose StatementBegin
INSERT INTO currency (code, name) VALUES
    ('USD', 'US Dollar'),
    ('BRL', 'Brazilian Real');
-- +goose StatementEnd

-- +goose StatementBegin
-- Single-row settings table: the BOOLEAN PK with CHECK (id) allows exactly one
-- row (TRUE), encoding single-owner without a tenant column (AD-7).
CREATE TABLE app_settings (
    id BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id),
    display_currency TEXT NOT NULL REFERENCES currency (code)
);
-- +goose StatementEnd

-- +goose StatementBegin
INSERT INTO app_settings (id, display_currency) VALUES (TRUE, 'USD');
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE app_settings;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE currency;
-- +goose StatementEnd
