---
baseline_commit: eeae658
---

# Story 3.4: Categories & classification

Status: review

<!-- Note: Validation is optional. Run validate-create-story for quality check before dev-story. -->

## Story

As the owner,
I want to classify income and expenses with categories,
so that I understand where money goes.

## Acceptance Criteria

From `epics.md` → Epic 3 → Story 3.4 (realizes FR-7). **Given** I am authenticated, **When** I create a Category (income-type or expense-type) and assign it to an Income/Expense Transaction, **Then**:

1. An **income-type Category attaches only to Income** and an **expense-type only to Expense** (FR-7) — assigning a mismatched kind is rejected. Transfers carry no category.
2. I can **filter and sum** Transactions by Category (a per-category view of its transactions with a total per currency).
3. A Category **in use cannot be deleted** without reassigning or confirming its Transactions (guarded delete: refused while referenced, with an explicit "delete and unassign" confirmation that clears the category from those transactions first).

> Scope: this story adds the `category` entity, a `category_id` on income/expense transactions, the kind-matching rule, a categories page (create / list-with-usage / guarded delete), and a per-category summary (filter + sum **per currency**). Cross-currency Display-Currency category totals are Epic 5 (no conversion here). The cross-account register/filter UI is Story 3.5; this story's "filter" is the per-category transactions view.

## Tasks / Subtasks

