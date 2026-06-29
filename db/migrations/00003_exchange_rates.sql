-- +goose Up
-- Owner-entered, effective-dated, directional exchange rates (AD-6). A row means
-- "1 unit of from_currency = rate units of to_currency, as of effective_date".
-- Append-only: corrections are new effective-dated rows; rates are NEVER inverted
-- in code (a missing direction prompts the owner). NUMERIC(18,8) per conventions.

-- +goose StatementBegin
CREATE TABLE exchange_rate (
    id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    from_currency  TEXT NOT NULL REFERENCES currency (code),
    to_currency    TEXT NOT NULL REFERENCES currency (code),
    effective_date DATE NOT NULL,
    rate           NUMERIC(18, 8) NOT NULL CHECK (rate > 0),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (from_currency <> to_currency)
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX exchange_rate_lookup
    ON exchange_rate (from_currency, to_currency, effective_date DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE exchange_rate;
-- +goose StatementEnd
