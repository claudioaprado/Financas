---
baseline_commit: NO_VCS
---

# Story 2.1: Currencies & display currency

Status: review

<!-- Note: Validation is optional. Run validate-create-story for quality check before dev-story. -->

## Story

As the owner,
I want to use USD and BRL and choose which currency totals are shown in,
so that my aggregated figures appear in the currency I think in.

## Acceptance Criteria

From `epics.md` → Epic 2 → Story 2.1. **Given** I am authenticated, **When** I set my Display Currency, **Then**:

1. The choice persists and is used for all aggregated views (FR-2).
2. USD and BRL are available as currencies with ISO-4217 codes.
3. Native amounts are never overwritten by the display choice (AD-5).

> Note: at this point there are no aggregated financial views yet (valuation is Epic 4). AC #1's "used for all aggregated views" is realized by **persisting the choice and making it the single source the display layer reads**; full consumption lands with valuation. Surface the current Display Currency in the shell so it is demonstrably wired.

## Tasks / Subtasks

- [x] **Task 1 — First domain migration: `currency` + `app_settings` (AC: #1, #2, #3)**
  - [x] Add goose migration `db/migrations/00002_currencies_and_settings.sql`. **Up:** `currency(code TEXT PRIMARY KEY, name TEXT NOT NULL)` seeded with `('USD','US Dollar'),('BRL','Brazilian Real')`; `app_settings(id BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id), display_currency TEXT NOT NULL REFERENCES currency(code))` seeded one row `(TRUE,'USD')`. **Down:** drop both (settings first). The `id BOOLEAN … CHECK(id)` enforces a single settings row (single-owner, no tenant column — AD-7).
  - [x] Sequential, forward-only convention; this is the first real schema after the `00001` no-op baseline.

- [x] **Task 2 — Establish the sqlc query workflow (first generated code) (AC: #1, #2)**
  - [x] Add `db/query/settings.sql` and `db/query/currency.sql` with named queries: `GetDisplayCurrency :one` (`SELECT display_currency FROM app_settings WHERE id = TRUE`), `SetDisplayCurrency :exec` (`UPDATE app_settings SET display_currency = $1 WHERE id = TRUE`), `ListCurrencies :many` (`SELECT code, name FROM currency ORDER BY code`).
  - [x] Run `make sqlc` to generate the typed `store` code into `internal/store`. **Watch-item (Dev Notes):** sqlc reads `db/migrations` as schema; it ignores goose `Down` blocks, but confirm it parses `00001_init.sql`'s `DO $$ … $$` no-op — if sqlc errors on it, replace that migration body with a comment-only goose up/down (goose accepts an empty body) and re-run. Commit the generated `*.go`.
  - [x] Generated code uses `pgx/v5` (per `sqlc.yaml`); `store.New(DBTX)` accepts both `*pgxpool.Pool` (reads) and `pgx.Tx` (writes).

- [x] **Task 3 — Supported-currency set in `money` (AC: #2, #3)**
  - [x] In `internal/money`, add `Supported() []Currency` (returns `USD, BRL`) and `IsSupported(Currency) bool`. Keep the existing `Currency`/`Valid()` from Story 1.2. This is the canonical "which currencies exist" source for validation and UI; the DB `currency` table mirrors it for referential integrity.
  - [x] Unit-test `Supported`/`IsSupported`.

- [x] **Task 4 — `service/settings` use-case (AC: #1, #3)**
  - [x] Add `internal/service/settings/settings.go`: `New(pool *pgxpool.Pool) *Service`, `DisplayCurrency(ctx) (money.Currency, error)` (reads via `store.New(pool)`), and `SetDisplayCurrency(ctx, money.Currency) error` (validates `IsSupported`, else a sentinel `ErrUnsupportedCurrency`; performs the write inside **one DB transaction** per AD-3 via `store.New(tx)` + commit/rollback). Optionally `ListCurrencies(ctx)` for the form.
  - [x] AD-5 guardrail: this use-case touches ONLY `app_settings.display_currency`; it never rewrites any stored amount (there are none yet — keep it that way). It performs no conversion.
  - [x] DB-gated integration test (skips without `DATABASE_URL`/`TEST_DATABASE_URL`, per Story 1.2 pattern): default is `USD`; `SetDisplayCurrency(BRL)` persists; setting an unsupported currency returns `ErrUnsupportedCurrency` and does not change the stored value.

- [x] **Task 5 — Settings page + shell wiring (AC: #1, #2)**
  - [x] Add a `Settings` interface to `http.Deps` (consumer-side: `DisplayCurrency(ctx) (money.Currency, error)`, `SetDisplayCurrency(ctx, money.Currency) error`, `ListCurrencies(...)`), implemented by `service/settings`; wire the concrete service in `cmd/server/main.go` (`settings.New(pool)`).
  - [x] Add an authenticated `GET /settings` (a templ page in the shell with a Display Currency `<select>` of the supported currencies, current one selected) and `POST /settings` (read the choice, call `SetDisplayCurrency`, redirect back with success; on an invalid value re-render with a message). Add the route to the protected chi group.
  - [x] Surface the current Display Currency in the shell header and add a "Settings" link there (keep the five-item primary nav from UX-DR1 unchanged — Settings lives by the greeting/logout, not in the main nav). Thread the value via `ShellData` (e.g. `DisplayCurrency string`); handlers populate it (the existing pages should keep rendering — update `renderPage`/`ShellData` so they still compile and show it).
  - [x] Run `make generate css` after editing templ/Tailwind; commit outputs.

- [x] **Task 6 — Tests, verify, docs**
  - [x] `go build ./...`, `go vet ./...`, `go test ./...`, `make nofloat` clean (DB-gated tests skip without a DB).
  - [x] http test: unauth `GET /settings` → 302 `/login`; authed GET shows USD & BRL options; authed `POST /settings` with `BRL` redirects and the shell then reflects BRL. (Use the existing `loginPost`/`sessionCookie` helpers + a stub Settings.)
  - [x] Live smoke (compose db + run, logged in): open `/settings`, switch to BRL, confirm it persists across a reload and the header shows BRL; switch back to USD.
  - [x] Update `README.md` briefly (currencies & display-currency setting; first sqlc-generated queries).

## Dev Notes

### Scope boundary — what this story does NOT do (read first)

- **NO exchange rates** (entering/maintaining USD↔BRL rates) — that is Story 2.2.
- **NO accounts** — Story 2.3.
- **NO conversion math / aggregated totals** — there are no amounts yet; valuation/conversion is Epic 4. This story only persists the Display-Currency *choice* and exposes it; do NOT build a converter or touch `money.Convert` consumers.
- **NO multi-currency beyond USD/BRL** — the supported set is exactly these two (PRD).
- Keep `money` minimal: add only `Supported()`/`IsSupported`; don't expand the type.

### Architecture invariants this story must honor

- **AD-5 — store native currency; convert only at read.** The Display Currency is a **presentation setting**; it never overwrites or pre-converts stored data. Changing it writes only `app_settings`. [Source: ARCHITECTURE-SPINE.md#AD-5]
- **AD-3 — single mutation path, one DB transaction per use-case.** `SetDisplayCurrency` runs in one tx via `service/settings`; handlers never write directly. [Source: ARCHITECTURE-SPINE.md#AD-3]
- **AD-1 — layering.** `http → service → store`; `service` imports `store`+`money`; `http` defines the `Settings` interface it needs and `main` injects the impl. [Source: ARCHITECTURE-SPINE.md#AD-1]
- **AD-7 — single owner, no tenant column.** `app_settings` is a single enforced row; no owner FK. [Source: ARCHITECTURE-SPINE.md#AD-7]
- **Conventions:** ISO-4217 3-letter currency codes; goose sequential/forward-only; sqlc-generated queries into `internal/store` using `pgx/v5`. [Source: ARCHITECTURE-SPINE.md#Consistency Conventions, #Stack]

### Previous-story intelligence (Epic 1) — load-bearing

[Source: 1-2-config-database-foundation.md, 1-3, 1-4 story files; [[financas-epic1-progress]]]

- **Build with `GOTOOLCHAIN=local`** (go.mod pins 1.26.3). Commit generated `*_templ.go` + `app.css` (Dockerfile only compiles). `make generate css` after templ/CSS edits.
- **DB wiring exists:** `internal/store.NewPool` (pgx pool) + `Migrate` (embedded goose, runs on startup); migrations in `db/migrations` (`00001_init.sql` is a no-op baseline). The app already migrates on boot — `00002` will apply automatically.
- **sqlc is wired but never run yet:** `sqlc.yaml` (v2, engine postgresql, queries `db/query`, schema `db/migrations`, out `internal/store`, `sql_package: pgx/v5`). `make sqlc` = `go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.27.0 generate` (first run builds sqlc — slow but fine). This story is where sqlc first emits code.
- **Local DB:** `docker compose up -d db` → host **5433**; `DATABASE_URL=postgres://financas:financas@localhost:5433/financas?sslmode=disable`. The Postgres healthcheck can report "healthy" before the role exists; if a role error appears, `docker compose down -v` and recreate. DB-gated tests skip when no `DATABASE_URL`/`TEST_DATABASE_URL`.
- **Router shape (`internal/http`):** `NewRouter(Deps)`; `Deps{Sessions, Auth, Ready, OwnerName}`; protected chi group with `requireAuth`; pages render via `web.*Page(ShellData).Render`. **Preserve** `/healthz` (dependency-free), `/readyz`, `/login`, `/logout`, CSRF (`http.CrossOriginProtection`), `/static/*`, and the existing shell pages. Add `/settings` to the protected group and a `Settings` field to `Deps`.
- **Shell:** `web/shell.templ` (`Shell(ShellData)`) + `web/shell.go` (`ShellData{OwnerName, Active}`, `NavItems`) + `web/pages.templ` (`DashboardPage`, `ComingSoon`). Extend `ShellData` (e.g. add `DisplayCurrency`) and the header; update every caller of `renderPage`/the page components so they still compile.
- **money:** `internal/money` has `Currency` (`USD`,`BRL` consts) + `Valid()`. Add `Supported()`/`IsSupported`. `make nofloat` must stay green.
- **Forms + CSRF:** same-origin form POSTs pass `http.CrossOriginProtection` with no token (as login does). Method POST for the settings update.
- Repo has no commits (`baseline_commit: NO_VCS`).

### sqlc + pgx specifics (latest-tech, prevent broken codegen)

- **sqlc reads migrations as schema and ignores goose `Down`** blocks (it detects the `-- +goose` format). Confirm `make sqlc` parses cleanly; if the `00001` `DO $$ … $$` no-op trips the parser, change `00001_init.sql` to a comment-only goose up/down (no executable statement) and re-run. [Knowledge cutoff: Jan 2026]
- **`store.New(db DBTX)`** — sqlc/pgx generates a `DBTX` interface satisfied by `*pgxpool.Pool` and `pgx.Tx`. Use the pool for reads and a `pgx.Tx` (`pool.Begin`) for the write (AD-3). Generated row structs use Go `string` for `TEXT`.
- Keep generated files committed; never hand-edit them.

### Project Structure Notes

New: `db/migrations/00002_currencies_and_settings.sql`, `db/query/settings.sql`, `db/query/currency.sql`, sqlc output in `internal/store` (e.g. `db.go`/`models.go`/`*.sql.go` — committed), `internal/service/settings/settings.go` (+ test), `web` settings page (templ + generated). Updated: `internal/money` (+test), `internal/http/router.go` (+`router_test.go`), `cmd/server/main.go`, `web/shell.go`+`shell.templ` (+ `ShellData.DisplayCurrency`), `README.md`. No structural variance.

### Testing standards

- `money`: pure unit tests for `Supported`/`IsSupported`.
- `service/settings`: DB-gated integration (default USD, set BRL persists, unsupported rejected) — `t.Skip` without a DB.
- `http`: httptest with a stub `Settings` — `/settings` auth-gated, GET shows options, POST switches and the shell reflects it.
- `go test ./...` green with no DB; `go vet` + `make nofloat` clean.

### References

- [Source: epics.md#Story 2.1] — acceptance criteria; [Source: epics.md#Epic 2] — currency scaffolding
- [Source: ARCHITECTURE-SPINE.md#AD-5] — store native, convert at read (display currency is presentation-only)
- [Source: ARCHITECTURE-SPINE.md#AD-3] — one tx per use-case
- [Source: ARCHITECTURE-SPINE.md#AD-1 / #AD-7] — layering; single-owner no-tenant
- [Source: ARCHITECTURE-SPINE.md#Consistency Conventions / #Stack] — ISO-4217, goose, sqlc/pgx
- [Source: 1-2-config-database-foundation.md] — pool/migrate/sqlc wiring, DB test gating, money type
- [Source: 1-4-authenticated-app-shell-navigation.md] — shell/ShellData/router patterns to extend

## Dev Agent Record

### Agent Model Used

claude-opus-4-8[1m] (Opus 4.8, 1M context)

### Debug Log References

- `make sqlc` (first run) — **`go run sqlc@v1.27.0` fails to build on this macOS SDK** (`strchrnul` redeclaration in the bundled pg_query C parser). Switched the Makefile `sqlc` target to the **pinned `sqlc/sqlc:1.27.0` Docker image**, which generated cleanly. sqlc parsed the goose migrations fine (the `00001` `DO $$…$$` no-op was a non-issue).
- `go build`/`go vet`/`make nofloat` clean. Unit suite green (DB-gated tests skip without a DB).
- Live DB: `service/settings.TestDisplayCurrencyLifecycle` PASS and `store.TestMigrateThenPool` PASS (00002 applies). 
- Live HTTP smoke (server :8093 + db :5433, logged in): `/settings` shows USD+BRL; header USD→**BRL** after POST; selection persists (`value="BRL" selected`); resetting then POSTing `EUR` is rejected and the header stays USD.

### Completion Notes List

All three acceptance criteria verified (unit + live):
- **AC1 — choice persists & feeds the display layer:** `app_settings.display_currency` (seeded USD) is read by `service/settings.DisplayCurrency` and surfaced in the shell header on every page; switching it persists across reloads. (Aggregated-total *consumption* arrives with valuation, Epic 4 — noted in the story.)
- **AC2 — USD & BRL available (ISO-4217):** seeded `currency` table + `money.Supported()`; the `/settings` `<select>` offers both.
- **AC3 — native amounts never overwritten:** the use-case writes only `app_settings` (one tx, AD-3); it performs no conversion and there are no amounts to touch; unsupported currencies return `ErrUnsupportedCurrency` and leave the stored value unchanged.

Decisions / variances (intentional):
- **sqlc runs via the pinned `sqlc/sqlc:1.27.0` Docker image** (Makefile `sqlc` target changed from `go run`) — its source build fails on macOS SDKs. Documented in the Makefile and `[[financas-epic1-progress]]` should be updated. Generated files committed.
- **`internal/store/db.go` name collision:** sqlc emits its DBTX/`Queries` into `db.go`, which clobbered the Story 1.2 `NewPool`/`Migrate`. Moved those into **`internal/store/pool.go`** (unchanged behavior) and kept sqlc's generated `db.go`. Future `make sqlc` regenerates only the sqlc files.
- **First domain schema + first sqlc generation** for the project — establishes the migration→query→generate→service pattern Epic 2+ reuses. `store.New(pool)` for reads, `store.New(tx)` for the write (AD-3).
- **Single-row `app_settings`** via `id BOOLEAN PK CHECK(id)` — single-owner, no tenant column (AD-7).
- **Settings lives by the greeting/logout**, not in the five-item primary nav (UX-DR1 preserved). `renderPage` now fetches the Display Currency (nil-Settings-safe, so existing tests pass).

Reviewer notes: no `sprint-status.yaml` → status tracked in this file only. Changes staged but **not committed** (left for the owner).

### File List

New:
- `db/migrations/00002_currencies_and_settings.sql`, `db/query/settings.sql`, `db/query/currency.sql`
- `internal/store/db.go`, `models.go`, `querier.go`, `settings.sql.go`, `currency.sql.go` (sqlc-generated), `internal/store/pool.go` (moved `NewPool`/`Migrate`)
- `internal/service/settings/settings.go`, `settings_test.go`
- `web/pages.templ` `SettingsPage` (+ regenerated `pages_templ.go`/`shell_templ.go`)

Modified:
- `internal/money/money.go` (`Supported`/`IsSupported`) + `money_test.go`
- `internal/http/router.go` (`Settings` iface, `Deps.Settings`, `/settings` routes, `shellData`/`renderPage`) + `router_test.go`
- `cmd/server/main.go` (wire `settings.New(pool)`)
- `web/shell.go` (`ShellData.DisplayCurrency`), `web/shell.templ` (header currency + Settings link) + rebuilt `web/static/css/app.css`
- `Makefile` (sqlc via Docker image; `SQLC_VERSION` → image tag), `README.md`

## Change Log

| Date | Change |
| --- | --- |
| 2026-06-28 | Story 2.1 implemented: first domain schema (`currency` + single-row `app_settings`), first sqlc-generated queries (via Docker image), `money.Supported`, `service/settings` use-case (AD-3 tx, AD-5 presentation-only), and a `/settings` page with the Display Currency surfaced in the shell. All 3 ACs verified (unit + live DB + live HTTP). Status → review. |
