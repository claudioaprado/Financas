---
baseline_commit: NO_VCS
---

# Story 1.2: Config & database foundation with decimal money

Status: review

<!-- Note: Validation is optional. Run validate-create-story for quality check before dev-story. -->

## Story

As the builder,
I want environment-driven config, a pgx connection pool with migrations, and the shared `Money` type,
so that data persists and all monetary math is exact from the first feature.

## Acceptance Criteria

From `epics.md` → Epic 1 → Story 1.2. **Given** the scaffold from Story 1.1, **When** the app boots with env config (DB URL, session secret), **Then**:

1. It connects via a pgx pool and runs pending goose migrations on startup.
2. The `money` package exposes a `Money` type (decimal amount + ISO-4217 currency) and a `Convert(amount, rate)` function using banker's rounding (AD-4, AD-12).
3. No floating-point type is used for any monetary or quantity value anywhere (NFR-5).

## Tasks / Subtasks

- [x] **Task 1 — Environment-driven config (AC: #1)**
  - [x] Add `internal/config` with a `Config` struct and `Load() (Config, error)` that reads from the environment: `DATABASE_URL` (required), `SESSION_SECRET` (required — consumed by Story 1.3 but loaded/validated now), `PORT` (default `8080`). Config is infrastructure read at startup; it does not violate AD-1 (it carries no financial logic).
  - [x] `Load()` returns a clear typed error naming any missing required variable; do NOT log secret values. Fail fast at boot.
  - [x] Update `cmd/server/main.go` to call `config.Load()` first and wire the rest from it. Keep `main` thin (config → migrate → pool → wire → listen).
  - [x] Unit-test `Load()`: required-present succeeds; missing `DATABASE_URL`/`SESSION_SECRET` errors; `PORT` default applies. Use `t.Setenv`.

- [x] **Task 2 — Initial goose migration + embedded FS (AC: #1)**
  - [x] Add the first migration `db/migrations/00001_init.sql` with `-- +goose Up` / `-- +goose Down` sections. It must be **foundational only** — create NO domain tables (currencies, accounts, etc. belong to Epic 2; the owner/credential table belongs to Story 1.3). A safe, reversible no-op that establishes the goose baseline is correct here (see Dev Notes for the exact body). Its real job is to prove the runner end-to-end and force the embed to resolve.
  - [x] Add `db/embed.go` (package `db`) exposing `//go:embed migrations/*.sql` as `var Migrations embed.FS`. The `*.sql` glob requires ≥1 file, which `00001_init.sql` satisfies.
  - [x] Keep `db/migrations/.gitkeep` or remove it once a real `.sql` exists — either is fine.

- [x] **Task 3 — pgx pool + run migrations on startup (AC: #1)**
  - [x] Add pgx v5 (`github.com/jackc/pgx/v5`), its pool (`.../pgxpool`) and stdlib adapter (`.../stdlib`), and goose v3 (`github.com/pressly/goose/v3`).
  - [x] In `internal/store`, add `NewPool(ctx, databaseURL) (*pgxpool.Pool, error)` (creates + pings the pool) and `Migrate(ctx, databaseURL string, fsys fs.FS) error` (opens a `*sql.DB` via the pgx stdlib driver, `goose.SetBaseFS(fsys)`, `goose.SetDialect("postgres")`, runs up, closes the `*sql.DB`). Store owns persistence wiring (AD-1); it does not import `service`.
  - [x] In `main`: `store.Migrate(...)` (run pending migrations) → `store.NewPool(...)` → defer pool close → build router → listen. Migrations run **before** serving. Fail fast (log + non-zero exit) if connect or migrate fails.
  - [x] Pass the pool into the router constructor so later stories have it (`NewRouter(pool)` or a small deps struct). `/healthz` stays dependency-free (Story 1.1 invariant); OPTIONAL: add `GET /readyz` that pings the pool and returns 200/503.
  - [x] DB-gated integration test in `internal/store`: when `DATABASE_URL` (or a test-specific `TEST_DATABASE_URL`) is set, `Migrate` then `NewPool`+ping succeed and the `goose_db_version` table exists; `t.Skip` cleanly when unset so the default `go test ./...` stays green without a database.

- [x] **Task 4 — The shared `money` package: `Money`, `Currency`, `Convert` (AC: #2, #3)**
  - [x] Add `github.com/shopspring/decimal`. In `internal/money/money.go`: define `type Currency string` (ISO-4217 3-letter; provide `USD` and `BRL` constants and a `Valid()`/normalization helper) and `type Money struct { amount decimal.Decimal; currency Currency }` with unexported fields and accessors (`Amount() decimal.Decimal`, `Currency() Currency`). Provide constructors `New(decimal.Decimal, Currency)` and `MustParse/Parse(string, Currency)`.
  - [x] Define the shared scale/precision constants in ONE place: `MoneyScale = 4` (NUMERIC(19,4)) and set `decimal.DivisionPrecision` explicitly (e.g. 12) in a single `init()` (per conventions — intermediates carry full precision, rounded once at the boundary).
  - [x] In `internal/money/convert.go`: `Convert(amount Money, rate decimal.Decimal, target Currency) Money` performs `amount.amount.Mul(rate)` at **full precision** and returns Money in `target`. Provide a single boundary rounding `Round() Money` (or `Rounded()`) using `decimal.Decimal.RoundBank(MoneyScale)` (banker's / half-to-even). See Dev Notes "Convert vs rounding" — this reconciles the AC wording with AD-12 (convert-then-sum, round once at the display boundary).
  - [x] Add `Money.Add`/`Sub` (same-currency only; return a typed error or panic-on-mismatch consistently) only if trivial; do not over-build — later stories extend `money`.
  - [x] Tests: banker's rounding is half-to-even (e.g. 2.5→2, 3.5→4 at the relevant scale; 0.00005 cases at money scale); `Convert` preserves precision pre-rounding; **convert-then-sum == sum-then-round** for a small set (the AD-12 invariant); currency validation; JSON/string rendering never yields a float.

- [x] **Task 5 — NFR-5 guardrail: no floating-point money anywhere (AC: #3)**
  - [x] Add a check that fails if `float32`/`float64` appears in the financial core. Implement as a `make nofloat` target (and/or a Go test) that greps `internal/money`, `internal/domain`, `internal/service`, `internal/store` excluding generated `*_templ.go`/sqlc output and `*_test.go` fixtures, and exits non-zero on a hit. Document it in the README.
  - [x] Confirm the money type and all new code use `decimal.Decimal` exclusively for monetary/quantity values, including any JSON/templ rendering paths.

- [x] **Task 6 — Verify end-to-end & update docs**
  - [x] `go build ./...`, `go vet ./...`, `go test ./...` all clean (DB integration test skips without a DB).
  - [x] Live DB check: `docker compose up -d db`, export `DATABASE_URL=postgres://financas:financas@localhost:5432/financas?sslmode=disable`, run `go run ./cmd/server` (or `make run`), confirm logs show a successful connect + "migrations applied" and `/healthz` (and `/readyz` if added) answer; then verify `goose_db_version` exists. Tear down with `docker compose down -v`.
  - [x] Update `README.md`: env vars (`DATABASE_URL`, `SESSION_SECRET`, `PORT`), how migrations run on startup, the `nofloat` check, and the money-scale/precision conventions.

## Dev Notes

### Scope boundary — what this story does NOT do (read first)

- **NO domain tables** (currencies, accounts, securities, transactions…) — those are Epic 2+. The only migration here is the foundational `00001_init.sql` (no-op baseline).
- **NO owner/credential table and NO auth** — Story 1.3. Load/validate `SESSION_SECRET` here, but do not use it yet.
- **NO sqlc queries** — there is no schema to query. sqlc stays wired (from 1.1) but unused.
- **Keep `money` minimal** — `Money`, `Currency`, `Convert`, boundary rounding, and only trivial same-currency `Add`/`Sub` if needed. Holdings/valuation/net-worth derivations are Epic 4 and live in `domain`, not here.

If a behavior is required for the app to boot and persist correctly end-to-end (config validation, fail-fast on bad DB URL, migrations-before-serving), it is in scope even if not spelled out.

### Previous-story intelligence (Story 1.1 — just completed)

[Source: _bmad-output/implementation-artifacts/1-1-project-scaffold-layered-structure.md]

The scaffold is in place and verified (chi `/healthz` 200, single-image Docker, `postgres:18` compose). Carry these forward:

- **Go directive is `1.26.3`** in `go.mod` (installed toolchain), not the spine's 1.26.4 — keep building with `GOTOOLCHAIN=local` to avoid an auto-download. Don't "fix" it to 1.26.4 unless that toolchain is installed.
- **`postgres:18` volume is mounted at `/var/lib/postgresql`** (NOT `/var/lib/postgresql/data`) — already corrected in `docker-compose.yml`. The compose `db` credentials are `financas/financas/financas` on `5432`.
- **Codegen outputs are committed** (`web/layout_templ.go`, `web/static/css/app.css`); the Dockerfile only compiles. If you add anything that needs codegen, commit the output.
- **Module path:** `github.com/claudioaprado/financas`. Packages already exist as stubs: `internal/{domain,money,service,store,http}` each with a `doc.go` — fill `money` and `store` here; leave `domain`/`service` for later stories.
- **Router is `internal/http.NewRouter()`** with no args today; changing its signature to accept the pool is expected — update `cmd/server/main.go` accordingly.
- **`baseline_commit`** will likely be `NO_VCS` again (repo has no commits yet); that's expected.
- Build/test commands live in the `Makefile`; add new targets there (`nofloat`), don't invent a parallel system.

### Architecture invariants this story must honor

- **AD-1 — Layered dependency direction.** `config` and `store` are infrastructure: `store` owns the pool + migration runner and imports neither `service` nor `http`. `main` (cmd) wires everything. `money` imports nothing project-internal. [Source: ARCHITECTURE-SPINE.md#AD-1]
- **AD-3 — Single mutation path, one DB transaction per use-case.** Not exercised yet (no use-cases), but the pool you create here is what `service` use-cases will wrap in a transaction later. Expose the pool; don't add ad-hoc query helpers that bypass `service`. [Source: ARCHITECTURE-SPINE.md#AD-3]
- **AD-4 — Money and quantities are decimal, never float.** `Money` = decimal amount + ISO-4217 currency; `NUMERIC` in PostgreSQL later. Floating-point money forbidden end-to-end including JSON and templ rendering. [Source: ARCHITECTURE-SPINE.md#AD-4]
- **AD-12 — Conversion & money-rounding policy.** `money.Convert(amount, rate)` is the single pure conversion; banker's rounding (half-to-even), rounded **once at the display boundary**; aggregates **convert-then-sum**. `decimal.DivisionPrecision` set explicitly in one shared place; intermediates carry full precision. [Source: ARCHITECTURE-SPINE.md#AD-12]
- **Conventions:** money/price `NUMERIC(19,4)`; quantity `NUMERIC(28,10)`; exchange rate `NUMERIC(18,8)`; goose migrations sequential, forward-only in production; `shopspring/decimal` in Go. [Source: ARCHITECTURE-SPINE.md#Consistency Conventions, #Stack]

### `00001_init.sql` — exact recommended body (foundational no-op)

```sql
-- +goose Up
-- Initial migration: establishes the goose version baseline. Application/domain
-- schema arrives in Epic 2 (currencies, accounts, …); this intentionally
-- creates no application tables. Its purpose is to prove the on-startup runner
-- and make the embedded migrations glob non-empty.
-- +goose StatementBegin
DO $$ BEGIN END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN END $$;
-- +goose StatementEnd
```

This runs cleanly up and down, creates `goose_db_version`, and preempts no later story. (If you prefer a visibly real artifact, a reversible `COMMENT ON DATABASE` is also acceptable — but do NOT create domain tables.)

### Convert vs rounding (resolve this deliberately — AC #2 vs AD-12)

The AC says "`Convert(amount, rate)` … using banker's rounding"; AD-12 says conversions are rounded **once at the display boundary** and aggregates **convert-then-sum**. If `Convert` itself rounded to money scale, `convert-then-sum` would round every leg before summing — diverging from "round once". Reconciliation (recommended):

- `Convert` multiplies at **full precision** and returns Money in the target currency (no rounding).
- A single boundary method `Round()`/`Rounded()` applies `RoundBank(MoneyScale)` (banker's, half-to-even). Banker's is the package-wide rounding mode — the AC's intent — applied exactly once at the boundary.
- Aggregation: convert each native leg (full precision) → sum → `Round()` once.

Add a test proving `sum(Convert(aᵢ, r)).Round() == Round(sum of full-precision conversions)` and that per-leg rounding would differ, so the invariant is locked by a test, not just a comment.

### Latest-tech gotchas

- **goose v3 with pgx:** goose needs a `database/sql` `*sql.DB`. Open it with the pgx stdlib adapter: `sql.Open("pgx", databaseURL)` (import `_ "github.com/jackc/pgx/v5/stdlib"`). Use `goose.SetBaseFS(embedFS)` + `goose.SetDialect("postgres")` + `goose.Up(db, "migrations")` (or the `UpContext`/provider API). Run migrations on a short-lived `*sql.DB`, separate from the long-lived `pgxpool.Pool` the app serves from. [Knowledge cutoff: Jan 2026]
- **pgxpool:** `pgxpool.New(ctx, url)` then `pool.Ping(ctx)` to fail fast. Defer `pool.Close()` in `main`. [Knowledge cutoff: Jan 2026]
- **shopspring/decimal:** use `RoundBank(places)` for half-to-even; set `decimal.DivisionPrecision` once (default 16; the spine suggests an explicit value like 12). Never construct decimals from `float64` for authored money — parse from string. [Knowledge cutoff: Jan 2026]

### Project Structure Notes

New/changed files (all under the established tree — no structural variance):
- `internal/config/config.go` (+ `config_test.go`) — NEW package
- `internal/store/db.go` (`NewPool`, `Migrate`) (+ DB-gated `db_test.go`) — fills the existing `store` stub
- `internal/money/money.go`, `internal/money/convert.go` (+ tests) — fills the existing `money` stub
- `db/migrations/00001_init.sql`, `db/embed.go` — NEW
- `cmd/server/main.go` — UPDATE (wire config → migrate → pool → router)
- `internal/http/router.go` — UPDATE (accept the pool; keep `/healthz` dependency-free; optional `/readyz`)
- `Makefile` (`nofloat` target), `README.md` — UPDATE

### Testing standards

[Source: ARCHITECTURE-SPINE.md (testing/errors conventions); Story 1.1 established `testing` + `httptest`]

- Standard `testing` package; table tests welcome. No third-party framework.
- `money` is pure → exhaustive unit tests (rounding, convert-then-sum invariant, currency validation). This is the highest-value test surface in the story.
- `config` → `t.Setenv` unit tests.
- `store` DB integration → gated on `DATABASE_URL`/`TEST_DATABASE_URL`, `t.Skip` when absent. `go test ./...` must pass with and without a database.
- Keep `go vet ./...` clean; add `make nofloat` to the verification set.

### References

- [Source: _bmad-output/planning-artifacts/architecture/architecture-Financas-2026-06-28/ARCHITECTURE-SPINE.md#AD-1] — layering (store/config infrastructure)
- [Source: _bmad-output/planning-artifacts/architecture/architecture-Financas-2026-06-28/ARCHITECTURE-SPINE.md#AD-4] — decimal money, no float
- [Source: _bmad-output/planning-artifacts/architecture/architecture-Financas-2026-06-28/ARCHITECTURE-SPINE.md#AD-12] — conversion & banker's-rounding policy
- [Source: _bmad-output/planning-artifacts/architecture/architecture-Financas-2026-06-28/ARCHITECTURE-SPINE.md#Consistency Conventions] — NUMERIC scales, goose forward-only, decimal
- [Source: _bmad-output/planning-artifacts/epics.md#Story 1.2] — acceptance criteria
- [Source: _bmad-output/implementation-artifacts/1-1-project-scaffold-layered-structure.md] — scaffold state, variances, compose db config

## Dev Agent Record

### Agent Model Used

claude-opus-4-8[1m] (Opus 4.8, 1M context)

### Debug Log References

- `go build ./...`, `go vet ./...` → clean. `make nofloat` → OK (no float in financial core).
- Unit suite (no DB): `config`, `http` (healthz + readyz 200/503/200), `money` all pass; `store` DB integration test `t.Skip`s without a URL.
- DB-backed suite (`DATABASE_URL`/`TEST_DATABASE_URL` → compose db on `localhost:5433`): `store.TestMigrateThenPool` passes — migrate is idempotent on second run; `goose_db_version` populated.
- Live server smoke (`PORT=8090 go run ./cmd/server`): log `migrations applied` → `database connected` → `listening`; `/healthz` 200 `ok`, `/readyz` 200 `ready`; `goose_db_version` count = 2 (baseline + 00001). `RESULT: LIVE_OK`.

### Completion Notes List

All three acceptance criteria verified:
- **AC1 (pgx pool + migrations on startup):** `internal/config.Load()` validates env (fail-fast); `internal/store.Migrate` runs embedded goose migrations over a short-lived `database/sql` handle (pgx stdlib driver) before serving; `internal/store.NewPool` opens + pings the pgx pool. Verified live (startup log + `goose_db_version`).
- **AC2 (`Money` + `Convert`, banker's rounding):** `internal/money` exposes `Money` (decimal amount + ISO-4217 `Currency`), `Convert(amount, rate, target)`, and boundary `Rounded()` (RoundBank, half-to-even). Locked by tests including the convert-then-sum-vs-per-leg-rounding invariant.
- **AC3 (no float):** `make nofloat` greps `internal/{money,domain,service,store}` and passes; all monetary code uses `shopspring/decimal`.

Decisions / variances (intentional):
- **`Convert` does NOT round; `Money.Rounded()` is the single display-boundary rounding** (banker's). This reconciles AC #2's "Convert … banker's rounding" wording with AD-12 (convert-then-sum, round once). `TestConvertThenSumInvariant` proves per-leg rounding would diverge, so the ordering is enforced by a test, not just a comment.
- **`/readyz` takes an injected `ReadyCheck func(context.Context) error`** (wired to `pool.Ping` in `main`) rather than importing pgxpool into `internal/http` — keeps the HTTP layer free of persistence types (cleaner AD-1 boundary). `/healthz` stays dependency-free (Story 1.1 invariant preserved).
- **`00001_init.sql` is a reversible no-op** (`DO $$ BEGIN END $$;` up/down) — establishes the goose baseline and makes the embed glob non-empty without creating any domain table (those are Epic 2+).
- **goose wired via library** (`pressly/goose/v3` `UpContext` + `SetBaseFS`), not the CLI, so migrations run in-process on startup. The Makefile `migrate` CLI target remains for ad-hoc use.
- **Compose DB host port moved 5432 → 5433** (mapped to container 5432) after live verification found a **native PostgreSQL already bound to host 5432** — the Postgres healthcheck still reported "healthy" (it only checks the server answers, not the role), so host connections silently hit the wrong server. `.env.example` and README updated to 5433. (The story's Task 6 text still references 5432; 5433 is the corrected value.)
- **Decimal precision:** `decimal.DivisionPrecision = 12` set once in `money.init()`; `MoneyScale = 4` (NUMERIC(19,4)).

Carry-forward for Story 1.3: `SESSION_SECRET` is loaded/validated but unused; the pool is available via `main` for handlers; auth/owner table is next.

Reviewer notes: no `sprint-status.yaml`, so status tracked in this file only. Changes staged but **not committed** (left for the owner).

### File List

New:
- `internal/config/config.go`, `internal/config/config_test.go`
- `db/embed.go`, `db/migrations/00001_init.sql`
- `internal/store/db.go`, `internal/store/db_test.go`
- `internal/money/money.go`, `internal/money/convert.go`, `internal/money/money_test.go`, `internal/money/convert_test.go`

Modified:
- `cmd/server/main.go` (config → migrate → pool → router wiring)
- `internal/http/router.go`, `internal/http/router_test.go` (injected `ReadyCheck`, `/readyz`)
- `internal/money/doc.go`, `internal/store/doc.go` (doc updates)
- `Makefile` (`nofloat` target), `README.md` (config/db/money docs)
- `docker-compose.yml` (db host port → 5433), `.env.example` (DATABASE_URL → 5433)
- `go.mod`, `go.sum` (pgx v5, goose v3, shopspring/decimal)

Removed:
- `db/migrations/.gitkeep` (superseded by `00001_init.sql`)

## Change Log

| Date | Change |
| --- | --- |
| 2026-06-28 | Story 1.2 implemented: env config (`internal/config`), pgx pool + on-startup goose migrations (`internal/store`, embedded `db/migrations`), and the decimal `money` package (`Money`/`Currency`/`Convert` + banker's-rounding boundary). NFR-5 `nofloat` guard added. All 3 ACs verified (unit + live DB). Fixed a host-port collision (compose db → 5433). Status → review. |
