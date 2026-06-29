---
baseline_commit: 6cea85214bfbd7ab96d2a55960d22f426b751508
---

# Story 3.1: Record cash income & expenses

Status: review

<!-- Note: Validation is optional. Run validate-create-story for quality check before dev-story. -->

## Story

As the owner,
I want to record income and expense transactions on my cash accounts,
so that my balances reflect reality.

## Acceptance Criteria

From `epics.md` → Epic 3 → Story 3.1 (realizes part of FR-6). **Given** a cash Account exists, **When** I add an Income or Expense (amount, date, account, optional description) through a service use-case, **Then**:

1. A `Transaction` row is created and the account balance updates within one DB transaction (FR-6, AD-2, AD-3). The balance is **derived on read** from the ledger (never stored on the account); creating the transaction makes the next balance read reflect it.
2. Amounts are stored as **non-negative magnitudes** with direction derived from `Transaction.type`, at `NUMERIC(19,4)` money scale (AD-4); amount input is parsed from a decimal string, never a float.
3. I can **edit** and **delete** the transaction, and the balance **re-derives** accordingly (no stored balance to fix up).

> Scope: this story establishes the **canonical `transaction` ledger table** and the **first `domain` derivation** (`AccountBalance`). It covers Income and Expense on **cash** accounts only. Credit-card expenses are Story 3.2, Transfers (the two-account row) are Story 3.3, categories are Story 3.4, the cross-account register/filtering is Story 3.5, and investment cash flows are Epic 4. The table is laid out in the **AD-9 one-row `from/to` shape** now so those stories extend it without restructuring.

## Tasks / Subtasks

