-- +goose Up
-- Investment transactions (FR-5): Buy/Sell/Dividend on the existing transaction
-- ledger (AD-9 one-row shape). A buy DEBITS the investment account's cash
-- (from_account, from_amount = quantity×price + fees); a sell CREDITS it
-- (to_account, to_amount = quantity×price − fees); a dividend CREDITS it
-- (to_account, to_amount = entered cash amount). Holdings (quantity, cost basis)
-- and realized gain are DERIVED on read (AD-2), never stored. Quantities at
-- NUMERIC(28,10), price/fees at money scale; non-negative magnitudes, direction
-- from from/to placement (AD-4). security_id is nullable (NULL for cash
-- income/expense/transfer; set for buy/sell/dividend).

-- +goose StatementBegin
ALTER TABLE transaction ADD COLUMN security_id BIGINT REFERENCES security (id);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE transaction ADD COLUMN quantity NUMERIC(28, 10) NOT NULL DEFAULT 0 CHECK (quantity >= 0);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE transaction ADD COLUMN price NUMERIC(19, 4) NOT NULL DEFAULT 0 CHECK (price >= 0);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE transaction ADD COLUMN fees NUMERIC(19, 4) NOT NULL DEFAULT 0 CHECK (fees >= 0);
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE transaction DROP CONSTRAINT IF EXISTS transaction_type_check;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE transaction ADD CONSTRAINT transaction_type_check
    CHECK (type IN ('income', 'expense', 'transfer', 'buy', 'sell', 'dividend'));
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX transaction_security ON transaction (security_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX transaction_security;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE transaction DROP CONSTRAINT IF EXISTS transaction_type_check;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE transaction ADD CONSTRAINT transaction_type_check
    CHECK (type IN ('income', 'expense', 'transfer'));
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE transaction DROP COLUMN fees;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE transaction DROP COLUMN price;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE transaction DROP COLUMN quantity;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE transaction DROP COLUMN security_id;
-- +goose StatementEnd
