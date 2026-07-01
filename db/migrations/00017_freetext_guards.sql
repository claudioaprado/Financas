-- +goose Up
-- Free-text backstops for the authoring tables (deferred from the Epic 4.1
-- review). The services stay the validation authority (internal/validate); these
-- DB constraints are the independent backstop for any non-service write path
-- (e.g. a restore from export, backup.RestoreInsertSecurity).
--
-- 1) security.symbol becomes case-insensitively UNIQUE at the DB level. The old
--    byte-exact UNIQUE only held because service/security.Create upper-cases
--    before insert; a raw insert (restore) could smuggle a case-collision. A
--    functional UNIQUE index on upper(symbol) enforces it regardless of casing.
-- 2) Length CHECK caps on the free-text columns match the service limits
--    (validate.MaxNameLen = 200, MaxSymbolLen = 32), bounding a runaway TEXT
--    value from a non-service path. Symbol also gets a no-whitespace CHECK,
--    mirroring validate.Symbol.

-- +goose StatementBegin
ALTER TABLE security DROP CONSTRAINT security_symbol_key;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE UNIQUE INDEX security_symbol_upper_key ON security (upper(symbol));
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE security
    ADD CONSTRAINT security_symbol_len CHECK (char_length(symbol) <= 32),
    ADD CONSTRAINT security_symbol_no_space CHECK (symbol !~ '\s'),
    ADD CONSTRAINT security_name_len CHECK (char_length(name) <= 200);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE account
    ADD CONSTRAINT account_name_len CHECK (char_length(name) <= 200);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE category
    ADD CONSTRAINT category_name_len CHECK (char_length(name) <= 200);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE category DROP CONSTRAINT category_name_len;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE account DROP CONSTRAINT account_name_len;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE security
    DROP CONSTRAINT security_symbol_len,
    DROP CONSTRAINT security_symbol_no_space,
    DROP CONSTRAINT security_name_len;
-- +goose StatementEnd

-- +goose StatementBegin
DROP INDEX security_symbol_upper_key;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE security ADD CONSTRAINT security_symbol_key UNIQUE (symbol);
-- +goose StatementEnd