- [x] **Task 1 — `category` schema + `transaction.category_id` (AC: #1)**
  - [x] Add goose migration `db/migrations/00007_categories.sql`. Up:
    ```sql
    CREATE TABLE category (
        id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
        name       TEXT NOT NULL CHECK (name <> ''),
        kind       TEXT NOT NULL CHECK (kind IN ('income', 'expense')),
        created_at TIMESTAMPTZ NOT NULL DEFAULT now()
    );
    ALTER TABLE transaction ADD COLUMN category_id BIGINT REFERENCES category (id);
    CREATE INDEX transaction_category ON transaction (category_id);
    ```
    Down: drop the index, drop the column, drop the table (reverse order). `-- +goose StatementBegin/End` per existing style.
  - [x] `category_id` is nullable (income/expense may be uncategorized; transfers are always NULL). The kind-matching rule (income category ↔ Income only) is enforced in the **service** (the authority), not the DB — there is no transaction-type column the FK can check.

- [x] **Task 2 — sqlc: category queries + thread `category_id` through transactions (AC: #1, #2, #3)**
  - [x] Add `db/query/category.sql`: `CreateCategory :one` (name, kind RETURNING all), `GetCategory :one` (id), `ListCategories :many` (ORDER BY kind, name), `DeleteCategory :execrows` (id), `ClearCategoryFromTransactions :execrows` (`UPDATE transaction SET category_id = NULL WHERE category_id = $1`), `CategoryUsageCounts :many` (`SELECT category_id, COUNT(*) AS n FROM transaction WHERE category_id IS NOT NULL GROUP BY category_id`), `ListCategoryTransactions :many` (`SELECT ... WHERE category_id = $1 ORDER BY occurred_on DESC, id DESC`).
  - [x] Update `db/query/transaction.sql`: add `category_id` to `CreateTransaction` (new param + RETURNING), `UpdateTransaction` (new SET param), and the `SELECT` column lists of `ListAccountTransactions` (and `CreateTransaction`/`ListCategoryTransactions` RETURNING/SELECT). The `transaction` row now has `category_id pgtype.Int8`.
  - [x] `make sqlc` (pinned Docker image). Confirm `Category` model (`kind` → `string`, `created_at` → `pgtype.Timestamptz`) and `Transaction.CategoryID` → `pgtype.Int8`; `CategoryUsageCounts` row has `CategoryID pgtype.Int8` + `N int64`. Commit generated files; keep hand code out of the sqlc files.

- [x] **Task 3 — `service/category` + category assignment + summary (AC: #1, #2, #3)**
  - [x] Add `internal/service/category/category.go`: `New(pool)`; a `Kind` string type (`Income`/`Expense` + `IsValid`); a `Category` struct (`ID, Name, Kind, CreatedAt`); `Create(ctx, name string, kind Kind) (Category, error)` (trim/validate non-empty, valid kind, one tx); `List(ctx) ([]Category, error)`; `ListWithUsage(ctx) ([]CategoryUsage, error)` where `CategoryUsage{Category; Count int64}` (joins `CategoryUsageCounts`); `Delete(ctx, id int64, force bool) error` — if referenced and `!force` ⇒ `ErrCategoryInUse`; if `force`, `ClearCategoryFromTransactions` then `DeleteCategory` in **one** tx (AD-3); missing id ⇒ `ErrNotFound`. Typed errors `ErrEmptyName`, `ErrInvalidKind`, `ErrCategoryInUse`, `ErrNotFound`.
  - [x] In `service/transaction`: extend `Record` and `Edit` with a `categoryID int64` parameter (0 = none). When `categoryID != 0`, load the category via `store.GetCategory` (missing ⇒ `ErrCategoryNotFound`) and require its `kind` matches the transaction `type` (income category only on income, expense only on expense) else `ErrCategoryKindMismatch`; store `category_id` (NULL when 0). The `legs()`/insert/update pass `category_id` through; `Transfer` always stores NULL. Add a `CategoryName string` (resolved) and `CategoryID int64` to the display `Transaction`, and a `category` id→name map in `List`/`toTransaction` (alongside the account-name map).
  - [x] Add `CategoryTransactions(ctx, categoryID int64) ([]CategoryTxn, []money.Money, error)` to `service/transaction` (it owns transaction reads): list the category's transactions (resolve each one's **account name + currency** via the all-accounts map), and compute per-currency **totals** via a new `domain` aggregation. A category's rows are all one kind (its amount = the income's `to_amount` / the expense's `from_amount`, in that account's currency).
  - [x] Add `domain.SumByCurrency(amounts []money.Money) []money.Money` — group by currency and sum (exact decimals, full precision; no conversion). The single home for this aggregation (AD-10). Pure unit test.
  - [x] DB-gated tests: `category` CRUD + guarded delete (in-use refused; force clears + deletes); assignment kind-match (income category on income ok; on expense ⇒ `ErrCategoryKindMismatch`; missing category ⇒ `ErrCategoryNotFound`); `CategoryTransactions` returns the rows + correct per-currency totals; `domain.SumByCurrency` unit test (multi-currency grouping).

- [x] **Task 4 — Categories page, category select on the tx form, category summary (AC: #1, #2, #3)**
  - [x] Add an authenticated `GET /categories` page (templ in the shell; reuse an existing nav target — link from `/settings` like `/exchange-rates`, keeping the five-item primary nav unchanged): a create form (name, kind select income/expense), and a list grouped/labelled by kind showing each category's name, usage count, a **Delete** button (guarded), and a link to its summary. `POST /categories` creates; `POST /categories/delete` deletes (`id`, optional `force=true`). On `ErrCategoryInUse`, re-render with a message offering "Delete and unassign" (a second submit that sets `force=true`).
  - [x] Add `GET /categories/{id}` summary page: the category's transactions (account, date, description, signed amount) and the **per-currency totals**.
  - [x] On the account-detail income/expense form (`AccountDetailPage`), add a **Category** `<select>` (optional; `<optgroup>` Income / Expense, or the full list) populated from `deps.Categories.List`. Submit `category_id` (empty = none) in `POST /accounts/{id}/transaction` and `/transaction/edit`; the edit form pre-selects the row's category. Show the assigned category in the register row. The handler parses `category_id` (0 when blank).
  - [x] Wire: add a `Categories` interface to `http.Deps` (`Create`, `List`, `ListWithUsage`, `Delete`) and extend `Transactions` with `CategoryTransactions`; `Record`/`Edit` interface signatures gain `categoryID int64`. Inject `category.New(pool)` in `main.go`. `make generate css`; commit `*_templ.go` + `app.css`.

- [x] **Task 5 — Tests, verify, docs (AC: all)**
  - [x] Update `internal/http/router_test.go`: extend the stub `Transactions` (`Record`/`Edit` gain `categoryID`, add `CategoryTransactions`) and add a stub `Categories`; register in `testDeps`. Add tests: create a category, assign it to an income on the account-detail page, see it on the row; `/categories` lists it with usage; guarded delete refused then forced; `/categories/{id}` shows the summary.
  - [x] `go build`/`go vet`/`go test ./...` + `make nofloat` clean (DB-gated tests skip without a DB; `nofloat` stays green — decimal only).
  - [x] Live smoke (compose db + run, logged in): create an expense category "Food" and an income category "Salary"; on a cash account record an expense assigned to "Food" and an income assigned to "Salary"; try assigning "Food" to an income ⇒ rejected; `/categories/{food}` shows the expense and its total; delete "Food" while in use ⇒ refused; "delete and unassign" ⇒ succeeds and the transaction is uncategorized; persistence across reload.
  - [x] Update `README.md` briefly (categories: income/expense kinds; a category attaches only to its matching transaction type; filter + per-currency sum by category; guarded delete with delete-and-unassign).

## Dev Notes

### Scope boundary — what this story does NOT do (read first)

- **NO cross-currency category totals** — sums are **per currency** (no `money.Convert` / Display Currency); converted category totals are Epic 5. A category's rows can span accounts of different currencies; `SumByCurrency` keeps them separate.
- **NO categories on transfers** — `category_id` is always NULL for transfers; only income/expense are categorized.
- **NO cross-account register/filter UI** — Story 3.5. This story's "filter" is the per-category summary page.
- **NO DB-enforced kind rule** — enforced in `service/transaction` (load the category, match `kind` to `type`); the DB has no transaction-type column for an FK/trigger to check, consistent with the project's service-as-authority pattern.

### Architecture invariants this story must honor

- **FR-7 / conventions — kind rule.** Income-type category only on Income, expense-type only on Expense; enforced in the service. [Source: ARCHITECTURE-SPINE.md#Consistency Conventions]
- **AD-2 / AD-10 — derived on read, one home.** Category totals are derived (not stored); the per-currency sum is one `domain.SumByCurrency`. `http` renders, does no math. [Source: ARCHITECTURE-SPINE.md#AD-2, #AD-10]
- **AD-3 — one tx per use-case.** `Create`/`Delete`(+force unassign)/`Record`/`Edit` each one tx. [Source: ARCHITECTURE-SPINE.md#AD-3]
- **AD-4 / AD-5 — decimal, native currency.** Amounts stay decimal in native currency; sums are same-currency exact aggregations. [Source: ARCHITECTURE-SPINE.md#AD-4, #AD-5]
- **AD-1 — layering.** New `service/category`; `service/transaction` reads `category` via `store.GetCategory` (not `service/category`); `http` defines the `Categories` interface; `main` injects. [Source: ARCHITECTURE-SPINE.md#AD-1]

### Previous-story intelligence (3.1–3.3 + 2.x) — load-bearing

[Source: 3-3-transfers-between-accounts.md; 3-1-record-cash-income-expenses.md; 2-1-currencies-display-currency.md; [[financas-epic1-progress]]]

- **The transaction ledger + queries already exist** (`transaction.sql`: Create/Update/Delete/ListAccountTransactions). Adding `category_id` means editing those query column lists and re-running sqlc; the `store.Transaction` model gains `CategoryID pgtype.Int8`. **Every caller building `CreateTransactionParams`/`UpdateTransactionParams` must set `CategoryID`** (income/expense from the form; `Transfer` ⇒ `pgtype.Int8{}`).
- **`service/transaction.Record`/`Edit` signatures change** (add `categoryID int64`). This ripples to `http.Transactions` (interface), the http handlers/`parseTxForm` (parse `category_id`), `cmd/server` (no change — same constructor), and **all stubs/tests** that call `Record`/`Edit` (3.1/3.2/3.3 http tests, the service tests). Update them. Keep `0 = no category`.
- **Display mapping** `toTransaction(accountID, row, names)` is account-relative (Story 3.3, `Incoming`/`Counterparty`). Add `CategoryID`/`CategoryName` (resolve via a category id→name map built from `ListCategories`, like the account-name map). `web.TxRow` gains `Category string`.
- **`store.GetCategory`** is read by `service/transaction` to validate kind (store-not-service rule, as `GetAccount` is). **`store.ListAllAccounts`** already provides the account map (Story 3.3) — extend its use to also map id→currency for `CategoryTransactions`.
- **Pattern for the new entity** (Story 2.1/2.3): migration → `db/query/*.sql` → `make sqlc` → `service/category` (reads `store.New(pool)`, writes one `pool.Begin` tx) → `http` `Categories` interface + page → wire in `main`. `/categories` linked from `/settings` (like `/exchange-rates`); five-item nav unchanged. `money.Money`/`decimal` as before; **build `GOTOOLCHAIN=local`**; `make nofloat` guards `internal/{money,domain,service,store}`. Local DB host **5433**; DB-gated tests skip without `DATABASE_URL`/`TEST_DATABASE_URL`. Dev login `owner`/`financas`. `baseline_commit` real SHA `eeae658`. Commit + push to `main` when done.
- **chi:** `/categories`, `/categories/{id}`, `POST /categories`, `POST /categories/delete` sit alongside the account routes; static vs param precedence as before.

### Project Structure Notes

New: `db/migrations/00007_categories.sql`, `db/query/category.sql`, sqlc output (`internal/store/category.sql.go`, regenerated `transaction.sql.go`/`models.go`/`querier.go`), `internal/service/category/category.go` (+ test), `internal/domain/sum.go` `SumByCurrency` (+ test), `web` `CategoriesPage` + `CategorySummaryPage` (+ regenerated `pages_templ.go`), `web/shell.go` view structs (`CategoryRow`, `CategoryTxRow`, `TxRow.Category`). Modified: `db/query/transaction.sql` (+`category_id`), `internal/service/transaction/transaction.go` (`categoryID` on Record/Edit, kind validation, `CategoryName` in display, `CategoryTransactions`) + test, `internal/http/router.go` (`Categories` iface, routes/handlers, category select on the tx form) + `router_test.go`, `cmd/server/main.go` (wire `category.New`), `web/pages.templ` (tx form category select + register category), `README.md`.

### Testing standards

- `domain`: pure unit test for `SumByCurrency` (multi-currency grouping, exact decimals).
- `service/category`: DB-gated — CRUD, guarded delete (refused/forced), `ListWithUsage` counts.
- `service/transaction`: DB-gated — kind-match enforcement (ok/mismatch/missing), `category_id` round-trip, `CategoryTransactions` rows + per-currency totals, transfers stay NULL.
- `http`: stub-backed — assign a category on the tx form; `/categories` list + guarded/forced delete; `/categories/{id}` summary.
- `go test ./...` green with no DB; `go vet` + `make nofloat` clean.

### References

- [Source: epics.md#Story 3.4] — acceptance criteria; [Source: epics.md FR-7] — categories, kind rule, filter/sum, guarded delete
- [Source: ARCHITECTURE-SPINE.md#Consistency Conventions] — income/expense category kind rule
- [Source: ARCHITECTURE-SPINE.md#AD-2 / #AD-10 / #AD-3 / #AD-1] — derived on read; one aggregation home; one tx; layering
- [Source: 3-1-record-cash-income-expenses.md / 3-3-transfers-between-accounts.md] — transaction queries, `Record`/`Edit`, account-relative `toTransaction`, name-map pattern

## Dev Agent Record

### Agent Model Used

claude-opus-4-8[1m] (Opus 4.8, 1M context)

### Debug Log References

- `make migrate` applied `00007` (goose v7). `make sqlc` regenerated: `Category` model, `Transaction.CategoryID pgtype.Int8`, `CategoryUsageCountsRow{CategoryID pgtype.Int8; N int64}`. **Gotcha:** the first `RETURNING`/`SELECT` column order put `category_id` before `created_at`, which differs from the table's physical order (the ALTER appended `category_id` last) → sqlc generated a bespoke `CreateTransactionRow`. Reordered the column lists to `... description, created_at, category_id` so `CreateTransaction`/`ListAccountTransactions`/`ListCategoryTransactions` reuse `store.Transaction`.
- Guarded delete uses the FK: a plain `DELETE category` while referenced raises Postgres `23503` (foreign_key_violation), mapped to `ErrCategoryInUse`; `force` runs `ClearCategoryFromTransactions` (sets `category_id = NULL`) then deletes — both in one tx (AD-3).
- `go build`/`go vet`/`make nofloat` clean. Full suite green with and without a DB. `gofmt -w` applied.
- Live DB: `domain.TestSumByCurrency`, `category.TestCategory` (CRUD, usage count, guarded/forced delete), `transaction.TestCategoryAssignment` (kind-match ok/mismatch/missing, `category_id` round-trip + resolved name, `CategoryTransactions` per-currency totals 30 USD / 70 BRL) PASS.
- Live HTTP smoke (server :8099 + db :5433, owner/financas): created Food(expense)+Salary(income); recorded an expense with Food; an **income with Food ⇒ 400 "category kind must match the transaction type"**; the account detail shows the **Food** badge on the row; `/categories/{food}` shows the **30.0000 USD** total + the account; **guarded delete refused** ("in use by transactions"); **delete & unassign (force) ⇒ 303** and the transaction's `category_id` is now NULL.

### Completion Notes List

All three acceptance criteria verified (pure unit + live DB + live HTTP):
- **AC1 — kind rule:** `category(kind)` + `transaction.category_id`; `service/transaction.resolveCategory` loads the category (via `store.GetCategory`) and requires `kind == type` (`ErrCategoryKindMismatch`; `ErrCategoryNotFound` if missing). Transfers always store `category_id = NULL`.
- **AC2 — filter + sum:** `domain.SumByCurrency` (the one home for this aggregation, AD-10) groups exact decimals **per currency** (no conversion — Epic 5). `service/transaction.CategoryTransactions` lists a category's rows (resolving each account's name + currency) and returns the per-currency totals; the `/categories/{id}` summary page renders them.
- **AC3 — guarded delete:** `service/category.Delete(id, force)` refuses an in-use category (`ErrCategoryInUse` via the FK 23503), and with `force` unassigns then deletes in one tx. The categories page shows the usage count, a plain Delete, and a "Delete & unassign" action for in-use categories.

Decisions / variances (intentional):
- **Per-currency totals, not converted** — a category can span accounts of different currencies; `SumByCurrency` keeps them separate (Display-Currency conversion is Epic 5, AD-12).
- **`Record`/`Edit` gained a `categoryID int64` param** (0 = uncategorized) — rippled to `http.Transactions`, the handlers/`parseTxForm`, all stubs, and the 3.1–3.3 tests (updated to pass `0`). `Transfer` is unchanged (transfers are never categorized).
- **`service/transaction` reads `category` via `store.GetCategory`** (not `service/category`) — no service→service dep (AD-1), same rule as `GetAccount`. The category id→name map (for register labels) is built from `store.ListCategories` alongside the account-name map.
- **`http` imports `service/category`** for the `Categories` interface types (http → service, AD-1). `/categories` is linked from `/settings` (like `/exchange-rates`); the five-item nav is unchanged.
- **Column-order fix** to keep `CreateTransaction` returning `store.Transaction` (see Debug Log) — avoids a bespoke row type and keeps `toTransaction` uniform.

Reviewer notes: no `sprint-status.yaml` → status tracked in this file only. `baseline_commit` is the real SHA `eeae658` (HEAD before this story). Committed + pushed to `main` per the owner's standing instruction.

### File List

New:
- `db/migrations/00007_categories.sql`, `db/query/category.sql`
- `internal/store/category.sql.go` (sqlc; `transaction.sql.go`/`models.go`/`querier.go` regenerated)
- `internal/service/category/category.go`, `category_test.go`
- `internal/domain/sum.go`, `sum_test.go`

Modified:
- `db/query/transaction.sql` (+`category_id`)
- `internal/service/transaction/transaction.go` (`categoryID` on Record/Edit, `resolveCategory` kind validation, `CategoryName`/`CategoryID` in display, `categoryNames` map, `CategoryTransactions`/`CategoryTxn`, Transfer stores NULL category) + `transaction_test.go`
- `internal/http/router.go` (`Categories` iface, `Transactions` Record/Edit + `CategoryTransactions`, `/categories` routes + handlers, category select on the tx form, register category) + `router_test.go` (stub `Categories`, stub `Transactions` updates, `TestCategoriesPageAndGuardedDelete`)
- `cmd/server/main.go` (wire `category.New(pool)`)
- `web/pages.templ` (`CategoriesPage`, `CategorySummaryPage`, category select + register badge, Settings link) + regenerated `web/pages_templ.go`, `web/shell.go` (`CategoryOption`/`CategoryRow`/`CategoryTxRow`, `TxRow.Category`/`.CategoryID`, `countLabel`), rebuilt `web/static/css/app.css`
- `README.md` (categories section)

## Change Log

| Date | Change |
| --- | --- |
| 2026-06-29 | Story 3.4 drafted (create-story): `category` entity + `transaction.category_id`, kind-matching, `domain.SumByCurrency`, categories page + per-category summary, guarded delete. Status → ready-for-dev. |
| 2026-06-29 | Story 3.4 implemented (dev-story): `00007` `category` + `transaction.category_id`; kind-matched assignment; `domain.SumByCurrency`; `/categories` page + per-category summary; guarded delete (+ delete-and-unassign). All 3 ACs verified (pure unit + live DB + live HTTP). build/vet/test + nofloat green. Status → review. |
