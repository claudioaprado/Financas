---
baseline_commit: ce02dcf
---

# Story 4.1: Manage securities

Status: review

<!-- Note: Validation is optional. Run validate-create-story for quality check before dev-story. -->

## Story

As the owner,
I want to define the securities I own,
so that I can record trades against them.

## Acceptance Criteria

From `epics.md` → Epic 4 → Story 4.1 (realizes FR-3). **Given** I am authenticated, **When** I add a Security (symbol, name, type, quote Currency), **Then**:

1. The Security is created and persisted with its **symbol, name, type (stock | ETF | fund | other), and quote Currency** (FR-3).
2. A **duplicate symbol** within the security list is **prevented at entry** (rejected with a clear message; matching is case-insensitive so `petr4` and `PETR4` are the same symbol).
3. The Security is **available to be selected** for Buy/Sell/Dividend transactions and Holdings — i.e. it is listed and readable by id/symbol for downstream stories (Story 4.2 consumes it).

> **Scope (this story only):** add the `security` entity, its store queries (create + read-by-id + list), a `service/security` use-case (create + list, duplicate-symbol prevention, type/currency validation), and a `/securities` management page (create form + list). This is **reference-data management**, the direct analog of categories (3.4) and exchange rates (2.2) — so it is linked from `/settings` and the five-item primary nav is unchanged. **NOT in this story:** investment transactions / Buy-Sell-Dividend, derived Holdings or cost basis, prices, valuation (those are Stories 4.2/4.3/4.4). **No edit/delete of a Security yet** — there are no references to guard against until 4.2 adds `transaction.security_id`; deferring avoids a delete path that becomes unsafe later. Symbol is immutable once created (it is the dedup identity).

## Tasks / Subtasks

