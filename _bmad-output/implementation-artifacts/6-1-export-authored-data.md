---
baseline_commit: 04a566b
---

# Story 6.1: Export authored data

Status: done

<!-- Note: Validation is optional. Run validate-create-story for quality check before dev-story. -->

## Story

As the owner,
I want to export all my data to a single file,
so that I have a backup I control.

## Acceptance Criteria

From `epics.md` → Epic 6 → Story 6.1 (realizes **FR-15**; honors **NFR-2**, **AD-2**, **AD-4/NFR-5**, **AD-1**, **AD-3**). **Given** I am authenticated with data, **When** I export, **Then**:

1. **The file contains only AUTHORED state** — Accounts, Securities, Transactions, Categories, Prices, Exchange Rates, and the display-currency setting (FR-15, AD-2). Every authored row is included in full fidelity: its **primary key**, all authored columns, and `created_at`, so a later restore (6.2) can re-insert it identically.
2. **Derived data is NOT included** — Holdings, balances, Valuation, Gain/Loss, Net Worth are derived on read (AD-2) and never stored, so they never appear in the export. The seeded **currency** reference table is also excluded (it is migration-seeded, identical on every instance — not authored).
3. **The export downloads as a single, self-describing, re-importable file** — one JSON document with a stable schema tag and an integer schema **version** (the contract 6.2 validates), streamed as a file download (`Content-Disposition: attachment`) with a dated filename. Decimal amounts are serialized as **strings**, never floats (AD-4/NFR-5).
4. **The export is a point-in-time snapshot read consistently** — all tables are read inside one read transaction so a concurrent write can't tear the snapshot (NFR-2). An empty instance exports a well-formed file with empty row arrays (not an error).

### Locked design decisions (read before implementing)

- **D1 — New `backup` service owns export assembly; http only serializes + streams (AD-1).** Add `internal/service/backup/backup.go` with `type Service struct { pool *pgxpool.Pool }` and `func New(pool *pgxpool.Pool) *Service`, mirroring `service/settings`. It exposes `func (s *Service) Export(ctx context.Context) (Export, error)` returning a fully-assembled, JSON-ready `Export` value (typed DTO, below). The http layer marshals it and sets headers — it performs no assembly, no SQL, no mapping math (AD-1). The service reads **all tables inside one `pool.Begin(ctx)` read transaction** (commit at the end; rollback on any error) so the snapshot is internally consistent (NFR-2, AD-3 "one DB transaction per use-case").

- **D2 — Dedicated full-row `Export*` store queries (NOT the UI read seams).** The existing store/service read seams are UI-shaped (account-scoped lists, joined names, dropped `created_at`, filtered `archived`) and can't faithfully reproduce a row. Add `db/query/backup.sql` with one full-row, **`ORDER BY id`** SELECT per authored table so sqlc returns the exact model structs:
  ```sql
  -- name: ExportAccounts :many
  SELECT id, name, type, currency, archived, created_at FROM account ORDER BY id;
  -- name: ExportCategories :many
  SELECT id, name, kind, created_at FROM category ORDER BY id;
  -- name: ExportSecurities :many
  SELECT id, symbol, name, type, quote_currency, created_at FROM security ORDER BY id;
  -- name: ExportExchangeRates :many
  SELECT id, from_currency, to_currency, effective_date, rate, created_at FROM exchange_rate ORDER BY id;
  -- name: ExportPrices :many
  SELECT id, security_id, effective_date, price, created_at FROM price ORDER BY id;
  -- name: ExportTransactions :many
  SELECT id, type, from_account_id, to_account_id, from_amount, to_amount, occurred_on,
         description, created_at, category_id, import_hash, security_id, quantity, price, fees
  FROM transaction ORDER BY id;
  ```
  Because each SELECT lists exactly the table's columns, sqlc emits `[]store.Account`, `[]store.Category`, `[]store.Security`, `[]store.ExchangeRate`, `[]store.Price`, `[]store.Transaction` (the existing models in `internal/store/models.go`) — full fidelity, including `pgtype.Timestamptz` `created_at` and the `decimal.Decimal` money columns. Display currency reuses the existing `GetDisplayCurrency`. Run **`make sqlc`** (the pinned Docker image — `make generate` also runs templ) and commit the generated `internal/store/backup.sql.go`. **Ordering by `id` is required** so 6.2 inserts parents before children deterministically and the export is byte-stable across runs.

