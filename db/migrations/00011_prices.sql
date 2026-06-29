-- +goose Up
-- Owner-entered, effective-dated security prices (AD-6) — the per-security analog
-- of exchange_rate. A row means "1 unit of security_id was worth price units of
-- the security's quote currency, as of effective_date". Append-only: corrections
-- are new effective-dated rows; the most recent (<= today) is used for valuation.
-- There is NO online/automated/real-time price feed. NUMERIC(19,4) per the money/
-- Price scale convention (distinct from the (18,8) exchange-rate scale).

-- +goose StatementBegin
CREATE TABLE price (
    id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    security_id    BIGINT NOT NULL REFERENCES security (id),
    effective_date DATE NOT NULL,
    price          NUMERIC(19, 4) NOT NULL CHECK (price > 0),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX price_security_effective
    ON price (security_id, effective_date DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE price;
-- +goose StatementEnd