- [x] **Task 1 — `security` schema (AC: #1, #2)**
  - [x] Add goose migration `db/migrations/00009_securities.sql`, mirroring the `account` migration style (`-- +goose StatementBegin/End` per statement). Up:
    ```sql
    CREATE TABLE security (
        id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
        symbol         TEXT NOT NULL CHECK (symbol <> '') UNIQUE,
        name           TEXT NOT NULL CHECK (name <> ''),
        type           TEXT NOT NULL CHECK (type IN ('stock', 'etf', 'fund', 'other')),
        quote_currency TEXT NOT NULL REFERENCES currency (code),
        created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
    );
    ```
    Down: `DROP TABLE security;`.
  - [x] `type` is stored **lowercase** (`'stock','etf','fund','other'`) to match the `account.type` CHECK convention; the UI renders `ETF` uppercase as a display label. `quote_currency` is a `TEXT` FK to `currency(code)` exactly like `account.currency` (currency table seeded USD/BRL in `00002`). The `UNIQUE` on `symbol` is the DB backstop for duplicate prevention (the service is the primary authority, AD-4/conventions). Store the symbol already **normalized** (trim + uppercase) so the UNIQUE index enforces case-insensitive uniqueness.

- [x] **Task 2 — sqlc: security queries (AC: #1, #2, #3)**
  - [x] Add `db/query/security.sql`:
    - `CreateSecurity :one` — `INSERT INTO security (symbol, name, type, quote_currency) VALUES ($1,$2,$3,$4) RETURNING id, symbol, name, type, quote_currency, created_at;`
    - `GetSecurity :one` — by `id` (full row). Needed by Story 4.2 to validate a trade's security (store-not-service rule).
    - `GetSecurityBySymbol :one` — by `symbol` (full row); used for the pre-insert duplicate check.
    - `ListSecurities :many` — `... ORDER BY symbol, id;`
  - [x] Run `make sqlc` (pinned `sqlc/sqlc:1.27.0` Docker image — **NOT** `go run`). Confirm the generated `store.Security` model (`type` → `string`, `quote_currency` → `string`, `created_at` → `pgtype.Timestamptz`). Commit the generated `internal/store/security.sql.go` (+ regenerated `models.go`/`querier.go`). Keep hand-written code out of the sqlc files (`db.go`/`models.go`/`querier.go`/`*.sql.go`).

- [x] **Task 3 — `service/security` use-case (AC: #1, #2, #3)**
  - [x] Add `internal/service/security/security.go`, mirroring `service/account`:
    - A `SecurityType string` type with consts `Stock`/`ETF`/`Fund`/`Other` (values `"stock"`,`"etf"`,`"fund"`,`"other"`) and an `IsValid()` method (switch).
    - A `Security` struct: `ID int64`, `Symbol string`, `Name string`, `Type SecurityType`, `QuoteCurrency money.Currency`, `CreatedAt time.Time`.
    - `New(pool *pgxpool.Pool) *Service`.
    - `Create(ctx, symbol, name string, typ SecurityType, quote money.Currency) (Security, error)`: normalize `symbol = strings.ToUpper(strings.TrimSpace(symbol))`; `name = strings.TrimSpace(name)`; validate non-empty symbol (`ErrEmptySymbol`) and name (`ErrEmptyName`), `typ.IsValid()` (`ErrInvalidType`), `money.IsSupported(quote)` (`ErrUnsupportedCurrency`). Then **one** `pool.Begin` tx (AD-3): insert via `store.New(tx).CreateSecurity`. On a unique-violation Postgres error (`pgconn.PgError` code `23505`), return `ErrDuplicateSymbol` (same `errors.As` pattern `service/category.Delete` uses for `23503`). Commit; return `toSecurity(row)`.
    - `List(ctx) ([]Security, error)` (via `store.ListSecurities`).
    - `Get(ctx, id int64) (Security, error)` — `pgx.ErrNoRows` → `ErrNotFound` (mirror `account.Get`).
    - Typed errors: `ErrEmptySymbol`, `ErrEmptyName`, `ErrInvalidType`, `ErrUnsupportedCurrency`, `ErrDuplicateSymbol`, `ErrNotFound`. Package doc comment in the `service/account` / `service/category` house style (what it owns; one tx per write AD-3; symbol normalized + unique; quote currency stored natively AD-5).
  - [x] DB-gated tests `security_test.go`: create a security; **duplicate symbol rejected** (insert `PETR4`, then `petr4` ⇒ `ErrDuplicateSymbol`); invalid type ⇒ `ErrInvalidType`; unsupported currency ⇒ `ErrUnsupportedCurrency`; empty symbol/name rejected; `List` returns rows ordered by symbol; `Get` by id + `ErrNotFound` on a missing id. (Gate on `DATABASE_URL`/`TEST_DATABASE_URL`, skip without — existing pattern.)

- [x] **Task 4 — `/securities` page + wiring (AC: #1, #2, #3)**
  - [x] Add a `Securities` interface to `http.Deps` (in `internal/http/router.go`, alongside `Categories`): `Create(ctx, symbol, name string, typ security.SecurityType, quote money.Currency) (security.Security, error)` and `List(ctx) ([]security.Security, error)`. Add a `Securities Securities` field to `Deps` and inject `security.New(pool)` in `cmd/server/main.go`. (`http → service` import is allowed, AD-1; same as `Categories`.)
  - [x] Register authenticated routes inside the `pr` group (mirror categories, around router.go:170): `pr.Get("/securities", securitiesPage(deps))` and `pr.Post("/securities", securitiesCreate(deps))`. Add a `securitiesPage`/`securitiesCreate` handler pair + a `renderSecurities(deps, w, req, errMsg, code)` helper (mirror `renderCategories`): GET lists via `deps.Securities.List`; POST parses `symbol`, `name`, `type`, `quote_currency`, calls `Create`, on error re-renders the page with the message + a 400 (`ErrDuplicateSymbol` → "A security with that symbol already exists."), on success `http.Redirect` to `/securities` (303).
  - [x] Add `web.SecuritiesPage(data ShellData, rows []SecurityRow, errMsg string)` templ in `web/pages.templ` + view structs in `web/shell.go` (`SecurityRow{Symbol, Name, TypeLabel, QuoteCurrency string}`, and a `SecurityTypeOption{Value, Label string}` list for the `<select>`). The create form: text `symbol`, text `name`, a `type` `<select>` (Stock / ETF / Fund / Other — value lowercase, label display-cased), and a `quote_currency` `<select>` populated from `money.Supported()` (same as the account create form). The list shows symbol, name, type label, quote currency. Render `TypeLabel` mapping `etf`→`ETF`, else title-case.
  - [x] **Link `/securities` from `/settings`** (like `/exchange-rates` and `/categories`); the five-item primary nav (Dashboard · Investments · Transactions · Accounts · Analytics) is **unchanged**. Use `shellData(deps, req.Context(), "settings")` as the active-nav key (matches the categories/exchange-rates pages).
  - [x] `make generate css` (templ via `go tool`, Tailwind via `npx`) and commit the regenerated `web/pages_templ.go` + `web/static/css/app.css`.

- [x] **Task 5 — Tests, verify, docs (AC: all)**
  - [x] Update `internal/http/router_test.go`: add a stub `Securities` (in-memory create/list with duplicate-symbol rejection mirroring the real error) and register it in `testDeps`. Add a test: create a security on `/securities`, see it listed; submitting a duplicate symbol re-renders with the error (400) and does not add a second row; invalid currency/type rejected.
  - [x] `GOTOOLCHAIN=local go build ./... && go vet ./... && go test ./...` green (DB-gated tests skip without a DB). `make nofloat` stays green (the `security` package has no monetary arithmetic — quote currency is just a code; no `decimal`/float involved).
  - [x] Live smoke (compose db on :5433 + run server, login `owner`/`financas`): open `/securities` (via the Settings link); create `PETR4 / Petrobras PN / stock / BRL` and `VOO / Vanguard S&P 500 ETF / etf / USD`; confirm both list; try creating `petr4` again ⇒ rejected with the duplicate message and no new row; try an unsupported currency ⇒ rejected; persistence across reload.
  - [x] Update `README.md` briefly (securities: symbol/name/type/quote currency; duplicate symbols prevented case-insensitively; managed from Settings; used by investment transactions in later stories).

## Dev Notes

### Scope boundary — what this story does NOT do (read first)

- **NO investment transactions** (Buy/Sell/Dividend), **no `transaction.security_id`**, no derived Holdings, cost basis, prices, or valuation — those are Stories 4.2/4.3/4.4. This story only stands up the Security registry and makes it selectable/readable.
- **NO edit or delete of a Security** — nothing references a security yet (the FK arrives in 4.2), so there is no in-use state to guard. Symbol is immutable (it is the dedup identity). Revisit edit/guarded-delete when references exist.
- **NO `money.Money`/decimal math** — a Security carries a *quote Currency code*, not an amount. Do not pull amounts into this package; `make nofloat` must stay green.
- **Five-item nav unchanged** — `/securities` is reference-data management linked from `/settings` (exactly like `/categories` 3.4 and `/exchange-rates` 2.2). The `Investments` nav item stays `ComingSoon`; the Investments area is built out by 4.2 (holdings) and 4.4 (portfolio/net worth).

### Architecture invariants this story must honor

- **FR-3 — securities.** Symbol, name, type (stock|ETF|fund|other), quote currency; duplicate symbol prevented at entry. [Source: epics.md#Story 4.1; epics.md FR-3]
- **AD-1 — layering.** New `service/security` reads/writes via `store.New(pool)`; `http` defines the `Securities` interface and imports `service/security` + `money`; `cmd/server` injects `security.New(pool)`. No service→service import. [Source: ARCHITECTURE-SPINE.md#AD-1]
- **AD-3 — one tx per use-case.** `Create` wraps its insert in a single `pool.Begin` transaction. [Source: ARCHITECTURE-SPINE.md#AD-3]
- **AD-4 / conventions — type & currency.** `bigint` identity PK; ISO-4217 currency code FK to `currency(code)`; lowercase `type` CHECK (mirrors `account.type`). The service is the validation authority; DB `UNIQUE`/`CHECK`/FK are the backstop. [Source: ARCHITECTURE-SPINE.md#Consistency Conventions]
- **AD-5 — native currency.** The quote currency is stored as the security's own code; nothing is converted here. [Source: ARCHITECTURE-SPINE.md#AD-5]
- **Spine ER model** already declares `CURRENCY ||--o{ SECURITY : "quote"` and `SECURITY` referenced by `TRANSACTION`/`PRICE`/`HOLDING` — this story creates the `SECURITY` table those later relations hang off. [Source: ARCHITECTURE-SPINE.md#Structural Seed]

### Decided for Epic 4 (downstream context; not all used in 4.1)

These were resolved with the owner before coding Epic 4 (see memory `financas-epic4-decisions`). 4.1 only needs to know the **same-currency-only** rule exists, because it shapes how 4.2 will pair a Security's `quote_currency` with an investment Account's `currency`:
- **Same-currency-only (confirmed real-portfolio-safe):** in 4.2, a Security may only be traded in an Account whose base currency == the Security's `quote_currency`; cross-currency trades are rejected at entry. → In 4.1, just make `quote_currency` a clean, queryable field; **do not** add any account-pairing constraint yet.
- Fees reduce proceeds only; oversell rejected via exact `NUMERIC(28,10)`; zero-crossing = exact basis wipe; Net Worth shows a partial total + warning when an FX rate is missing. (All for 4.2/4.4.)

### Previous-story intelligence (3.x + 2.x) — load-bearing

[Source: 3-4-categories-classification.md; 2-3-create-manage-accounts.md; [[financas-epic1-progress]]]

- **Mirror the entity pattern exactly:** migration → `db/query/*.sql` → `make sqlc` (pinned Docker image) → `service/<x>` (`store.New(pool)`; one `pool.Begin` tx per write) → `http` interface + page → wire in `cmd/server/main.go`. `service/account` is the closest template for `service/security` (has the currency FK + `money.IsSupported` validation + `Get`/`List` + `toX` mapper); `service/category` is the template for the **23505 → typed error** mapping (it does the same with 23503 for FK violations).
- **Currency FK:** `account.currency TEXT NOT NULL REFERENCES currency(code)` — copy this for `security.quote_currency`. Validate in the service with `money.IsSupported` (the allow-list is `money.Supported()` = USD, BRL). [Source: db/migrations/00004_accounts.sql; internal/service/account/account.go:90]
- **sqlc lesson (general):** generated files are committed; run `make sqlc` (Docker), never `go run`. This story **adds a brand-new table** (not a column on `transaction`), so the 3.4/3.6 "append the new column to every full-row SELECT/RETURNING of `transaction`" hazard does **not** apply here — `transaction` queries are untouched. Just make the new `security.sql` SELECT/RETURNING lists internally consistent (same column order in CreateSecurity RETURNING, GetSecurity, GetSecurityBySymbol, ListSecurities) so they all reuse the `store.Security` model.
- **http wiring shape:** `Deps` holds one interface per service; the authenticated routes live in the `pr` group; settings-linked pages use `shellData(deps, ctx, "settings")`. `renderCategories` (router.go:660) is the exact template for `renderSecurities`. [Source: internal/http/router.go]
- **Environment:** build/test with `GOTOOLCHAIN=local` (go.mod pins 1.26.3). Local DB via `docker compose up -d db` → host port **5433**; `DATABASE_URL=postgres://financas:financas@localhost:5433/financas?sslmode=disable`. DB-gated tests skip without `DATABASE_URL`/`TEST_DATABASE_URL`. Dev login `owner`/`financas` (argon2id in docker-compose). HTMX is vendored + wired app-wide (no JS needed for this story — a plain form POST is fine, matching categories/exchange-rates). `baseline_commit` is the real HEAD `ce02dcf`. **Commit + push to `main` when done** (one commit per story, trunk-based — owner's standing instruction).

### Project Structure Notes

New files:
- `db/migrations/00009_securities.sql`
- `db/query/security.sql`
- `internal/store/security.sql.go` (sqlc-generated; `models.go`/`querier.go` regenerated)
- `internal/service/security/security.go` (+ `security_test.go`)
- `web` `SecuritiesPage` (regenerated `web/pages_templ.go`) + view structs in `web/shell.go` (`SecurityRow`, `SecurityTypeOption`)

Modified files:
- `internal/http/router.go` (`Securities` interface + `Deps` field, `/securities` GET/POST routes + `securitiesPage`/`securitiesCreate`/`renderSecurities`, Settings link)
- `internal/http/router_test.go` (stub `Securities` + test)
- `cmd/server/main.go` (wire `security.New(pool)`)
- `web/pages.templ` (SecuritiesPage + Settings link), rebuilt `web/static/css/app.css`
- `README.md` (securities section)

No conflicts with the unified structure — this adds a new sibling `service/security` package and a new table, following the established onion layout.

### Testing standards

- `service/security`: DB-gated — create; duplicate-symbol rejection (case-insensitive); invalid type/unsupported currency/empty fields rejected; `List` ordering; `Get` + `ErrNotFound`.
- `http`: stub-backed — create via `/securities`, list, duplicate re-renders with error (400, no second row), invalid input rejected.
- `go test ./...` green with **no** DB (DB-gated tests skip); `go vet` + `make nofloat` clean.
- No `domain` change in this story (no derived figure yet — Holdings/valuation are 4.2/4.4), so no `domain` unit test is added here.

### References

- [Source: epics.md#Story 4.1] — acceptance criteria; [Source: epics.md FR-3] — securities: symbol/name/type/quote currency, prevent duplicate symbols
- [Source: ARCHITECTURE-SPINE.md#AD-1 / #AD-3 / #AD-4 / #AD-5] — layering; one tx; decimal/identity/currency conventions; native currency
- [Source: ARCHITECTURE-SPINE.md#Structural Seed] — ER model (`CURRENCY ||--o{ SECURITY`), `security` is the table later FR-4/FR-5/FR-9 relations reference
- [Source: db/migrations/00004_accounts.sql; internal/service/account/account.go] — currency-FK + `money.IsSupported` validation template
- [Source: 3-4-categories-classification.md; internal/service/category/category.go:132] — entity CRUD pattern + 23xxx PgError → typed error mapping
- [Source: internal/http/router.go] — `Deps`/interface/route/`renderCategories` wiring template
- [Source: memory `financas-epic4-decisions`] — Epic 4 decisions (same-currency-only shapes 4.2, not 4.1)

## Dev Agent Record

### Agent Model Used

claude-opus-4-8[1m] (Opus 4.8, 1M context)

### Debug Log References

- `make sqlc` (pinned `sqlc/sqlc:1.27.0` Docker image) generated `store.Security` (`Type`/`QuoteCurrency` → `string`, `CreatedAt` → `pgtype.Timestamptz`) and the four query methods — no bespoke row types (the `transaction` queries were untouched, so the 3.4/3.6 column-order hazard didn't apply). `make migrate` applied `00009` cleanly (goose → version 9).
- `securityTypeLabel` initially used `strings.Title` (deprecated); replaced with an explicit switch over the four known types to keep `go vet`/lint clean and avoid the `golang.org/x/text/cases` dependency.
- Build / vet / `gofmt -l` clean. Full suite green with **and** without a DB (`security` DB-gated test skips without `DATABASE_URL`). `make nofloat` green (the `security` package carries no monetary arithmetic).
- Live DB: `service/security.TestSecurity` PASS (normalized symbol round-trip, case-insensitive `ErrDuplicateSymbol`, `ErrInvalidType`/`ErrUnsupportedCurrency`/`ErrEmptySymbol`/`ErrEmptyName`, `Get` + `ErrNotFound`, symbol-ordered `List`).
- Live HTTP smoke (server :8099 + db :5433, owner/financas): login 303; created `petr4` ⇒ stored + displayed as **PETR4** (normalization); created `VOO` (etf) ⇒ displayed with **ETF** label; **duplicate `PETR4` ⇒ 400 "already exists"** and no second `VOO`/`PETR4` row; **unsupported `EUR` ⇒ 400**; both securities persist across reload.

### Completion Notes List

All three acceptance criteria verified (live DB + live HTTP + stub-backed http test):
- **AC1 — create & persist:** `security(symbol, name, type, quote_currency, created_at)` via `00009`; `service/security.Create` validates and inserts in one tx (AD-3). Type stored lowercase (`stock|etf|fund|other`, CHECK), quote currency a FK to `currency(code)` validated by `money.IsSupported` (AD-5).
- **AC2 — duplicate symbol prevented (case-insensitive):** symbol normalized `ToUpper(TrimSpace())` before insert; a `UNIQUE` index backs it; the service maps Postgres `23505` → `ErrDuplicateSymbol` (mirrors `category.Delete`'s 23503 handling). Verified `petr4` vs `PETR4` collide.
- **AC3 — available downstream:** `store.GetSecurity`/`GetSecurityBySymbol`/`ListSecurities` added now so Story 4.2 reads securities via `store` (no service→service); `service/security.List`/`Get` expose them; the `/securities` page lists them.

Decisions / variances (intentional):
- **Scope = Create + List only** (no edit/delete) — nothing references a security until 4.2 adds `transaction.security_id`, so there is no in-use state to guard; symbol is immutable (dedup identity). Documented in the story scope.
- **`/securities` linked from `/settings`** (like `/exchange-rates` 3.4 / `/categories` 2.2); the five-item primary nav is unchanged. The `Investments` nav target stays `ComingSoon` until 4.2/4.4 build the portfolio views.
- **`type` lowercase in storage, display-cased in UI** (`etf`→`ETF`) via `securityTypeLabel`; matches `account.type`.
- **No `domain` change** — 4.1 has no derived figure (Holdings/valuation are 4.2/4.4), so no `domain` unit test was added.

### File List

New:
- `db/migrations/00009_securities.sql`, `db/query/security.sql`
- `internal/store/security.sql.go` (sqlc; `models.go`/`querier.go` regenerated)
- `internal/service/security/security.go`, `security_test.go`
- `web/pages_templ.go` regenerated for `SecuritiesPage`

Modified:
- `internal/http/router.go` (`Securities` interface + `Deps` field, `/securities` GET/POST routes, `securitiesPage`/`securitiesCreate`/`renderSecurities`/`securityTypeLabel`, `service/security` import)
- `internal/http/router_test.go` (stub `Securities` + `TestSecuritiesPage`, `service/security` import)
- `cmd/server/main.go` (wire `security.New(pool)`)
- `web/pages.templ` (`SecuritiesPage` + Settings "Manage securities →" link), `web/shell.go` (`SecurityRow`, `SecurityTypeOption`), rebuilt `web/static/css/app.css`
- `README.md` (securities section)

## Change Log

| Date | Change |
| --- | --- |
| 2026-06-29 | Story 4.1 drafted (create-story): `security` entity (`00009`) + `db/query/security.sql` + `service/security` (create/list, duplicate-symbol prevention, type/currency validation) + `/securities` page linked from Settings. Epic 4 design questions resolved with owner beforehand. Status → ready-for-dev. |
| 2026-06-29 | Story 4.1 implemented (dev-story): `00009` `security` table; `service/security` Create/Get/List with case-insensitive duplicate-symbol prevention (UNIQUE + 23505→`ErrDuplicateSymbol`); `/securities` page (create + list) linked from Settings. All 3 ACs verified (live DB + live HTTP + stub http test). build/vet/test/nofloat green. Status → review. |
