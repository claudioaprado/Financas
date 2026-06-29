-- +goose Up
-- Initial migration: establishes the goose version baseline. Application/domain
-- schema arrives in Epic 2 (currencies, accounts, …); this intentionally
-- creates no application tables. Its purpose is to prove the on-startup runner
-- and make the embedded migrations glob non-empty.
-- +goose StatementBegin
DO $$ BEGIN END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN END $$;
-- +goose StatementEnd
