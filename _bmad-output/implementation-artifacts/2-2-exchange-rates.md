---
baseline_commit: 77e2a59a1f13de8e31092f832fb5baf4a8690f73
---

# Story 2.2: Exchange rates

Status: review

<!-- Note: Validation is optional. Run validate-create-story for quality check before dev-story. -->

## Story

As the owner,
I want to enter effective-dated USD↔BRL exchange rates,
so that cross-currency totals convert correctly over time.

## Acceptance Criteria

From `epics.md` → Epic 2 → Story 2.2. **Given** a Display Currency is set, **When** I enter an Exchange Rate with an effective date, **Then**:

1. It is stored as a directional, append-only row at `NUMERIC(18,8)` scale (AD-6).
2. Conversions select the rate effective at (≤) the relevant date, latest for "now".
3. When a needed currency pair has no effective rate, the system prompts me rather than inverting or guessing.

> Note on AC #2/#3: there is no cross-currency conversion *consumer* yet (valuation/dashboard is Epic 4/5). This story builds the **canonical effective-dated lookup** (`RateAt`) with the **no-inversion / `ErrNoRate`** behavior and proves it by unit test; the user-facing "prompt" is the `ErrNoRate` path the conversion layer will surface later. The owner can enter and review rates now.

## Tasks / Subtasks