- **D3 — Explicit, versioned export DTO (decimals & dates as strings) — the 6.2 contract.** Do **not** marshal `store.*` structs directly (their `pgtype.Timestamptz`/`pgtype.Int8`/`decimal.Decimal` JSON shapes are an implementation detail and would couple the file to sqlc). Define explicit DTO types in `backup` whose fields are stable and self-evidently float-free:
  ```go
  const (
      ExportSchema  = "financas.export" // stable schema tag
      ExportVersion = 1                 // bump when the shape changes; 6.2 validates ==
  )

  type Export struct {
      Schema          string             `json:"schema"`            // == ExportSchema
      Version         int                `json:"version"`           // == ExportVersion
      ExportedAt      string             `json:"exported_at"`       // RFC3339 UTC, informational (ignored on restore)
      DisplayCurrency string             `json:"display_currency"`  // app_settings singleton
      Accounts        []AccountDTO       `json:"accounts"`
      Categories      []CategoryDTO      `json:"categories"`
      Securities      []SecurityDTO      `json:"securities"`
      ExchangeRates   []ExchangeRateDTO  `json:"exchange_rates"`
      Prices          []PriceDTO         `json:"prices"`
      Transactions    []TransactionDTO   `json:"transactions"`
  }
  ```
  DTO field rules: every PK and FK id is `int64`; **nullable** FKs/text (`from_account_id`, `to_account_id`, `category_id`, `security_id`, `import_hash`) are `*int64` / `*string` with `json:"...,omitempty"` (JSON `null`/absent ⇄ SQL NULL); **decimals** (`from_amount`, `to_amount`, `rate`, `price`, `quantity`, `fees`) are `string` via `decimal.Decimal.String()` (NFR-5 — never a float); **dates** (`occurred_on`, `effective_date`) are `string` in `"2006-01-02"`; **`created_at`** is RFC3339 UTC `string` from the `pgtype.Timestamptz`. The DTOs and these constants live in `backup` so 6.2 imports them as the canonical schema. Slices are **non-nil empty** (`[]AccountDTO{}`) so an empty instance emits `[]`, not `null` (matches sqlc `emit_empty_slices`).