- [x] **Task 1 — `transaction` ledger schema (canonical AD-9 shape) (AC: #1, #2)**
  - [x] Add goose migration `db/migrations/00005_transactions.sql`. Up:
    ```sql
    CREATE TABLE transaction (
        id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
        type            TEXT NOT NULL CHECK (type IN ('income', 'expense')),
        from_account_id BIGINT REFERENCES account (id),
        to_account_id   BIGINT REFERENCES account (id),
        from_amount     NUMERIC(19, 4) NOT NULL DEFAULT 0 CHECK (from_amount >= 0),
        to_amount       NUMERIC(19, 4) NOT NULL DEFAULT 0 CHECK (to_amount >= 0),
        occurred_on     DATE NOT NULL,
        description     TEXT NOT NULL DEFAULT '',
        created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
        CHECK (from_account_id IS NOT NULL OR to_account_id IS NOT NULL)
    );
    CREATE INDEX transaction_from_account ON transaction (from_account_id);
    CREATE INDEX transaction_to_account   ON transaction (to_account_id);
    ```
    Down: `DROP TABLE transaction;`. Follow the goose `StatementBegin/End` + header-comment style of `00004`.
  - [x] **The AD-9 model:** a transaction **debits `from_account` and credits `to_account`** from one row. **Income** ⇒ `to_account_id = account`, `to_amount = magnitude`, `from_*` unused (NULL / 0). **Expense** ⇒ `from_account_id = account`, `from_amount = magnitude`, `to_*` unused. Transfers (Story 3.3) populate both sides; investment columns (security/qty/price/fees) are ALTERed in Epic 4. The `type` CHECK is intentionally narrow now — later stories widen it (forward-only goose). Amounts are non-negative magnitudes (AD-4); direction is the `from`/`to` placement chosen by `type`. No `currency` column — each leg's currency is its account's native currency (AD-5).

- [x] **Task 2 — sqlc queries + `GetAccount` + generate (AC: #1, #3)**
  - [x] Add `db/query/transaction.sql`:
    - `CreateTransaction :one` — INSERT (`type, from_account_id, to_account_id, from_amount, to_amount, occurred_on, description`) RETURNING all columns.
    - `UpdateTransaction :execrows` — `UPDATE transaction SET type=$2, from_account_id=$3, to_account_id=$4, from_amount=$5, to_amount=$6, occurred_on=$7, description=$8 WHERE id=$1`.
    - `DeleteTransaction :execrows` — `DELETE FROM transaction WHERE id=$1`.
    - `ListAccountTransactions :many` — `SELECT ... WHERE from_account_id=$1 OR to_account_id=$1 ORDER BY occurred_on DESC, id DESC` (used for both the register and the balance derivation).
  - [x] Add to `db/query/account.sql`: `GetAccount :one` — `SELECT id, name, type, currency, archived, created_at FROM account WHERE id=$1` (the transaction use-case reads the account via `store`, NOT by importing `service/account` — preserves AD-1, no service→service dep).
  - [x] `make sqlc` (pinned Docker image). Confirm generated types: nullable `from_account_id`/`to_account_id` → `pgtype.Int8`; `from_amount`/`to_amount` → `decimal.Decimal`; `occurred_on` → `time.Time` (date override); `created_at` → `pgtype.Timestamptz`; `:execrows` → `(int64, error)`. Commit generated files; keep hand code out of the sqlc files / out of `pool.go`.

- [x] **Task 3 — `domain.AccountBalance` (the first canonical derivation; AC: #1, #2, #3)**
  - [x] Add `internal/domain/balance.go`: a pure `AccountBalance(accountID int64, currency money.Currency, txns []BalanceTxn) money.Money` where `BalanceTxn{FromAccountID int64; FromAmount decimal.Decimal; ToAccountID int64; ToAmount decimal.Decimal}` (a 0 account id means "no account on that side"). It sums `+to_amount` for legs crediting the account and `−from_amount` for legs debiting it, returning `money.New(net, currency)` (full precision; rounding is a display-boundary concern, AD-12). This is the single home for "account balance" (AD-10) — `service`/`http` never re-derive it.
  - [x] **`domain` now imports the inner `money` value package** (and `shopspring/decimal`). `money` imports nothing project-internal, so `domain → money` is acyclic and AD-1 holds (it is the dependency Net Worth/Valuation will also need for `money.Convert`, AD-12). Update `internal/domain/doc.go` to say domain imports only the inner `money` value layer (not "nothing project-internal").
  - [x] Pure unit test `balance_test.go` (no DB): income credits (+), expense debits (−), a mix nets correctly, an unrelated account's legs are ignored, and a both-sides (transfer-shaped) leg debits one account / credits the other. Assert exact decimal values via `money.Money.Amount()`.

- [x] **Task 4 — `service/transaction` use-case (AC: #1, #2, #3)**
  - [x] Add `internal/service/transaction/transaction.go`: `New(pool)`; a `TxType` string type (`Income`/`Expense` + `IsValid`); a display `Transaction` struct (`ID int64`, `Type TxType`, `AccountID int64`, `Amount decimal.Decimal` (magnitude), `Date time.Time`, `Description string`, `CreatedAt time.Time`).
  - [x] Methods (each write in **one** `pool.Begin` tx, AD-3):
    - `Record(ctx, accountID int64, typ TxType, amount decimal.Decimal, date time.Time, description string) (Transaction, error)` — load the account via `store.GetAccount` (→ `ErrAccountNotFound` on no rows); require it be a **cash** account (`ErrNotCashAccount` otherwise — Story 3.2 widens to credit); validate `typ.IsValid()` and `amount.IsPositive()` (`ErrNonPositiveAmount`); place magnitude into `to_amount`+`to_account` (income) or `from_amount`+`from_account` (expense) with the other side NULL/0; insert.
    - `Edit(ctx, accountID, txID int64, typ TxType, amount decimal.Decimal, date time.Time, description string) error` — same validation; recompute from/to placement from `typ`; `UpdateTransaction`; rows==0 → `ErrTxNotFound`.
    - `Delete(ctx, txID int64) error` — `DeleteTransaction`; rows==0 → `ErrTxNotFound`.
    - `Balance(ctx, accountID int64) (money.Money, error)` — `GetAccount` (currency) + `ListAccountTransactions` → build `[]domain.BalanceTxn` (map `pgtype.Int8` → int64, 0 when invalid) → `domain.AccountBalance`. **All math is in `domain`.**
    - `List(ctx, accountID int64) ([]Transaction, error)` — `ListAccountTransactions` → map to display `Transaction` (magnitude = whichever side is non-zero; `AccountID` = the bound account).
  - [x] Typed errors: `ErrAccountNotFound`, `ErrNotCashAccount`, `ErrInvalidType`, `ErrNonPositiveAmount`, `ErrTxNotFound`. Reads via `store.New(s.pool)`; writes via `store.New(tx)`.
  - [x] DB-gated integration test `transaction_test.go` (skips without DB): create a cash USD account (via `store`/`service/account`); record income 100 and expense 30 → `Balance` = 70.0000 USD; edit the expense to 50 → balance 50; delete the income → balance −50; reject a non-cash account (`ErrNotCashAccount`), a non-positive amount, an invalid type, and `Edit`/`Delete` of a missing id (`ErrTxNotFound`). Use a unique account name per run (resilient to row accumulation).

- [x] **Task 5 — Account detail / register page + add/edit/delete (AC: #1, #2, #3)**
  - [x] Add an authenticated `GET /accounts/{id}` account-detail page (templ in the shell, active nav `accounts`): header (name, type, base currency), the **derived balance** (`money.Money.String()`), an add form (type `<select>` income/expense, amount, date, optional description), and the account's transactions newest-first (date, type, description, signed amount — `+` income green / `−` expense red, per UX-DR5; the sign is presentation by `type`, not math). Each row has an **Edit** link (`?edit={txid}` re-renders the top form pre-filled, posting to the edit route) and a **Delete** button. With `?edit={txid}` the form switches to edit mode.
  - [x] Routes (chi; the static `/accounts/rename` + `/accounts/archive` POSTs keep precedence over the `{id}` param route):
    - `GET  /accounts/{id}` — detail/register (reads optional `?edit=`).
    - `POST /accounts/{id}/transaction` — add (`type`, `amount`, `date`, `description`).
    - `POST /accounts/{id}/transaction/edit` — edit (`tx_id`, `type`, `amount`, `date`, `description`).
    - `POST /accounts/{id}/transaction/delete` — delete (`tx_id`).
    All success paths redirect 303 to `/accounts/{id}`; validation errors re-render with a message + 400. Parse amount via `decimal.NewFromString` (never a float); parse date `time.Parse("2006-01-02", …)`.
  - [x] Add a `Transactions` interface to `http.Deps` (`Record`, `Edit`, `Delete`, `Balance`, `List`); add `Get(ctx, id) (account.Account, error)` to the `Accounts` interface (+ implement `service/account.Get` using `store.GetAccount`, returning `ErrNotFound` on no rows). Wire `transaction.New(pool)` in `main.go`. **Link each account name in the accounts list to `/accounts/{id}`** (small templ change in `AccountsPage`). Keep the five-item primary nav unchanged; `/transactions` stays a ComingSoon placeholder (Story 3.5 builds the cross-account register).
  - [x] `make generate css` after templ/Tailwind edits; commit `*_templ.go` + rebuilt `app.css`.

- [x] **Task 6 — Tests, verify, docs**
  - [x] `go build`/`go vet`/`go test ./...` + `make nofloat` clean (DB-gated tests skip without a DB). **`nofloat` MUST stay green** — amounts are `decimal.Decimal` end to end (`internal/domain` is now in the guarded set and uses decimal, no float).
  - [x] http test (stub `Transactions`; extend stub `Accounts` with `Get`): unauth `GET /accounts/{id}` → 303 `/login`; authed GET renders the balance + add form + any rows; `POST .../transaction` (valid income) → 303 and the row + updated balance appear; `POST .../transaction/edit` changes it; `POST .../transaction/delete` removes it; an invalid add (bad amount / non-positive) → 400 without crashing.
  - [x] Live smoke (compose db + run, logged in): create a cash account; add income 1000 and expense 250 → balance shows 750.0000; edit the expense to 300 → 700; delete the income → −300; confirm persistence across reload (balance always re-derives, never stored).
  - [x] Update `README.md` briefly (transactions: append-only-style ledger in the AD-9 one-row from/to shape; income credits / expense debits a cash account; balances **derived on read** by `domain.AccountBalance`, never stored; edit/delete re-derive).

## Dev Notes

### Scope boundary — what this story does NOT do (read first)

- **NO stored balances** — the account has no balance column (Story 2.3 deliberately omitted it). Balance is `domain.AccountBalance` over the ledger, computed on every read (AD-2). Edit/delete need no balance fix-up.
- **NO credit / investment accounts** — Income/Expense are restricted to **cash** accounts here (`ErrNotCashAccount`). Credit is Story 3.2; investment cash flows are Epic 4.
- **NO transfers** — the two-account row is Story 3.3. The schema already supports it (both `from`/`to` populated); this story only ever fills one side.
- **NO categories** — Story 3.4 adds the `category_id` column + classification.
- **NO cross-account register / filtering** — Story 3.5 builds `/transactions`. This story's register is per-account on the account-detail page; `/transactions` stays ComingSoon.
- **NO conversion** — single-account income/expense is same-currency; `money.Convert`/Display-Currency aggregation is Epic 4/5.

### Architecture invariants this story must honor

- **AD-2 — ledger is the single source of truth; balances derived on read.** `transaction` rows are the only authored state; the balance is computed, never stored or independently edited. [Source: ARCHITECTURE-SPINE.md#AD-2]
- **AD-9 — one canonical Transfer storage shape.** The table uses `from_account_id`/`to_account_id`/`from_amount`/`to_amount` from day one; income = to-only, expense = from-only, so Story 3.3 transfers need no restructuring. Balance derivation = `Σ to_amount[to=A] − Σ from_amount[from=A]`. [Source: ARCHITECTURE-SPINE.md#AD-9]
- **AD-4 — decimal, never float; non-negative magnitudes, direction from type.** `NUMERIC(19,4)`, `decimal.Decimal` end to end; amounts ≥ 0; income vs expense decides from/to placement. [Source: ARCHITECTURE-SPINE.md#AD-4]
- **AD-10 — one canonical home per derived figure; http does no math.** `domain.AccountBalance` is the single balance function; `service` loads inputs and calls it; `http` only renders. The `+/−` sign in the register is presentation keyed off `type`, not arithmetic. [Source: ARCHITECTURE-SPINE.md#AD-10]
- **AD-3 — one DB transaction per use-case.** `Record`/`Edit`/`Delete` each wrap their write in one tx. [Source: ARCHITECTURE-SPINE.md#AD-3]
- **AD-1 — layering.** `http → service → store`; `service/transaction` reads accounts via `store` (not `service/account`); `domain` imports only `money`. [Source: ARCHITECTURE-SPINE.md#AD-1]
- **AD-5 — native currency.** Each leg's amount is in its account's currency; no currency column, no conversion. [Source: ARCHITECTURE-SPINE.md#AD-5]
- **Conventions:** `NUMERIC(19,4)` money; `DATE` for `occurred_on`; `timestamptz` `created_at`; bigint identity PK; amounts non-negative with direction from `type`. [Source: ARCHITECTURE-SPINE.md#Consistency Conventions]

### The `domain` layering clarification (decision)

`internal/domain` was empty (only `doc.go`). This story adds the first derivation and makes `domain` import the inner **`money`** package so `AccountBalance` can return `money.Money`. This is acyclic — `internal/money` imports nothing project-internal — and is the same dependency Net Worth/Valuation (Epic 4/5) require for `money.Convert` (AD-12). The dependency graph stays `http → service → store → domain → money → decimal`. The Transaction **entity** (display struct) lives in `service/transaction` (mirroring `Rate`/`Account`); `domain` owns only the **derivation** and its minimal `BalanceTxn` input type. Update `doc.go`'s "imports nothing project-internal" line accordingly.

### Previous-story intelligence (2.1–2.3 + Epic 1) — load-bearing

[Source: 2-3-create-manage-accounts.md; 2-2-exchange-rates.md; [[financas-epic1-progress]]]

- **Pattern (do exactly this):** migration → `db/query/*.sql` → `make sqlc` (pinned `sqlc/sqlc:1.27.0` Docker image, **not** `go run`) → `service/<x>` (reads `store.New(pool)`, writes one `pool.Begin` tx) → `http` Deps interface + templ page → wire in `main`. Generated files committed.
- **Store file ownership:** `db.go`/`models.go`/`querier.go`/`*.sql.go` are sqlc-generated; `internal/store/pool.go` is hand-written (pgx pool + goose + decimal codec). Don't cross them.
- **decimal↔NUMERIC plumbing already exists** (Story 2.2): pgx-shopspring-decimal codec registered in `store.NewPool`; sqlc overrides `numeric → decimal.Decimal`, `date → time.Time`. **Reuse — no new overrides.** `created_at` stays `pgtype.Timestamptz` (override key doesn't match); map `.Time` in the service (as `account`/`exchangerate` do).
- **Nullable columns:** `from_account_id`/`to_account_id` are nullable bigint → sqlc emits `pgtype.Int8` (`.Int64`, `.Valid`). Construct `pgtype.Int8{Int64: id, Valid: true}` for the populated side and `{Valid: false}` for the absent side; map back to `int64` (0 when `!Valid`) for `domain.BalanceTxn`.
- **Router:** `NewRouter(Deps)`; protected chi group with `requireAuth`; handler template = `exchangeRates`/`accounts` handlers (parse form → service → 303 redirect; re-render with `errMsg` + non-200 on error). `Deps` has `Sessions, Auth, Ready, Settings, ExchangeRates, Accounts, OwnerName` — **add `Transactions`** and extend `Accounts` with `Get`. chi **static routes take precedence over `{param}`** within a method, so `/accounts/rename` + `/accounts/archive` (POST) coexist with `GET /accounts/{id}` and the `POST /accounts/{id}/transaction*` routes.
- **templ/view:** copy `AccountsPage` shape; add `AccountDetailPage` + a `TxRow` view struct in `web/shell.go`. Use `accountID(int64)` helper pattern for stringifying ids in attributes (add a similar `txID` helper or reuse). Money renders via `money.Money.String()` (`"100.0000 USD"`, never a float). Tailwind tokens in use: `rounded-card`, `shadow-card`, `text-muted`, `text-gain`/`text-loss` (green/red), `text-accent`.
- **http test:** copy `stubAccounts`/`stubExchangeRates`; add `stubTransactions` + extend `stubAccounts` with `Get`; register both in `testDeps`.
- **money:** `money.New(decimal, Currency)`, `Money.String()`, `Money.Amount()`, `Currency`, `USD`/`BRL`, `IsSupported`. `decimal` = `github.com/shopspring/decimal` (`NewFromString`, `RequireFromString`, `IsPositive`, `Zero`, `Add`/`Sub`).
- **Build with `GOTOOLCHAIN=local`** (go.mod pins 1.26.3). `make nofloat` guards `internal/{money,domain,service,store}` — **domain is guarded**, so use decimal only. Local DB: `docker compose up -d db` → host **5433**; `DATABASE_URL=postgres://financas:financas@localhost:5433/financas?sslmode=disable`; DB-gated tests skip without `DATABASE_URL`/`TEST_DATABASE_URL`. Dev login `owner`/`financas` (argon2id hash in docker-compose with `$$` escaping). `baseline_commit` real SHA `6cea852`. Per the owner's standing instruction, commit + push to `main` when the story is done (one commit per story).

### Project Structure Notes

New: `db/migrations/00005_transactions.sql`, `db/query/transaction.sql`, sqlc output (`internal/store/transaction.sql.go`, `account.sql.go` regenerated for `GetAccount`, + `models.go`/`querier.go`), `internal/domain/balance.go` (+ `balance_test.go`), `internal/service/transaction/transaction.go` (+ `transaction_test.go`), `web` `AccountDetailPage` (+ regenerated `pages_templ.go`), `web/shell.go` `TxRow`. Updated: `db/query/account.sql` (+`GetAccount`), `internal/service/account/account.go` (+`Get`), `internal/http/router.go` (+`Transactions` iface, `Accounts.Get`, `/accounts/{id}` routes + handlers) + `router_test.go`, `cmd/server/main.go` (wire), `web/pages.templ` (link account name → detail), `internal/domain/doc.go`, `README.md`. No structural variance — same layered shape as 2.x.

### Testing standards

- `domain`: pure unit tests for `AccountBalance` (the AD-10 heart) — no DB, exact decimals.
- `service/transaction`: DB-gated integration — record/edit/delete re-derive the balance correctly; validation (non-cash, non-positive, bad type, missing tx).
- `http`: httptest with stub `Transactions` + extended stub `Accounts` — auth-gating, detail render with balance, add/edit/delete happy paths, invalid-add sad path.
- `go test ./...` green with no DB; `go vet` + `make nofloat` clean.

### References

- [Source: epics.md#Story 3.1] — acceptance criteria; [Source: epics.md FR-6] — income/expense/transfer, magnitudes + direction-from-type
- [Source: ARCHITECTURE-SPINE.md#AD-2 / #AD-9 / #AD-10] — ledger source of truth; one-row from/to shape; one canonical balance function, http no math
- [Source: ARCHITECTURE-SPINE.md#AD-3 / #AD-4 / #AD-1 / #AD-5] — one tx; decimal magnitudes; layering; native currency
- [Source: 2-3-create-manage-accounts.md] — account schema + `store.GetAccount` source, store-not-service read rule, handler/templ/test templates, `pgtype.Timestamptz` `.Time` mapping
- [Source: 2-2-exchange-rates.md] — decimal↔NUMERIC plumbing (no new overrides), sqlc-via-Docker

## Dev Agent Record

### Agent Model Used

claude-opus-4-8[1m] (Opus 4.8, 1M context)

### Debug Log References

- `make sqlc` (Docker image) — generated `internal/store/transaction.sql.go` + `Transaction` model (`from_account_id`/`to_account_id` → `pgtype.Int8`, `from_amount`/`to_amount` → `decimal.Decimal`, `occurred_on` → `time.Time`, `created_at` → `pgtype.Timestamptz`); `:execrows` for update/delete → `(int64, error)`; `GetAccount(ctx, id)` added to `account.sql.go`. No new `sqlc.yaml` overrides (reused the 2.2 decimal↔NUMERIC plumbing).
- `internal/domain` now imports `internal/money` (acyclic — money imports nothing internal); `doc.go` updated. `domain` is in the `nofloat` set and uses `decimal` only — `make nofloat` stays green.
- `go build`/`go vet`/`make nofloat` clean. Full suite green **with** a DB and **without** (DB-gated `service/transaction` + `service/account` tests `t.Skip` cleanly).
- Live DB: `domain.TestAccountBalance` (pure), `transaction.TestTransaction` PASS — record income/expense, balance re-derive (70 → 50 after edit → −50 after delete), validation (non-cash → `ErrNotCashAccount`, non-positive, bad type, missing tx/account).
- Live HTTP smoke (server :8096 + db :5433, owner/financas): created a cash USD account; income 1000 + expense 250 → balance **750.0000 USD**; edit expense 250→300 → **700.0000 USD**; delete income → **−300.0000 USD**; reload re-derives the same. DB confirmed the surviving expense row is `from_account=acct, from_amount=300.0000, to_amount=0` — the AD-9 from/to shape (income was to-only, expense from-only).

### Completion Notes List

All three acceptance criteria verified (pure unit + live DB + live HTTP):
- **AC1 — transaction created, balance updates within one tx:** `transaction` ledger (AD-9 one-row from/to shape); `Record` validates + inserts in one `pool.Begin` tx (AD-3); the balance is **derived on read** by `domain.AccountBalance`, so the next read reflects the new row. No balance is stored (AD-2).
- **AC2 — non-negative magnitudes, direction from type:** `NUMERIC(19,4)` `from_amount`/`to_amount` (CHECK ≥ 0); income places the magnitude in `to_amount`+`to_account`, expense in `from_amount`+`from_account`. Amount parsed via `decimal.NewFromString` (never a float); `nofloat` green.
- **AC3 — edit & delete re-derive:** `Edit` recomputes from/to from the new type; `Delete` removes the row; balance re-derives on the next read (no stored figure to fix up). Verified live (750→700→−300).

Decisions / variances (intentional, recorded):
- **Canonical AD-9 transaction shape adopted now** (`from_account_id`/`to_account_id`/`from_amount`/`to_amount`), even though 3.1 only fills one side — so Story 3.3 transfers need **no restructuring**. Balance = `Σ to_amount[to=A] − Σ from_amount[from=A]`. The `type` CHECK is narrow (`income`/`expense`); later stories widen it (forward-only goose). No `currency` column — each leg's currency is its account's (AD-5).
- **First `domain` derivation + `domain → money` dependency.** `domain.AccountBalance` is the single canonical balance function (AD-10); `domain` now imports the inner `money` value package (acyclic; it is the same import Net Worth/Valuation will need for `money.Convert`). `doc.go` updated from "imports nothing project-internal" accordingly. The Transaction **entity** (display struct) lives in `service/transaction` (mirroring `Rate`/`Account`); `domain` owns only the derivation + its `BalanceTxn` input.
- **`service/transaction` reads accounts via `store.GetAccount`** (added to `account.sql`), not by importing `service/account` — no service→service dependency (AD-1). `GetAccount` is also exposed as `service/account.Get` for the http detail header.
- **Scope:** income/expense on **cash** accounts only (`ErrNotCashAccount`); credit is 3.2, transfers 3.3, categories 3.4, the cross-account register `/transactions` is 3.5 (kept as a ComingSoon placeholder; the five-item nav is unchanged). The register lives on a new `GET /accounts/{id}` detail page, linked from the accounts list; chi static routes (`/accounts/rename`, `/accounts/archive`) keep precedence over the `{id}` param route.
- **`http` does no math (AD-10):** the `+/−` sign in the register is presentation keyed off `type`; the balance string is `money.Money.String()` from the domain figure.

Reviewer notes: no `sprint-status.yaml` → status tracked in this file only. `baseline_commit` is the real SHA `6cea852` (HEAD before this story). Committed + pushed to `main` per the owner's standing instruction.

### File List

New:
- `db/migrations/00005_transactions.sql`, `db/query/transaction.sql`
- `internal/store/transaction.sql.go` (sqlc-generated; `account.sql.go`/`models.go`/`querier.go` regenerated for `GetAccount`)
- `internal/domain/balance.go`, `internal/domain/balance_test.go`
- `internal/service/transaction/transaction.go`, `internal/service/transaction/transaction_test.go`

Modified:
- `db/query/account.sql` (+`GetAccount`), `internal/service/account/account.go` (+`Get`, +pgx import)
- `internal/domain/doc.go` (layering note)
- `internal/http/router.go` (`Transactions` iface, `Accounts.Get`, `/accounts/{id}` detail + `/transaction` create/edit/delete routes + handlers) + `internal/http/router_test.go` (stub `Transactions`, `stubAccounts.Get`, detail/transaction tests)
- `cmd/server/main.go` (wire `transaction.New(pool)`)
- `web/pages.templ` `AccountDetailPage` + account-name link (+ regenerated `web/pages_templ.go`), `web/shell.go` (`TxRow`), rebuilt `web/static/css/app.css`
- `README.md` (Transactions section)

## Change Log

| Date | Change |
| --- | --- |
| 2026-06-28 | Story 3.1 drafted (create-story): canonical `transaction` ledger (AD-9 from/to shape, income/expense), first `domain` derivation `AccountBalance`, `service/transaction` Record/Edit/Delete/Balance/List, account-detail register page. Status → ready-for-dev. |
| 2026-06-28 | Story 3.1 implemented (dev-story): migration `00005_transactions` + sqlc; `domain.AccountBalance` (first canonical derivation, `domain → money`); `service/transaction` (one-tx-per-write, cash-only, reads accounts via `store`); `/accounts/{id}` register page (add/edit/delete, derived balance). All 3 ACs verified (pure unit + live DB + live HTTP). build/vet/test + nofloat green. Status → review. |
