---
baseline_commit: 1a38e2f1f62a6e90c6f1204886c4e8dafd8be48d
---

# Story 2.3: Create & manage accounts

Status: review

<!-- Note: Validation is optional. Run validate-create-story for quality check before dev-story. -->

## Story

As the owner,
I want to create, rename, and archive cash, credit, and investment accounts,
so that my money is organized the way I hold it.

## Acceptance Criteria

From `epics.md` → Epic 2 → Story 2.3 (realizes FR-1). **Given** I am authenticated, **When** I create an Account with a name, type (cash | credit | investment), and base Currency, **Then**:

1. It appears in the account list and is persisted so it is selectable for transactions later (FR-1). Name, type, and base Currency are stored; the base Currency is one of the supported ISO-4217 codes and is never overwritten by the Display Currency (AD-5).
2. The account's **type carries its balance semantics**: an investment Account exposes a cash balance and a cash Account a cash balance (assets); a credit Account tracks a balance owed (liability). The type is the contract the later balance/Net-Worth derivations key off.
3. I can **rename** an Account (its history and identity are preserved) and **archive** an Account; archiving preserves its history but excludes it from default views and from current Net Worth, and I can still view archived accounts on demand and unarchive them.

> Note on AC #2 / "selectable for transactions" in AC #1: **balances are derived from the transaction ledger (AD-2), and there are no transactions yet** (Income/Expense/Transfer are Epic 3; investment cash flows are Epic 4). This story creates the **Account entity and its type semantics** and proves them by unit test + the account-management UI. Like Story 2.2's `RateAt` (built with no conversion consumer yet), the "cash balance / balance owed" figure and the transaction-form account picker are wired by their owning epics; Story 2.3 stores the `type` + `archived` flag those derivations will read and renders the per-type balance **label** with a `—` placeholder (no ledger yet). The "excluded from Net Worth" guarantee is satisfied here by storing `archived` and excluding archived rows from the default list; the Net-Worth derivation (Epic 4) filters on the same flag.

## Tasks / Subtasks

