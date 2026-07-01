-- +goose Up
-- Monthly category budgets (FR-18, Story 8.1): the current monthly target amount
-- per category, in the Display Currency. One target per category (UNIQUE), so a
-- category either has a budget or is "no budget". The amount is decimal
-- NUMERIC(19,4) (AD-4/NFR-5). Carryover and actual-vs-planned are DERIVED on read
-- (AD-2/AD-10) from the ledger + this target — never stored here. Deleting a
-- category cascades its budget away.

-- +goose StatementBegin
CREATE TABLE budget (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    category_id BIGINT NOT NULL UNIQUE REFERENCES category (id) ON DELETE CASCADE,
    amount      NUMERIC(19, 4) NOT NULL CHECK (amount > 0),
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE budget;
-- +goose StatementEnd
