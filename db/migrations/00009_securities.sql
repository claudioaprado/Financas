-- +goose Up
-- Securities (FR-3): the instruments the owner trades — a symbol, display name,
-- type (stock | ETF | fund | other), and the currency the security is quoted in.
-- The symbol is stored normalized (trimmed + uppercased) and UNIQUE, so duplicate
-- symbols are prevented case-insensitively (the service is the authority; this
-- UNIQUE is the backstop). type is lowercase to match account.type. quote_currency
-- is a FK to currency(code), like account.currency (AD-5: stored natively).
-- Holdings, cost basis, prices, and valuation are NOT here — they are derived on
-- read / added by later Epic 4 stories (AD-2). bigint identity PK + timestamptz
-- created_at per conventions.

-- +goose StatementBegin
CREATE TABLE security (
    id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    symbol         TEXT NOT NULL CHECK (symbol <> '') UNIQUE,
    name           TEXT NOT NULL CHECK (name <> ''),
    type           TEXT NOT NULL CHECK (type IN ('stock', 'etf', 'fund', 'other')),
    quote_currency TEXT NOT NULL REFERENCES currency (code),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE security;
-- +goose StatementEnd