- [x] **Task 1 — `exchange_rate` schema (append-only, directional, effective-dated) (AC: #1)**
  - [x] Add goose migration `db/migrations/00003_exchange_rates.sql`. Up: `exchange_rate(id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY, from_currency TEXT NOT NULL REFERENCES currency(code), to_currency TEXT NOT NULL REFERENCES currency(code), effective_date DATE NOT NULL, rate NUMERIC(18,8) NOT NULL CHECK (rate > 0), created_at TIMESTAMPTZ NOT NULL DEFAULT now(), CHECK (from_currency <> to_currency))`. Add index `(from_currency, to_currency, effective_date DESC)` for the lookup. Down: drop the table.
  - [x] **Directional & append-only:** a row means "1 unit of `from_currency` = `rate` units of `to_currency` as of `effective_date`". Rows are only inserted (never updated/deleted in normal flow); corrections are new rows. `NUMERIC(18,8)` per conventions (distinct from money's `(19,4)`). bigint identity PK.

- [x] **Task 2 — decimal ↔ NUMERIC plumbing (project-wide first; AC: #1)**
  - [x] Add `github.com/jackc/pgx-shopspring-decimal`. In `internal/store.NewPool`, build the pool with `pgxpool.ParseConfig` + an `AfterConnect` that registers the decimal codec: `pgxdecimal.Register(conn.TypeMap())`, then `pgxpool.NewWithConfig`. This lets pgx scan/encode `NUMERIC` ↔ `shopspring/decimal.Decimal` (binary protocol). Keep the existing ping/fail-fast.
  - [x] In `sqlc.yaml`, add `gen.go.overrides` mapping `pg_catalog.numeric → github.com/shopspring/decimal.Decimal` and `date → time.Time` (so generated structs use `decimal.Decimal` and `time.Time`, not `pgtype.*`). Re-running sqlc must keep the Story 2.1 TEXT columns as `string`.
  - [x] **This establishes decimal NUMERIC handling for the whole codebase** — prices (Story 4.3) and all money amounts (Epic 3/4) reuse it. Note it in the Dev Agent Record.

- [x] **Task 3 — sqlc queries + generate (AC: #1, #2)**
  - [x] Add `db/query/exchange_rate.sql`: `AddExchangeRate :one` (INSERT … RETURNING all columns), `RateEffectiveAt :one` (`SELECT rate FROM exchange_rate WHERE from_currency=$1 AND to_currency=$2 AND effective_date <= $3 ORDER BY effective_date DESC, id DESC LIMIT 1`), `ListExchangeRates :many` (all rows, newest-first by effective_date then id).
  - [x] `make sqlc` (Docker image). Confirm `rate` is `decimal.Decimal` and `effective_date` is `time.Time` in the generated code. Commit generated files. (Watch: don't let sqlc clobber `internal/store/pool.go` — it only owns `db.go`/`models.go`/`querier.go`/`*.sql.go`.)

- [x] **Task 4 — `service/exchangerate` use-case (AC: #1, #2, #3)**
  - [x] Add `internal/service/exchangerate/exchangerate.go`: `New(pool)`; a `Rate` struct (`ID int64`, `From/To money.Currency`, `EffectiveDate time.Time`, `Rate decimal.Decimal`, `CreatedAt time.Time`); `Add(ctx, from, to money.Currency, effective time.Time, rate decimal.Decimal) (Rate, error)`; `RateAt(ctx, from, to money.Currency, date time.Time) (decimal.Decimal, error)`; `List(ctx) ([]Rate, error)`.
  - [x] `Add`: validate both currencies `money.IsSupported`, `from != to`, `rate.IsPositive()`; insert in **one transaction** (AD-3). Append-only — no update path.
  - [x] `RateAt`: returns the rate for the **exact `from→to` direction** effective at ≤ `date` (latest); if no row, returns a sentinel **`ErrNoRate`** — it MUST NOT invert a `to→from` rate or guess `1/rate` (AD-6). "Latest for now" = `RateAt(from, to, today)`.
  - [x] DB-gated integration test (skips without DB): add USD→BRL @ 2024-01-01 = 5.0 and @ 2024-06-01 = 5.2; `RateAt(USD,BRL, 2024-03-01)` = 5.0, `RateAt(USD,BRL, 2024-07-01)` = 5.2, `RateAt(USD,BRL, 2023-12-01)` = ErrNoRate (before first); `RateAt(BRL,USD, today)` = **ErrNoRate** (no inversion); reject `from==to`, unsupported currency, and non-positive rate.

- [x] **Task 5 — Exchange-rates management page (AC: #1, #3)**
  - [x] Add an authenticated `GET /exchange-rates` (a templ page in the shell) listing existing rates (from→to, effective date, rate, newest-first) and an add form: from/to currency `<select>` (supported currencies), an effective-date input (`type=date`), and a rate input. `POST /exchange-rates` parses the form, calls `exchangerate.Add`, and redirects back; on a validation error re-render with the message (parse the rate string via `decimal.NewFromString`, never a float).
  - [x] Add an `ExchangeRates` interface to `http.Deps` (`Add`, `List`; `RateAt` not needed by http yet); wire `exchangerate.New(pool)` in `main.go`. Add a "Manage exchange rates" link from the Settings page (keep the five-item primary nav unchanged).
  - [x] `make generate css` after templ/Tailwind edits; commit outputs.

- [x] **Task 6 — Tests, verify, docs**
  - [x] `go build/vet/test ./...` + `make nofloat` clean (DB-gated tests skip without a DB). **`nofloat` must stay green** — rates use `decimal.Decimal` end to end (the form parses strings to decimal; no float anywhere).
  - [x] http test (stub `ExchangeRates`): unauth `GET /exchange-rates` → 302 `/login`; authed GET shows the form + any rows; authed `POST` with a valid rate redirects and the new row appears; an invalid rate (e.g. `from==to` or bad number) does not crash and reports back.
  - [x] Live smoke (compose db + run, logged in): add a USD→BRL rate with an effective date, see it listed; confirm it persists across reload; verify a second rate with a later date is also stored (append-only history).
  - [x] Update `README.md` briefly (exchange rates: directional, effective-dated, append-only, no inversion; the decimal↔NUMERIC plumbing now in place).

## Dev Notes

### Scope boundary — what this story does NOT do (read first)

- **NO conversion of real totals / no dashboard** — there are no amounts or aggregated views yet (Epic 4/5). `RateAt` is built and unit-tested but has no UI consumer; the user-facing "no rate → prompt" surfaces when conversion is wired.
- **NO rate inversion, ever** — do not compute `1/rate`. Missing direction ⇒ `ErrNoRate`. The owner enters each direction they need.
- **NO external/online FX feed** — owner-entered only (AD-6).
- **NO editing/deleting rates** — append-only; corrections are new effective-dated rows.
- **NO accounts** — Story 2.3.

### Architecture invariants this story must honor

- **AD-6 — owner-entered, effective-dated, directional, never inverted.** `exchange_rate` is append-only; reads select the row effective at (≤) the query date; a missing `from→to` pair triggers `ErrNoRate` (the FR-2 prompt), never a `1/rate` guess or a `to→from` inversion. No market-data/FX client. [Source: ARCHITECTURE-SPINE.md#AD-6]
- **AD-3 — one DB transaction per use-case.** `Add` writes in a single tx via `service/exchangerate`. [Source: ARCHITECTURE-SPINE.md#AD-3]
- **AD-4 — decimal, never float.** `rate` is `NUMERIC(18,8)` in PG and `decimal.Decimal` in Go end to end; parse owner input from string. [Source: ARCHITECTURE-SPINE.md#AD-4]
- **AD-1 — layering.** `http → service → store`; `http` defines the `ExchangeRates` interface; `main` injects. [Source: ARCHITECTURE-SPINE.md#AD-1]
- **Conventions:** exchange-rate scale `NUMERIC(18,8)` (distinct from money `(19,4)`); `DATE` for effective dates, `timestamptz` UTC for `created_at`; bigint identity PK. [Source: ARCHITECTURE-SPINE.md#Consistency Conventions]

### Previous-story intelligence (2.1 + Epic 1) — load-bearing

[Source: 2-1-currencies-display-currency.md; [[financas-epic1-progress]]]

- **The migration→query→sqlc→service→http pattern is established (2.1).** Follow it. `currency` table + `money.Supported()` already exist; FK `from_currency/to_currency → currency(code)`.
- **sqlc runs via the pinned `sqlc/sqlc:1.27.0` Docker image** (`make sqlc`), NOT `go run` (source build fails on this macOS SDK). Generated files committed.
- **`internal/store/db.go`/`models.go`/`querier.go`/`*.sql.go` are sqlc-generated; `internal/store/pool.go` is the hand-written pool+migrate.** Don't put hand code in sqlc files; don't let sqlc overwrite `pool.go`. `store.New(pool)` for reads, `store.New(tx)` (from `pool.Begin`) for writes.
- **Router:** `NewRouter(Deps)`; protected chi group with `requireAuth`; `renderPage`/`shellData` already fetch the Display Currency for the header (nil-Settings-safe). `Deps` currently has `Sessions, Auth, Ready, Settings, OwnerName`. Add `ExchangeRates`. Preserve `/healthz`, `/readyz`, `/login`, `/logout`, CSRF, `/static/*`, `/settings`, and the shell pages.
- **Settings page** (`web/pages.templ` `SettingsPage`) is the currency-config home — add the "Manage exchange rates" link there. Header has Settings + Display-Currency badge + Log out.
- **money:** `Currency`/`USD`/`BRL`/`Supported()`/`IsSupported()`; `decimal` is `github.com/shopspring/decimal`. `make nofloat` guards `internal/{money,domain,service,store}`.
- Build with `GOTOOLCHAIN=local`. Repo now HAS commits (baseline_commit will be a real SHA). Local db: `docker compose up -d db` → host 5433; DB-gated tests skip without `DATABASE_URL`/`TEST_DATABASE_URL`.

### decimal ↔ NUMERIC + pgx/sqlc specifics (latest-tech; get this right once)

- **pgx needs the decimal codec registered** to scan/encode `NUMERIC` ↔ `decimal.Decimal`. Use `github.com/jackc/pgx-shopspring-decimal`: build the pool via `pgxpool.ParseConfig(url)`, set `cfg.AfterConnect = func(ctx, c *pgx.Conn) error { pgxdecimal.Register(c.TypeMap()); return nil }`, then `pgxpool.NewWithConfig(ctx, cfg)`. Without this, scanning a NUMERIC into `decimal.Decimal` is unreliable. [Knowledge cutoff: Jan 2026]
- **sqlc type overrides** (in `sqlc.yaml` under `gen.go`):
  ```yaml
        overrides:
          - db_type: "pg_catalog.numeric"
            go_type: "github.com/shopspring/decimal.Decimal"
          - db_type: "date"
            go_type: "time.Time"
  ```
  These make generated structs/params use `decimal.Decimal` and `time.Time`. NOT-NULL columns → non-pointer types. `timestamptz` already maps to `time.Time` (no override needed). [Knowledge cutoff: Jan 2026]
- **Parse owner rate input** with `decimal.NewFromString` (reject errors); never `decimal.NewFromFloat` for authored values. `decimal.Decimal.IsPositive()` for the `> 0` check (the DB CHECK is the backstop).
- **`effective_date`** is a calendar date — send a `time.Time` at UTC midnight (the form gives `YYYY-MM-DD`; `time.Parse("2006-01-02", v)`).

### Project Structure Notes

New: `db/migrations/00003_exchange_rates.sql`, `db/query/exchange_rate.sql`, sqlc output (regenerated `models.go`/`querier.go`/`db.go` + `exchange_rate.sql.go`), `internal/service/exchangerate/exchangerate.go` (+ test), a templ exchange-rates page in `web/pages.templ` (+ regenerated `*_templ.go`). Updated: `sqlc.yaml` (overrides), `internal/store/pool.go` (decimal codec registration), `internal/http/router.go` (+`ExchangeRates` iface, routes) + `router_test.go`, `cmd/server/main.go` (wire), `web/pages.templ` Settings link, `go.mod`/`go.sum`, `README.md`. No structural variance.

### Testing standards

- `service/exchangerate`: DB-gated integration covering the effective-at selection, **no-inversion `ErrNoRate`**, and validation (from==to, unsupported, non-positive). This is the AD-6 heart — test it thoroughly.
- `http`: httptest with a stub `ExchangeRates` — auth-gating, list+add render, POST happy/sad paths.
- `go test ./...` green with no DB; `go vet` + `make nofloat` clean (no float introduced).

### References

- [Source: epics.md#Story 2.2] — acceptance criteria
- [Source: ARCHITECTURE-SPINE.md#AD-6] — owner-entered, effective-dated, directional, no inversion, no feed
- [Source: ARCHITECTURE-SPINE.md#AD-3 / #AD-4 / #AD-1] — one tx; decimal not float; layering
- [Source: ARCHITECTURE-SPINE.md#Consistency Conventions] — NUMERIC(18,8) rate scale, DATE/timestamptz, bigint identity
- [Source: 2-1-currencies-display-currency.md] — migration→sqlc→service→http pattern, sqlc-via-Docker, store file layout, router/Deps shape, Settings page

## Dev Agent Record

### Agent Model Used

claude-opus-4-8[1m] (Opus 4.8, 1M context)

### Debug Log References

- `make sqlc` (Docker image) — `rate` generated as `decimal.Decimal`, `effective_date` as `time.Time` (overrides applied). `timestamptz`/`created_at` override did NOT apply (sqlc kept `pgtype.Timestamptz`); handled by reading `.Time` in the service rather than fighting the override key.
- `go build`/`go vet`/`make nofloat` clean. Unit suite green (DB-gated tests skip without a DB).
- Live DB: `exchangerate.TestExchangeRate` PASS — effective-at (5.0/5.2/ErrNoRate), **no-inversion** (BRL→USD ⇒ ErrNoRate), and validation (same-currency/unsupported/non-positive). Confirms the decimal codec round-trips `NUMERIC(18,8)` ↔ `decimal.Decimal`.
- Live HTTP smoke (server :8094 + db :5433): added USD→BRL @5.30 and @5.45 (both 303), both listed (append-only history accumulates), values render via decimal, same-currency POST → 400.

### Completion Notes List

All three acceptance criteria verified (unit + live):
- **AC1 — directional, append-only, `NUMERIC(18,8)`:** `exchange_rate` schema (FK to `currency`, `CHECK rate>0`, `CHECK from<>to`, bigint identity); `Add` only inserts (corrections are new rows). Decimal values persisted/read exactly.
- **AC2 — effective-at selection:** `RateEffectiveAt` (`effective_date <= $date ORDER BY effective_date DESC, id DESC LIMIT 1`); "latest for now" = date today. Proven by the integration test.
- **AC3 — no inversion / prompt:** `RateAt` returns `ErrNoRate` for a missing `from→to` direction (incl. when only the reverse exists) — never `1/rate`. The UI prompt surfaces when conversion is wired (Epic 4/5).

Decisions / variances (intentional):
- **Established the project-wide decimal↔NUMERIC plumbing** (first NUMERIC money column): `github.com/jackc/pgx-shopspring-decimal` registered per-connection in `store.NewPool` (now `ParseConfig` + `AfterConnect` + `NewWithConfig`), and `sqlc.yaml` overrides `numeric → decimal.Decimal`, `date → time.Time`. **All future amount/price columns reuse this** (Epic 3/4, Story 4.3).
- **`created_at` stays `pgtype.Timestamptz`** in generated code (the timestamptz override key didn't match); the service maps `.Time` to `time.Time`. Minor; not worth a custom plugin.
- **`http` imports `service/exchangerate`** for the `Rate` type in the `ExchangeRates` interface — allowed (http → service, AD-1); behavior is still stubbed in tests. Currency options for the form come from `money.Supported()` (no DB hit).
- **Exchange rates live at `/exchange-rates`**, linked from `/settings`; the five-item primary nav (UX-DR1) is unchanged.
- Validation authority is the service (`ErrSameCurrency`/`ErrUnsupportedCurrency`/`ErrNonPositiveRate`), backed by DB CHECKs; the handler parses the rate with `decimal.NewFromString` (never a float) and re-renders with a message on error. `nofloat` stays green.

Reviewer notes: no `sprint-status.yaml` → status tracked in this file only. `baseline_commit` is the real SHA `77e2a59`. Changes staged but **not committed** (the owner commits/pushes).

### File List

New:
- `db/migrations/00003_exchange_rates.sql`, `db/query/exchange_rate.sql`
- `internal/store/exchange_rate.sql.go` (sqlc-generated; `models.go`/`querier.go`/`db.go` regenerated)
- `internal/service/exchangerate/exchangerate.go`, `exchangerate_test.go`
- `web/pages.templ` `ExchangeRatesPage` (+ regenerated `pages_templ.go`)

Modified:
- `internal/store/pool.go` (register decimal codec via `ParseConfig`/`AfterConnect`/`NewWithConfig`)
- `sqlc.yaml` (numeric→decimal, date→time.Time overrides)
- `internal/http/router.go` (`ExchangeRates` iface, `Deps.ExchangeRates`, `/exchange-rates` routes + handlers) + `router_test.go`
- `cmd/server/main.go` (wire `exchangerate.New(pool)`)
- `web/shell.go` (`RateRow`), `web/pages.templ` Settings link + rebuilt `web/static/css/app.css`
- `go.mod`/`go.sum` (pgx-shopspring-decimal), `README.md`

## Change Log

| Date | Change |
| --- | --- |
| 2026-06-28 | Story 2.2 implemented: `exchange_rate` schema (directional, append-only, effective-dated, NUMERIC(18,8)); project-wide decimal↔NUMERIC plumbing (pgx-shopspring-decimal + sqlc overrides); `service/exchangerate` with the AD-6 no-inversion `RateAt`/`ErrNoRate`; `/exchange-rates` management page. All 3 ACs verified (unit + live DB + live HTTP). Status → review. |