- [x] **Task 1 — `account` schema (typed, currency-scoped, archivable) (AC: #1, #2, #3)**
  - [x] Add goose migration `db/migrations/00004_accounts.sql`. Up:
    ```sql
    CREATE TABLE account (
        id         BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
        name       TEXT NOT NULL CHECK (name <> ''),
        type       TEXT NOT NULL CHECK (type IN ('cash', 'credit', 'investment')),
        currency   TEXT NOT NULL REFERENCES currency (code),
        archived   BOOLEAN NOT NULL DEFAULT FALSE,
        created_at TIMESTAMPTZ NOT NULL DEFAULT now()
    );
    ```
    Down: `DROP TABLE account;`. Follow the goose `-- +goose StatementBegin/End` block style and header-comment style used in `00002`/`00003`.
  - [x] **No `owner_id`/tenant column (AD-7)** — single-owner is an invariant, not a column. **No balance column (AD-2)** — balances derive from transactions on read; storing one would create a second source of truth. `type` is `TEXT + CHECK` (matches the project's TEXT+CHECK style, e.g. `exchange_rate`), not a PG `ENUM`. `currency` FKs `currency(code)` (the supported set is seeded in `00002`). `bigint` identity PK + `timestamptz` `created_at` per conventions. No index needed (single-user row counts are tiny).

- [x] **Task 2 — sqlc queries + generate (AC: #1, #3)**
  - [x] Add `db/query/account.sql`:
    - `CreateAccount :one` — `INSERT INTO account (name, type, currency) VALUES ($1,$2,$3) RETURNING id, name, type, currency, archived, created_at`.
    - `RenameAccount :execrows` — `UPDATE account SET name = $2 WHERE id = $1`.
    - `SetAccountArchived :execrows` — `UPDATE account SET archived = $2 WHERE id = $1`.
    - `ListActiveAccounts :many` — `SELECT id, name, type, currency, archived, created_at FROM account WHERE NOT archived ORDER BY name, id`.
    - `ListAllAccounts :many` — same columns, no filter, `ORDER BY archived, name, id`.
  - [x] `make sqlc` (the pinned `sqlc/sqlc:1.27.0` Docker image — **not** `go run`). Confirm generated types: `type`/`name`/`currency` → `string`, `archived` → `bool`, `created_at` → `pgtype.Timestamptz` (the `timestamptz` override key doesn't match — same as `exchange_rate.created_at`; the service maps `.Time`). `:execrows` returns `(int64, error)`. Commit generated files. **Don't let sqlc clobber `internal/store/pool.go`** — it owns only `db.go`/`models.go`/`querier.go`/`*.sql.go` (+ new `account.sql.go`).

- [x] **Task 3 — `service/account` use-case (AC: #1, #2, #3)**
  - [x] Add `internal/service/account/account.go`: `New(pool)`; an `AccountType` string type with constants `Cash`/`Credit`/`Investment` and an `IsValid()` (or `ParseAccountType`) check; an `Account` struct (`ID int64`, `Name string`, `Type AccountType`, `Currency money.Currency`, `Archived bool`, `CreatedAt time.Time`).
  - [x] Methods (each write in **one** `pool.Begin` transaction, AD-3):
    - `Create(ctx, name string, typ AccountType, currency money.Currency) (Account, error)` — trim `name`; validate non-empty, `typ.IsValid()`, `money.IsSupported(currency)`; insert; return the row.
    - `Rename(ctx, id int64, name string) error` — trim + validate non-empty; `RenameAccount`; if rows affected == 0 → `ErrNotFound`.
    - `SetArchived(ctx, id int64, archived bool) error` — `SetAccountArchived`; rows == 0 → `ErrNotFound`.
    - `List(ctx, includeArchived bool) ([]Account, error)` — `includeArchived` ? `ListAllAccounts` : `ListActiveAccounts`; map rows via a `toAccount` helper (`CreatedAt: row.CreatedAt.Time`).
  - [x] Typed errors: `ErrEmptyName`, `ErrInvalidType`, `ErrUnsupportedCurrency`, `ErrNotFound` (the service is the validation authority; DB CHECK/FK are backstops). Reads use `store.New(s.pool)`; writes use `store.New(tx)`.
  - [x] DB-gated integration test `account_test.go` (skips without `DATABASE_URL`/`TEST_DATABASE_URL`): create one of each type (`cash` USD, `credit` USD, `investment` BRL) → rows returned with `CreatedAt` set, `Archived=false`; reject empty/whitespace name, invalid type, unsupported currency; `Rename` happy path + `Rename(missingID)` → `ErrNotFound`; `SetArchived(id,true)` then `List(false)` excludes it and `List(true)` includes it (and `SetArchived(id,false)` restores it to the active list).

- [x] **Task 4 — Accounts management page (AC: #1, #2, #3)**
  - [x] Replace the `/accounts` **ComingSoon** placeholder with a real authenticated page (a templ page in the shell, active nav key `accounts`). `GET /accounts` lists active accounts (name, type, base currency, and a per-type **balance label** — "Cash balance" for cash/investment, "Balance owed" for credit — with a `—` value placeholder since no ledger exists yet) plus a "Create account" form (name text input, type `<select>` of cash/credit/investment, currency `<select>` of `money.Supported()`). Each row has a **rename** control (inline form: hidden `id`, name input) and an **archive** button. A `?show=archived` query param (a "Show archived" link/toggle) switches the list to include archived rows, where archived rows show an **unarchive** button instead.
  - [x] Routes (flat, same style as `/settings`, `/exchange-rates`; same-origin form POSTs pass `CrossOriginProtection`):
    - `GET  /accounts` — list (reads `?show=archived` → `List(true)`, else `List(false)`).
    - `POST /accounts` — create (`name`, `type`, `currency`); on validation error re-render the page with the message + a 400; on success redirect 303 to `/accounts`.
    - `POST /accounts/rename` — rename (`id`, `name`); redirect 303 (re-render with message on error).
    - `POST /accounts/archive` — set archived (`id`, `archived` = `"true"`/`"false"`); redirect 303 (preserve the `show` view if practical, else redirect to `/accounts`).
  - [x] Add an `Accounts` interface to `http.Deps` (`Create`, `Rename`, `SetArchived`, `List`; the interface returns `account.Account` — `http → service` import is allowed, AD-1, exactly as `ExchangeRates` returns `exchangerate.Rate`). Wire `account.New(pool)` in `cmd/server/main.go`. Add an `AccountRow` view struct to `web/shell.go` (`ID int64`, `Name`, `Type`, `Currency`, `BalanceLabel string`, `Archived bool`) — the handler maps service `Account`s to `AccountRow`s (the per-type label is a presentation string, **not** financial math; AD-10 is not violated). **Keep the five-item primary nav unchanged** (UX-DR1) — `/accounts` is already nav item #4; this story fills it in (it is NOT linked from `/settings`, unlike `/exchange-rates`).
  - [x] `make generate css` after templ/Tailwind edits; commit `*_templ.go` + rebuilt `web/static/css/app.css`.

- [x] **Task 5 — Tests, verify, docs**
  - [x] `go build`/`go vet`/`go test ./...` + `make nofloat` clean (DB-gated tests skip without a DB). **`nofloat` stays green trivially** — accounts store no money (no NUMERIC columns); balances are derived later.
  - [x] http test (stub `Accounts`, mirroring `stubExchangeRates`): unauth `GET /accounts` → 303 `/login`; authed `GET` shows the create form + any rows + the type labels; authed `POST /accounts` (valid) → 303 and the row appears; `POST /accounts/rename` changes the name; `POST /accounts/archive` with `archived=true` drops it from the default list and `GET /accounts?show=archived` reveals it; an invalid create (empty name) → 400 and re-renders without crashing.
  - [x] **Regression — update `router_test.go`:** `TestNavTargetAuthed` currently GETs `/accounts` and asserts `"Coming soon"`. `/accounts` is no longer a placeholder — point that test at a still-placeholder target (`/investments`) or assert the real Accounts page content. Confirm `TestNavTargetRequiresAuth` (which includes `/accounts`) still passes (the route stays auth-gated). Add the stub `Accounts` to `testDeps`.
  - [x] Live smoke (compose db + run, logged in): create a cash USD, a credit USD, and an investment BRL account; see all three listed with correct type + currency + label; rename one; archive one → it disappears from the default list and reappears under "Show archived"; unarchive it; confirm everything persists across reload.
  - [x] Update `README.md` briefly (accounts: cash/credit/investment, base currency, rename, archive; archived excluded from default views and from Net Worth, retained for history; balances derive from the ledger in later epics).

## Dev Notes

### Scope boundary — what this story does NOT do (read first)

- **NO balance computation / no Net Worth** — there is no transaction ledger yet (Epic 3 cash/credit, Epic 4 investments). The per-type "cash balance / balance owed" is rendered as a **label with a `—` placeholder**. Real balances are derived on read by `domain` functions in Epic 3/4 (AD-2, AD-10).
- **NO transaction-form account picker** — transaction forms are Epic 3. This story only persists accounts so `ListActiveAccounts` is the future selection source.
- **NO account deletion** — accounts are archived, never deleted (archiving "preserves history"). No hard-delete path.
- **NO name uniqueness constraint** — FR-1 does not require unique account names (unlike Security symbols in Story 4.1). Don't add one.
- **NO `owner_id`/tenant column, NO balance column** — see AD-7 / AD-2 below.
- **NO currencies/rates changes** — those shipped in 2.1/2.2.

### Architecture invariants this story must honor

- **AD-2 — the ledger is the single source of truth; balances derive on read.** The `account` table stores **no balance**. Quantity/cash/owed figures are computed from `Transaction` rows by `domain` later. Storing a balance here would be a second writer that drifts. [Source: ARCHITECTURE-SPINE.md#AD-2]
- **AD-7 — single owner, no tenant column.** No `owner_id` on `account`; single-tenancy is an invariant. Every `/accounts*` route is behind `requireAuth`. [Source: ARCHITECTURE-SPINE.md#AD-7]
- **AD-3 — one DB transaction per use-case.** `Create`/`Rename`/`SetArchived` each wrap their write in a single `pool.Begin` tx. [Source: ARCHITECTURE-SPINE.md#AD-3]
- **AD-5 — store native currency.** Each account's base `Currency` is stored and never overwritten by the Display Currency. [Source: ARCHITECTURE-SPINE.md#AD-5]
- **AD-1 — layering.** `http → service → store`; `http` defines the `Accounts` interface and imports `service/account` for the `Account` type; `main` injects. The store never reaches up. Other epics that need account data read it via `store` (the persistence row), **not** by importing `service/account`, so no `service → service` dependency is introduced. [Source: ARCHITECTURE-SPINE.md#AD-1]
- **AD-10 — `http` does no financial math.** The per-type balance **label** is a static presentation string keyed off `type` (no arithmetic). When real balances arrive, the number comes from a single canonical `domain` function. [Source: ARCHITECTURE-SPINE.md#AD-10]
- **Conventions:** `bigint` identity PK; ISO-4217 currency code (FK to `currency`); `timestamptz` UTC `created_at`; **archived accounts excluded from both Portfolio total and Net Worth in current views, retained for history**. [Source: ARCHITECTURE-SPINE.md#Consistency Conventions]

### Where the Account type lives (decision)

`Account` + `AccountType` live in **`internal/service/account`**, mirroring Story 2.2's accepted precedent (`Rate` lives in `service/exchangerate`, and `http` imports it for the `Deps` interface). `internal/domain` is still just a doc placeholder and is reserved for the **pure derived-figure functions** (balance, Net Worth, holdings) that AD-10 says have exactly one home — those land in Epic 4 and take plain values (currency, type string, amounts) as inputs. The string values `'cash'|'credit'|'investment'` are the durable contract (DB CHECK + `AccountType` constants). If a shared typed enum is later needed across packages, promote it to `domain` then; do not pre-populate `domain` now.

### Previous-story intelligence (2.1 + 2.2 + Epic 1) — load-bearing

[Source: 2-2-exchange-rates.md (Dev Agent Record); 2-1-currencies-display-currency.md; [[financas-epic1-progress]]]

- **The migration → `db/query/*.sql` → `make sqlc` → `service/<x>` → `http` Deps interface + templ page pattern is established (2.1/2.2). Follow it exactly.** This story adds the 4th migration; the `currency` table + `money.Supported()`/`IsSupported()` already exist.
- **sqlc runs via the pinned `sqlc/sqlc:1.27.0` Docker image** (`make sqlc`), **not** `go run` (the source build fails on this macOS SDK). Generated files are committed.
- **Store file ownership:** `db.go`/`models.go`/`querier.go`/`*.sql.go` are sqlc-generated; **`internal/store/pool.go` is hand-written** (pgx pool + goose runner + decimal codec) — keep hand code out of the sqlc files and don't let `make sqlc` overwrite `pool.go`. `store.New(pool)` for reads; `store.New(tx)` (from `pool.Begin`) for writes.
- **Router:** `NewRouter(Deps)`; protected chi group with `requireAuth`; `renderPage`/`shellData` fetch the Display Currency for the header (nil-`Settings`-safe). `Deps` currently has `Sessions, Auth, Ready, Settings, ExchangeRates, OwnerName` — **add `Accounts`**. Preserve `/healthz`, `/readyz`, `/login`, `/logout`, CSRF, `/static/*`, `/settings`, `/exchange-rates`, and the shell pages. **`/accounts` currently renders `web.ComingSoon` via `renderPage` — replace that single registration with the real create/list/rename/archive handlers.**
- **Existing handler shape to copy:** `exchangeRatesForm`/`exchangeRatesSubmit`/`renderExchangeRates` in `router.go` are the exact template — form parse → service call → 303 redirect on success → re-render with `errMsg` + non-200 on error. Reuse `shellData(deps, ctx, "accounts")` for the active nav (use key `"accounts"`, not `"settings"`).
- **templ/view shape to copy:** `ExchangeRatesPage` (form + table over a `[]web.RateRow`) in `web/pages.templ`; add `AccountsPage` + `web.AccountRow` the same way. Tailwind classes already in use: `rounded-card`, `bg-white`, `p-6`, `shadow-card`, `text-muted`, `text-loss`, `text-accent`. `make generate css` regenerates `web/static/css/app.css` (committed); `templ` via `go tool templ`.
- **http test shape to copy:** `stubExchangeRates` + `TestExchangeRates*` in `router_test.go`; add `stubAccounts` + `TestAccounts*` and register the stub in `testDeps`.
- **money:** `Currency`/`USD`/`BRL`/`Supported()`/`IsSupported()` in `internal/money`. `make nofloat` guards `internal/{money,domain,service,store}` against `float32/64`.
- **Build with `GOTOOLCHAIN=local`** (go.mod pins 1.26.3; installed 1.26.3 — avoids a toolchain auto-download). Repo HAS commits; `baseline_commit` is the real HEAD `1a38e2f`. Local DB: `docker compose up -d db` → host **port 5433** (a native Postgres owns 5432); `DATABASE_URL=postgres://financas:financas@localhost:5433/financas?sslmode=disable`. DB-gated tests skip without `DATABASE_URL`/`TEST_DATABASE_URL`. On role errors, `docker compose down -v` and recreate.

### sqlc / pgx specifics for this story

- **No new `sqlc.yaml` overrides needed** — `account` has no NUMERIC or DATE columns. `name`/`type`/`currency` → `string`, `archived` → `bool`. `created_at` (timestamptz) generates as `pgtype.Timestamptz` (the existing override key only catches `numeric`/`date`); map `.Time` in the service `toAccount`, exactly as `exchangerate.toRate` does for `CreatedAt`.
- **`:execrows`** returns `(int64, error)` (rows affected) under pgx/v5 — use it for `Rename`/`SetArchived` to detect a missing id (rows == 0 → `ErrNotFound`). [Knowledge cutoff: Jan 2026]
- **`emit_empty_slices: true`** is set, so `List*` returns a non-nil empty slice when there are no rows.

### Project Structure Notes

New: `db/migrations/00004_accounts.sql`, `db/query/account.sql`, sqlc output (`internal/store/account.sql.go` + regenerated `models.go`/`querier.go`/`db.go`), `internal/service/account/account.go` (+ `account_test.go`), `web/pages.templ` `AccountsPage` (+ regenerated `pages_templ.go`), `web/shell.go` `AccountRow`. Updated: `internal/http/router.go` (+`Accounts` iface, `Deps.Accounts`, replace the `/accounts` ComingSoon registration with create/list/rename/archive routes + handlers) + `router_test.go` (stub + tests, fix `TestNavTargetAuthed`), `cmd/server/main.go` (wire `account.New(pool)`), `README.md`. No structural variance — same shape as 2.2.

### Testing standards

- `service/account`: DB-gated integration covering create (each type, both currencies), all three validations (empty name, invalid type, unsupported currency), rename (+ not-found), and archive→list-exclusion / unarchive→list-inclusion. This is the heart of the story — test it thoroughly.
- `http`: httptest with a stub `Accounts` — auth-gating, list+create render, rename, archive/unarchive view toggle, and the invalid-create sad path. Update the `/accounts` nav regression test.
- `go test ./...` green with no DB; `go vet` + `make nofloat` clean (no float, no money columns introduced).

### References

- [Source: epics.md#Story 2.3] — acceptance criteria; [Source: epics.md FR-1] — create/rename/archive, type semantics, archived retention
- [Source: ARCHITECTURE-SPINE.md#AD-2] — ledger is source of truth; balances derived on read (no balance column)
- [Source: ARCHITECTURE-SPINE.md#AD-7] — single owner, no tenant column
- [Source: ARCHITECTURE-SPINE.md#AD-3 / #AD-5 / #AD-1 / #AD-10] — one tx; native currency; layering; http does no math
- [Source: ARCHITECTURE-SPINE.md#Consistency Conventions] — bigint identity, ISO-4217 FK, timestamptz, archived excluded from Net Worth/Portfolio
- [Source: 2-2-exchange-rates.md] — migration→sqlc→service→http pattern, sqlc-via-Docker, store file ownership, router/Deps shape, handler + templ + test templates, timestamptz `.Time` mapping

## Dev Agent Record

### Agent Model Used

claude-opus-4-8[1m] (Opus 4.8, 1M context)

### Debug Log References

- `make sqlc` (Docker image) — generated `internal/store/account.sql.go` + `Account` model (`type`/`name`/`currency` → `string`, `archived` → `bool`, `created_at` → `pgtype.Timestamptz`); `:execrows` for `RenameAccount`/`SetAccountArchived` returns `(int64, error)`. Param structs matched the service exactly (no rework needed). No new `sqlc.yaml` overrides (account has no NUMERIC/DATE columns).
- `go build`/`go vet`/`make nofloat` clean. Full suite green both **with** a DB and **without** (the `service/account` integration test `t.Skip`s when no `DATABASE_URL`/`TEST_DATABASE_URL`).
- Live DB: `account.TestAccount` PASS — create (each type, both currencies), trimming, validation (empty name / invalid type / unsupported currency), rename (+ not-found), archive→active-list-exclusion / include-archived inclusion / unarchive→restore.
- Live HTTP smoke (server :8095 + db :5433, logged in owner/financas): created cash USD "Checking", credit USD "Visa", investment BRL "Brokerage" (all 303 → `/accounts`); list rendered all three with correct type + currency + per-type balance label ("Cash balance" / "Balance owed"). Renamed "Checking" → "Main Checking" (persisted). Archived "Visa": absent from the default list, present under `?show=archived` with an "archived" badge + Unarchive button; unarchived → back in the active list. DB query confirmed `name/type/currency/archived` persisted across reload.

### Completion Notes List

All three acceptance criteria verified (unit + live DB + live HTTP):
- **AC1 — create, persisted, selectable later:** `account` schema (typed, currency FK, bigint identity); `Create` validates + inserts in one tx; rows appear in the list and persist (DB-verified). Base currency stored natively, never overwritten by the Display Currency (AD-5).
- **AC2 — type carries balance semantics:** `AccountType` (cash/credit/investment); the page renders the per-type balance **label** ("Cash balance" for cash/investment, "Balance owed" for credit) with a `—` placeholder. **No balance is stored** — it derives from the ledger in Epic 3/4 (AD-2). This mirrors Story 2.2's `RateAt`-with-no-consumer pattern.
- **AC3 — rename + archive:** `Rename` preserves identity/history; `SetArchived(true/false)` archives/unarchives; the default list excludes archived rows, `?show=archived` includes them; the Net-Worth derivation (Epic 4) will filter the same `archived` flag.

Decisions / variances (intentional):
- **`Account` + `AccountType` live in `service/account`** (not `domain`), mirroring the accepted 2.2 precedent (`Rate` in `service/exchangerate`). `domain` stays reserved for the pure derived-figure functions (balance, Net Worth) that arrive in Epic 4 and take plain values; other epics read account rows via `store`, so no `service → service` import is introduced (AD-1).
- **No balance column and no tenant column** — balances derive on read (AD-2); single-owner is an invariant, not a column (AD-7).
- **`created_at` stays `pgtype.Timestamptz`** in generated code (the timestamptz override key doesn't match); the service maps `.Time` — same handling as `exchangerate`.
- **`/accounts` replaced the ComingSoon placeholder** — it is nav item #4, so the five-item primary nav (UX-DR1) is unchanged. Flat routes (`POST /accounts`, `/accounts/rename`, `/accounts/archive`) match the existing `/settings` / `/exchange-rates` style; same-origin form POSTs pass `CrossOriginProtection`. The archived view is preserved across rename/archive redirects via a `show` field.
- **Regression fixed:** `TestNavTargetAuthed` previously asserted `/accounts` was a "Coming soon" placeholder; it now targets `/investments` (still a placeholder). `nofloat` stays green trivially (accounts store no money).

Reviewer notes: no `sprint-status.yaml` → status tracked in this file only. `baseline_commit` is the real SHA `1a38e2f` (HEAD before this story). Changes staged but **not committed** (the owner commits/pushes).

### File List

New:
- `db/migrations/00004_accounts.sql`, `db/query/account.sql`
- `internal/store/account.sql.go` (sqlc-generated; `models.go`/`querier.go` regenerated)
- `internal/service/account/account.go`, `internal/service/account/account_test.go`

Modified:
- `internal/http/router.go` (`Accounts` iface, `Deps.Accounts`, `/accounts` create/list/rename/archive routes + handlers, replacing the ComingSoon registration) + `internal/http/router_test.go` (stub `Accounts`, account tests, `TestNavTargetAuthed` regression fix)
- `cmd/server/main.go` (wire `account.New(pool)`)
- `web/pages.templ` `AccountsPage` (+ regenerated `web/pages_templ.go`), `web/shell.go` (`AccountRow` + `accountID` helper), rebuilt `web/static/css/app.css`
- `README.md` (Accounts section)

## Change Log

| Date | Change |
| --- | --- |
| 2026-06-28 | Story 2.3 drafted (create-story): `account` schema (typed cash/credit/investment, base currency FK, archivable, no balance/tenant column); `service/account` Create/Rename/SetArchived/List; `/accounts` management page replacing the ComingSoon placeholder. Status → ready-for-dev. |
| 2026-06-28 | Story 2.3 implemented (dev-story): migration `00004_accounts` + sqlc queries; `service/account` (one-tx-per-write, AD-3); `/accounts` create/rename/archive page + handlers; `Accounts` Deps interface wired in `main`. All 3 ACs verified (unit + live DB + live HTTP smoke); `go build/vet/test ./...` + `make nofloat` green. Status → review. |
