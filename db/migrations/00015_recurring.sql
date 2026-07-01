-- +goose Up
-- Recurring transaction templates (FR-20, Epic 9): the owner defines a repeating
-- income/expense/transfer once and posts each due occurrence with one click — no
-- background scheduler (remind + confirm), so this fits the single-container
-- architecture. The template is AUTHORED state (it is not derived): `next_due` is
-- the one authored schedule cursor, advanced when the owner posts or skips an
-- occurrence. Whether a template is "due" (next_due <= today, within end_date) is
-- derived on read (AD-2/AD-10); posting MATERIALIZES a real transaction row
-- (AD-9: a transfer is one two-account row) — the ledger stays the single source
-- of truth. Amounts are decimal NUMERIC(19,4) (AD-4/NFR-5).
--
-- Column shape mirrors the ledger's one-row convention (00005/00007): income
-- credits to_account_id, expense debits from_account_id, a transfer populates
-- both. `to_amount` carries the destination leg of a cross-currency transfer
-- (0 ⇒ same-currency, resolved to `amount` at post time, like Transfer). The
-- category kind rule (income category on income only, etc.) and the two-account
-- transfer rule are enforced in the service (AD-1), as elsewhere.

-- +goose StatementBegin
CREATE TABLE recurring (
    id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    type            TEXT NOT NULL CHECK (type IN ('income', 'expense', 'transfer')),
    from_account_id BIGINT REFERENCES account (id),
    to_account_id   BIGINT REFERENCES account (id),
    amount          NUMERIC(19, 4) NOT NULL CHECK (amount > 0),
    to_amount       NUMERIC(19, 4) NOT NULL DEFAULT 0 CHECK (to_amount >= 0),
    category_id     BIGINT REFERENCES category (id),
    cadence         TEXT NOT NULL CHECK (cadence IN ('weeks', 'months', 'years')),
    interval_n      INTEGER NOT NULL CHECK (interval_n > 0),
    start_date      DATE NOT NULL,
    end_date        DATE,
    next_due        DATE NOT NULL,
    description     TEXT NOT NULL DEFAULT '',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK (from_account_id IS NOT NULL OR to_account_id IS NOT NULL),
    CHECK (end_date IS NULL OR end_date >= start_date)
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX recurring_next_due ON recurring (next_due);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE recurring;
-- +goose StatementEnd