- **D4 — `created_at` is exported and preserved.** `created_at` is authored history (when the row was first written), not a derived financial figure — exporting it lets 6.2 reproduce the source instance exactly (NFR-2 "reproduces the same … instance"). Map `pgtype.Timestamptz` → `t.Time.UTC().Format(time.RFC3339Nano)` when `Valid`, else omit. (6.2 will insert it explicitly; a fresh row's `DEFAULT now()` is the fallback only when absent.)

- **D5 — GET `/export` streams the download; a "Download my data" link on Settings (AD-1, UX).** Add an authenticated `GET /export` route (inside the `requireAuth` group in `NewRouter`). Handler: call `deps.Backup.Export(req.Context())`; on error render a graceful 500 (reuse the existing error-banner style — never a blank/partial file); on success set `Content-Type: application/json; charset=utf-8` and `Content-Disposition: attachment; filename="financas-export-YYYY-MM-DD.json"` (date from `time.Now().UTC()`), then `json.NewEncoder(w).SetIndent("", "  ")` + `Encode(exp)` (indented for human-inspectability; stdlib `encoding/json`, **no new dependency**). Define a consumer-side `Backup` interface in `internal/http/router.go` (`Export(ctx context.Context) (backup.Export, error)`) and add it to `Deps`; wire `backup.New(pool)` in `cmd/server/main.go`. Surface it on the **Settings** page: a new "Backup & restore" `<section>` with a normal link/anchor button to `/export` (a GET download — no form/CSRF needed) and copy ("Download a single file with all your accounts, transactions, securities, prices and rates. Keep it somewhere safe."). Leave a visual placeholder note that Restore arrives in 6.2 (do not build upload here).

- **D6 — Scope guard (what 6.1 does NOT touch).** No change to any existing service/handler/query behavior; no migration; no new Go dependency (stdlib `encoding/json`). `make nofloat` stays green — the `backup` service maps decimals to strings via `.String()` and contains no `float32/float64`. The financial core is untouched (export is a pure read).

## Tasks / Subtasks

- [x] **Task 1 — Store: full-row export queries (AC: #1, #2; D2)**
  - [x] Add `db/query/backup.sql` with the six `Export*` `:many` SELECTs from D2 (each `ORDER BY id`, columns matching the table so sqlc returns the existing model structs).
  - [x] Run `make sqlc`; commit generated `internal/store/backup.sql.go` and any `querier.go` delta. Confirm the generated methods return `[]store.Account` / `[]store.Category` / `[]store.Security` / `[]store.ExchangeRate` / `[]store.Price` / `[]store.Transaction` (no new row structs). `gofmt` clean.

- [x] **Task 2 — Service `backup`: assemble the versioned Export DTO (AC: #1–#4; D1, D3, D4)**
  - [x] Create `internal/service/backup/backup.go`: package doc, `Service{pool}`, `New`, the `ExportSchema`/`ExportVersion` consts, the `Export` + per-table DTO types (D3), and `Export(ctx) (Export, error)`.
  - [x] Implement `Export`: `tx, _ := s.pool.Begin(ctx)` with `defer tx.Rollback`; `q := store.New(tx)`; read `GetDisplayCurrency` + the six `Export*` queries; map each `store.*` row to its DTO (decimals→`.String()`, dates→`"2006-01-02"`, `created_at`→RFC3339Nano UTC, nullable `pgtype.Int8/Text`→`*int64`/`*string`); set `Schema/Version/ExportedAt` (`time.Now().UTC().Format(time.RFC3339)`); `tx.Commit`. Initialize every slice non-nil. Wrap errors `fmt.Errorf("backup: ...: %w", err)`.
  - [x] **Tests first (TDD)** `internal/service/backup/backup_test.go`, DB-gated (`TEST_DATABASE_URL`, reuse the `isolatedDB` helper pattern used by `service/transaction`/`valuation` tests so the shared base DB is untouched): (a) seed a small but representative instance (≥2 accounts incl. one archived + cross-currency, ≥1 category, ≥1 security, ≥1 exchange rate, ≥1 price, and transactions covering income/expense/transfer/buy/sell/dividend incl. NULL and non-NULL category/security/from/to), then assert `Export` returns every row with matching PKs, decimal **strings** (exact, e.g. `"1234.5600"`/raw `.String()` — assert the value you stored), correct null-vs-set pointers, and `created_at` present; (b) assert `Schema==ExportSchema && Version==ExportVersion`; (c) a fresh/empty `isolatedDB` exports `Schema/Version/DisplayCurrency` set and all six arrays **non-nil empty**; (d) a `nofloat`-style sanity: decimal fields are Go `string`. Confirm **no float** anywhere in the package.

- [x] **Task 3 — HTTP: `GET /export` download + Settings link (AC: #3; D5)**
  - [x] In `internal/http/router.go`: add the `Backup` consumer interface + `Deps.Backup` field; register `pr.Get("/export", exportData(deps))` in the authenticated group; implement `exportData` (assemble via `deps.Backup.Export`, graceful 500 on error, else set `Content-Type` + `Content-Disposition` dated filename and stream indented JSON via `json.NewEncoder`).
  - [x] Wire `Backup: backup.New(pool)` in `cmd/server/main.go`.
  - [x] Add the "Backup & restore" section + `/export` download link to `web.SettingsPage` (`web/pages.templ`); run `make generate` (templ) and commit `web/*_templ.go`. No new CSS utility expected (reuse existing tokens); only run `make css` if you introduce a genuinely new dynamically-composed class (Tailwind v4 safelist note).
  - [x] **Handler tests** (`internal/http/router_test.go`): add a `stubBackup` implementing the interface; assert `GET /export` (authenticated) returns 200, `Content-Type: application/json`, a `Content-Disposition: attachment; filename="financas-export-...json"` header, and a body that JSON-decodes to an `Export` with the canned schema/version/rows; assert an unauthenticated `GET /export` redirects to `/login` (auth group); assert a service error yields a graceful 500 (no partial JSON). Keep all existing router tests green (the `Deps` now has `Backup`; provide the stub in the shared test deps builder).

- [x] **Task 4 — Verify (AC: all)**
  - [x] `GOTOOLCHAIN=local make generate` (templ+sqlc) leaves a clean tree (generated files committed); `gofmt` clean; `GOTOOLCHAIN=local make nofloat` green; `GOTOOLCHAIN=local make vet` clean; `GOTOOLCHAIN=local make test` green (DB-gated tests run with `TEST_DATABASE_URL=postgres://financas:financas@localhost:5433/financas?sslmode=disable`).
  - [x] Live smoke (free :8080 first — `lsof -ti tcp:8080 | xargs kill`): `make build && make run`, log in `owner`/`financas`, hit `/export`, confirm a JSON file downloads with the expected schema/version and only authored tables (spot-check no holdings/balance/networth keys). Export is read-only — it does **not** mutate the base DB, so no cleanup needed (unlike 6.2). Kill the server when done.

## Dev Notes

- **Why dedicated queries over reusing read seams:** fidelity. Restore (6.2) does an identity insert preserving PKs and `created_at`; the UI read seams drop those. The `Export*` SELECTs are the smallest faithful surface and keep the export schema decoupled from UI shapes.
- **Decimal serialization:** `decimal.Decimal.String()` yields the exact stored value as text; the DTO holds it as a Go `string`. Never `float64`. The DB session is UTC; `created_at` is normalized to UTC RFC3339Nano.
- **Snapshot consistency (NFR-2):** one read transaction wraps all six reads + the setting, so a concurrent insert can't appear in some arrays but not others.
- **The file is the 6.2 contract:** `ExportSchema`/`ExportVersion` + the DTO types are exported from `backup` precisely so 6.2 imports and validates them. Any shape change after this story must bump `ExportVersion`.
- **Build/tooling reminders:** `GOTOOLCHAIN=local` for all go/make; `make sqlc` uses the pinned Docker image; commit generated `*.sql.go` and `*_templ.go`; `make nofloat` scope is `internal/{money,domain,service,store}` — `backup` is in scope, so keep it string-based.

## Change Log

| Date       | Version | Description                          | Author |
| ---------- | ------- | ------------------------------------ | ------ |
| 2026-06-29 | 0.1     | Initial draft (create-story, 6.1)    | Claude |

## Dev Agent Record

### Context Reference

### Agent Model Used

### Debug Log References

### Agent Model Used

claude-opus-4-8 (executor for Task 1; main loop completed Tasks 2–4 after the executor's connection dropped mid-run)

### Completion Notes List

- Implemented exactly to locked decisions D1–D6. All four tasks complete, TDD throughout.
- Task 1 (store): `db/query/backup.sql` + generated `internal/store/backup.sql.go` — six full-row `Export*` queries returning the existing model structs, `ORDER BY id`.
- Task 2 (service): `internal/service/backup/backup.go` — `Export` DTO + `ExportSchema`/`ExportVersion` (=1) + per-table DTOs (decimals/dates/created_at as strings; nullable FKs/import_hash as `*int64`/`*string`). One read transaction wraps all reads (NFR-2). The `decimal` import was dropped (the type is never named; `.String()` is called on store-struct fields). DB-gated tests assert full fidelity + empty-instance non-nil arrays.
- Task 3 (http): `Backup` consumer interface + `Deps.Backup`; `GET /export` (`exportData`) streams indented JSON via `encoding/json` with `Content-Disposition: attachment; filename="financas-export-YYYY-MM-DD.json"`; graceful 500 with no partial body. Wired `backup.New(pool)` in `cmd/server/main.go`. Added a "Backup & restore" section + `/export` link to `web.SettingsPage`. Handler tests: auth-redirect, authored-JSON download (asserts no derived keys), service-error 500, settings link present.
- Verification (Task 4): `make generate` idempotent (no drift); `gofmt -l` empty; `go vet ./...` clean; `make nofloat` OK; full `make test` green (incl. new backup + http tests, DB-gated against :5433). Live smoke: logged in owner/financas, `GET /export` → 200, correct headers + dated filename; body had exactly the 7 authored sections (accounts/categories/securities/exchange_rates/prices/transactions + display_currency), decimals as strings, nulls omitted, `created_at` preserved, and ZERO derived keys (the only "Valuation" substring was a seeded security name "Valuation Co"). Export is read-only — base DB `display_currency` stayed USD (settings-test invariant intact).
- No new Go dependency (stdlib `encoding/json`); no migration; no change to existing behavior.
- **Independent Opus review (separate lane): CHANGES-REQUIRED → resolved.** One High blocker (H1): `Export` used `pool.Begin` (READ COMMITTED), so the six reads did NOT share one snapshot — a concurrent write between reads could tear the export (a transaction referencing an account absent from the file), defeating the AC#4/NFR-2 guarantee. Fixed to `pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})`. Also applied L1 nit (renamed receiver-shadowing `s` loop var → `sec`). Re-verified: gofmt/vet/nofloat clean, full suite green. (L2 swallowed-encode-error left as-is — non-blocking; an in-memory struct of strings/ints can't fail marshaling and the status is already committed.) Reviewer confirmed full column fidelity, authored-only/no-derived, float-free, auth-gating, graceful 500, no tx leak, generated tree in sync. Three informational heads-ups carried into 6.2: decimal scale not normalized (compare numerically, not by string); identity PKs need `OVERRIDING SYSTEM VALUE` on restore + sequence reset; `ORDER BY id` is intra-table only (cross-table parents-before-children is 6.2's insert-order responsibility).

### File List

- `db/query/backup.sql` (new)
- `internal/store/backup.sql.go` (new, generated)
- `internal/store/querier.go` (modified, generated — interface methods added)
- `internal/service/backup/backup.go` (new)
- `internal/service/backup/backup_test.go` (new)
- `internal/http/router.go` (modified — `Backup` iface, `Deps.Backup`, `/export` route, `exportData` handler, `encoding/json` import)
- `internal/http/router_test.go` (modified — `stubBackup`, `cannedExport`, `/export` tests, `authedGet` helper)
- `cmd/server/main.go` (modified — wire `backup.New(pool)`)
- `web/pages.templ` (modified — Backup & restore section on Settings)
- `web/pages_templ.go` (modified, generated)
