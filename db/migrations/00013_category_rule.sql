-- +goose Up
-- Auto-categorization rules (FR-17): a global "description contains match_text →
-- category" rule. A rule inherits its category's kind (income/expense), so it is
-- only suggested on matching-type rows. Rules only SUGGEST in the import preview;
-- nothing auto-commits. Deleting a category cascades its rules away.

-- +goose StatementBegin
CREATE TABLE category_rule (
    id          BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    match_text  TEXT NOT NULL,
    category_id BIGINT NOT NULL REFERENCES category (id) ON DELETE CASCADE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- +goose StatementEnd

-- +goose StatementBegin
CREATE INDEX category_rule_category ON category_rule (category_id);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE category_rule;
-- +goose StatementEnd
