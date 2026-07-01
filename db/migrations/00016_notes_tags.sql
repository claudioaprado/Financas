-- +goose Up
-- Notes & tags on transactions (FR-21, Story 10.2): presentation metadata the
-- owner attaches to a Transaction to annotate and group beyond a single Category.
-- They are AUTHORED but purely descriptive — they NEVER affect balances, budgets,
-- or valuation (AD-2, derived figures ignore them). `note` is a free-text column
-- on the ledger (empty by default). Tags are reusable labels (create-on-use,
-- UNIQUE name); transaction_tag is the many-to-many join. Both FKs cascade so
-- deleting a transaction or a tag cleans up its links.

-- +goose StatementBegin
ALTER TABLE transaction ADD COLUMN note TEXT NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE tag (
    id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE CHECK (name <> ''),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TABLE transaction_tag (
    transaction_id BIGINT NOT NULL REFERENCES transaction (id) ON DELETE CASCADE,
    tag_id         BIGINT NOT NULL REFERENCES tag (id) ON DELETE CASCADE,
    PRIMARY KEY (transaction_id, tag_id)
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX transaction_tag_tag ON transaction_tag (tag_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE transaction_tag;
-- +goose StatementEnd

-- +goose StatementBegin
DROP TABLE tag;
-- +goose StatementEnd

-- +goose StatementBegin
ALTER TABLE transaction DROP COLUMN note;
-- +goose StatementEnd
