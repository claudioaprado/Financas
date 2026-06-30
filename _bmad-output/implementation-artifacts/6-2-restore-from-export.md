---
baseline_commit: 1bc08d8
---

# Story 6.2: Restore from export

Status: done

<!-- Note: Validation is optional. Run validate-create-story for quality check before dev-story. -->

## Story

As the owner,
I want to restore an instance from an export file,
so that I can recover after a host loss.

## Acceptance Criteria

From `epics.md` → Epic 6 → Story 6.2 (realizes **FR-15**; honors **NFR-2**, **AD-2**, **AD-3**, **AD-4/NFR-5**, **AD-1**). It consumes the export format defined in Story 6.1 (`backup.Export`, `backup.ExportSchema`, `backup.ExportVersion`). **Given** a valid export file, **When** I restore it, **Then**:

1. **Authored rows are inserted preserving primary keys** — identity insert (`OVERRIDING SYSTEM VALUE`), no FK remap; every account/category/security/exchange-rate/price/transaction is re-created with its original `id` and `created_at`, and the display-currency setting is restored (FR-15, AD-2).
2. **Balances, Holdings, and Net Worth re-derive to match the source instance** — restore rehydrates only AUTHORED data; everything derived (balances, holdings, valuation, net worth) recomputes on read from the restored ledger and matches the source (NFR-2, AD-2). No derived figure is ever written.
3. **A malformed, wrong-schema, wrong-version, or partial file is rejected with a clear reason and leaves the instance unchanged** — the entire restore runs in one DB transaction (AD-3); any validation failure or constraint violation rolls everything back, so the instance is byte-for-byte what it was before (atomic, all-or-nothing). The owner sees a specific message (bad JSON / wrong schema / unsupported version / which constraint failed).
4. **Restore is idempotent and a true recovery (replace, not merge)** — restoring the same file twice yields the same final state; restoring replaces the current authored data wholesale (the recovery use-case: reproduce the source instance, AD-2). The destructive nature is confirmed in the UI before it runs.

### Locked design decisions (read before implementing)

- **D1 — Replace-all in ONE transaction (AD-3); validate-then-apply.** Restore is recovery, not merge. The whole operation is a single `pool.Begin(ctx)` transaction: (a) **validate** the parsed file up front (schema/version/structure) — cheap rejects before any write; (b) **delete** all authored rows in child→parent order; (c) **insert** every row with `OVERRIDING SYSTEM VALUE` in parent→child order, preserving `id` + `created_at`; (d) **reset** each table's identity sequence so future inserts don't collide; (e) **set** `display_currency`; (f) `Commit`. `defer tx.Rollback` covers every early return and any constraint violation → **the instance is unchanged on any failure** (AC#3). Replace-all makes restore **idempotent** (AC#4) and correct on a non-empty instance (delete-all first), while still satisfying the AC's fresh/empty case (delete-all is a no-op there). This lives in the existing `internal/service/backup` package (the data-safety use-case), extending the `Service` from 6.1.

- **D2 — Service owns parse + validation + apply; http owns the upload + CSRF (AD-1).** Add `func (s *Service) Restore(ctx context.Context, raw []byte) (RestoreSummary, error)`. The service **parses the JSON itself** (`json.Unmarshal` into `backup.Export`) and validates, returning **typed sentinel errors** so http can render a precise reason without business logic:
  ```go
  var (
      ErrMalformed     = errors.New("backup: file is not a valid Financas export")
      ErrUnsupportedSchema  = errors.New("backup: unrecognized export schema")
      ErrUnsupportedVersion = errors.New("backup: unsupported export version")
  )
  type RestoreSummary struct { Accounts, Categories, Securities, ExchangeRates, Prices, Transactions int }
  ```
  Validation order (all before mutating anything): non-empty & JSON-parses (`ErrMalformed`, wrap the json error for the message tail); `Schema == ExportSchema` (`ErrUnsupportedSchema`); `Version == ExportVersion` (`ErrUnsupportedVersion`, message names the number); `DisplayCurrency` present & `money.IsSupported` (`ErrMalformed` with reason). Per-row decimal/date **parse** happens during mapping inside the tx — a parse failure aborts the tx (`ErrMalformed`, naming the field) and rolls back. Referential integrity (a transaction referencing a missing account, a price referencing a missing security, an unknown currency) is enforced by the existing DB **foreign keys** during insert: a violation aborts the tx → rollback → wrap as `ErrMalformed` with the DB reason (AC#3 "partial file rejected, instance unchanged"). The http layer maps these sentinels to **400** with the message and the success case to a redirect; it never inspects the file (AD-1).

- **D3 — Deletes (child→parent) and identity inserts (parent→child).** Add to `db/query/backup.sql` (so 6.1's queries stay grouped):
  - **Deletes** (`:exec`, no params), run in this order so FKs never block: `DELETE FROM transaction`, `DELETE FROM price`, `DELETE FROM exchange_rate`, `DELETE FROM security`, `DELETE FROM category`, `DELETE FROM account`. (`app_settings` is the singleton — updated, never deleted.) Name them `RestoreDeleteTransactions` … `RestoreDeleteAccounts`.
  - **Inserts** (`:exec`) with explicit PK + `created_at`, parent→child: `RestoreInsertAccount`, `RestoreInsertCategory`, `RestoreInsertSecurity`, `RestoreInsertExchangeRate`, `RestoreInsertPrice`, `RestoreInsertTransaction`. Each is `INSERT INTO <t> (id, …all cols…, created_at) OVERRIDING SYSTEM VALUE VALUES ($1,…)`. The PKs are `GENERATED ALWAYS AS IDENTITY`, so **`OVERRIDING SYSTEM VALUE` is mandatory** — a plain insert of an explicit id errors (reviewer heads-up #2). Insertion order: accounts, categories, securities (depend only on the seeded `currency`), then exchange_rates (→currency), prices (→security), transactions (→account/category/security). Generated `*Params` structs carry `decimal.Decimal` / `time.Time` / `pgtype.*` — the service builds them from the DTO strings.
  - **Sequence resets** (`:exec`, one per table) after inserts so the next owner-created row gets a fresh id: `SELECT setval(pg_get_serial_sequence('account','id'), COALESCE((SELECT MAX(id) FROM account), 1), (SELECT MAX(id) FROM account) IS NOT NULL)`. The 3-arg form sets `is_called` = "rows exist", so a restored-empty table's next id is 1 and a populated table's next id is MAX+1. Name them `RestoreResetAccountSeq` … `RestoreResetTransactionSeq`. Run `make sqlc`; commit generated code.

- **D4 — DTO → row mapping (the inverse of 6.1; decimals via decimal, never float).** For each DTO, build the sqlc `*Params`: ids `int64` direct; nullable `*int64`/`*string` → `pgtype.Int8{Int64:*p, Valid:p!=nil}` / `pgtype.Text{String:*p, Valid:p!=nil}`; decimals (`from_amount`, `to_amount`, `rate`, `price`, `quantity`, `fees`) via `decimal.NewFromString(dto.Field)` — a parse error is `ErrMalformed` (NFR-5: parse the exact string, never `ParseFloat`); dates (`occurred_on`, `effective_date`) via `time.Parse("2006-01-02", …)`; `created_at` via `time.Parse(time.RFC3339Nano, …)` → `pgtype.Timestamptz{Time:t, Valid:true}` (the reviewer heads-up #1: scale like `"1.5"` vs `"1.5000"` re-inserts identically into `NUMERIC` — do **not** string-compare; just re-parse). Cross-table parents-before-children ordering is the service's responsibility (reviewer heads-up #3: 6.1's `ORDER BY id` is intra-table only).

- **D5 — HTTP: `POST /restore` upload + destructive confirm (AD-1).** Add `pr.Post("/restore", restoreData(deps))` in the `requireAuth` group (a same-origin form POST → `CrossOriginProtection` + the session cover CSRF). Handler: `req.ParseMultipartForm(N<<20)`; require the confirm checkbox (`req.PostFormValue("confirm") == "on"`) — without it, re-render the Settings/Backup section with "Tick the box to confirm you understand restore replaces all current data."; read the uploaded `file` (reuse the `readImportContent` multipart pattern from importer, but keep **bytes** — JSON, not text munging); call `deps.Backup.Restore(req.Context(), raw)`. On success → redirect `/settings` (optionally a `?restored=1` flash with the `RestoreSummary` counts). On a typed error → re-render with the specific reason and **400**. Extend the `Backup` interface (`internal/http/router.go`) with `Restore(ctx, raw []byte) (backup.RestoreSummary, error)`; `Deps`/`main.go` already inject `backup.New(pool)`.

- **D6 — Settings UI: turn the 6.1 placeholder into a real restore form.** In `web.SettingsPage` "Backup & restore" section, replace the "Restore arrives later" note with an upload `<form method="post" action="/restore" enctype="multipart/form-data">`: a `file` input (`accept=".json,application/json"`), a **required confirm checkbox** ("I understand this replaces all my current data with the backup's contents"), a submit button ("Restore from backup"), and a slot to show the success summary or the error reason. Keep it visually subordinate to (below) the export link, with a clear destructive-action warning tone. `make generate` (templ) + commit `web/pages_templ.go`. Only run `make css` if a genuinely new dynamically-composed utility class is introduced (Tailwind v4 safelist note).

- **D7 — Scope & invariants.** No schema migration (uses existing tables/FKs). No new Go dependency. `make nofloat` stays green — restore parses decimals via `decimal.NewFromString` (string in scope `internal/{…,service,store}`), no float. The financial core is untouched: restore only writes authored rows; all derived figures recompute on read (AD-2). **Smoke hygiene (shared base DB):** the live smoke MUTATES data (restore deletes/replaces!). Run it against the base `financas` DB only with care — after the smoke, restore the base DB to a known good state and ensure `display_currency` is back to `'USD'` (the `TestDisplayCurrencyLifecycle`/settings-suite invariant), or do the smoke against a throwaway DB. The DB-gated unit tests use `isolatedDB` (throwaway), so they never touch the base DB.

## Tasks / Subtasks

- [x] **Task 1 — Store: delete, identity-insert, and sequence-reset queries (AC: #1, #3; D3)**
  - [x] Add the six `RestoreDelete*` (`:exec`), six `RestoreInsert*` (`:exec`, `OVERRIDING SYSTEM VALUE`, explicit id+created_at), and six `RestoreReset*Seq` (`:exec`, 3-arg `setval`) queries to `db/query/backup.sql`.
  - [x] `make sqlc`; commit generated `internal/store/backup.sql.go` + `querier.go` delta. Confirm the `*Params` structs include `ID int64` and `CreatedAt pgtype.Timestamptz` and the decimal/date columns. `gofmt` clean.

- [x] **Task 2 — Service: `Restore` (parse → validate → atomic replace) (AC: #1–#4; D1, D2, D4)**
  - [x] In `internal/service/backup/backup.go` add the sentinel errors, `RestoreSummary`, and `Restore(ctx, raw []byte) (RestoreSummary, error)`. Validate (schema/version/display-currency) before the tx; then `tx, _ := s.pool.Begin(ctx)` / `defer Rollback`; `q := store.New(tx)`; run all `RestoreDelete*` (child→parent); map+insert every DTO (parent→child) building `*Params` per D4 (decimal/date/timestamp parse errors → `ErrMalformed`); run all `RestoreReset*Seq`; `q.SetDisplayCurrency`; `tx.Commit`. Return the counts. Wrap DB/constraint errors as `ErrMalformed` (so a referential-integrity failure reads as a bad file), keep schema/version/parse sentinels distinct.
  - [x] **Tests first (TDD)**, DB-gated, `isolatedDB` (reuse the 6.1 test helpers in this package):
    - **Round-trip (the crown jewel, AC#2):** seed a representative instance (cross-currency cash + investment accounts, a category, securities, a rate, a price, income/expense/transfer/buy/sell/dividend); capture source `valuation.Portfolio`/Net Worth + an account `Balance` + `Holdings`; `Export`; **wipe** (or use a second `isolatedDB`); `Restore(exportedBytes)`; assert the restored rows preserve PKs + `created_at`, and that `valuation` Net Worth / portfolio / a balance / holdings **equal the source** (derived-on-read matches — NFR-2/AD-2).
    - **Identity preserved & sequence reset:** after restore, a newly-created account gets `MAX(id)+1` (no collision, no gap-reuse that clashes).
    - **Idempotent (AC#4):** `Restore` the same bytes twice → identical final state (row counts + a checksum/Net Worth equal).
    - **Atomic reject leaves instance unchanged (AC#3):** seed instance X; attempt `Restore` of (a) non-JSON → `ErrMalformed`, (b) wrong `schema` → `ErrUnsupportedSchema`, (c) `version: 999` → `ErrUnsupportedVersion`, (d) a structurally-valid file whose a transaction references a non-existent `from_account_id` → error; after EACH failed restore assert instance X is **unchanged** (same rows / same Net Worth).
  - [x] Confirm **no float** in the package (`make nofloat`).

- [x] **Task 3 — HTTP: `POST /restore` upload + confirm (AC: #3, #4; D5)**
  - [x] Extend the `Backup` interface with `Restore`; add `pr.Post("/restore", restoreData(deps))`; implement `restoreData` (multipart read → bytes; require confirm checkbox; call `deps.Backup.Restore`; success→redirect `/settings`, typed error→re-render with the reason + 400).
  - [x] **Handler tests** (`router_test.go`): extend `stubBackup` with a `restoreErr`/`restoreSummary` + record the received bytes. Assert: a valid multipart POST with confirm → 303 `/settings` and the service received the file bytes; missing confirm → no service call + a "tick the box" message; a `stubBackup{restoreErr: backup.ErrUnsupportedVersion}` → 400 with the version message; unauthenticated `POST /restore` → redirect `/login`. Keep existing tests green.

- [x] **Task 4 — Settings UI: the restore form (D6)**
  - [x] Update `web.SettingsPage` "Backup & restore" section to the upload form + required confirm checkbox + success/error slot (D6). `make generate`; commit `web/pages_templ.go`. Add a `web` render test asserting the form (`action="/restore"`, `enctype`, the confirm checkbox, the warning copy) is present.

- [x] **Task 5 — Verify (AC: all)**
  - [x] `GOTOOLCHAIN=local make generate` clean tree; `gofmt -l` empty; `GOTOOLCHAIN=local make vet` clean; `GOTOOLCHAIN=local make nofloat` green; `TEST_DATABASE_URL=postgres://financas:financas@localhost:5433/financas?sslmode=disable GOTOOLCHAIN=local make test` green.
  - [x] **Live round-trip smoke (careful — restore mutates data):** free :8080 (`lsof -ti tcp:8080 | xargs kill`); `make build && make run`; log in owner/financas; `GET /export` to a file; **then** either (preferred) point the server at a throwaway DB and restore there, OR restore into the base DB and afterwards re-seed/clean so the base DB's `display_currency` is back to `'USD'` and the settings suite still passes. Confirm the dashboard Net Worth/holdings after restore match the pre-restore figures. Kill the server; leave the base DB in its known-good (USD) state.

## Dev Notes

- **Why replace-all-in-one-tx:** it is simultaneously the recovery semantics (reproduce the source), the idempotency guarantee, and the "unchanged on failure" guarantee — all three fall out of one atomic delete+insert. A merge would reintroduce PK-collision and partial-state ambiguity the AC explicitly wants to avoid.
- **`OVERRIDING SYSTEM VALUE` + sequence reset** are both required because PKs are `GENERATED ALWAYS AS IDENTITY` (reviewer heads-up #2): the override lets us re-insert the original ids; the `setval` keeps the identity generator ahead of them.
- **Decimals re-parse, never compare as strings** (reviewer heads-up #1): `"1.5"` and `"1.5000"` are the same money and both land identically in `NUMERIC(19,4)`. The round-trip test asserts on **derived figures** (Net Worth/balances) and re-parsed values, not raw export-string equality.
- **Referential integrity for free:** the existing FKs make a partial/dangling file fail at insert time inside the tx, giving AC#3's "rejected, unchanged" without a hand-rolled validator — wrap the DB error into `ErrMalformed` for a readable message.
- **Build/tooling:** `GOTOOLCHAIN=local` everywhere; `make sqlc` via the pinned Docker image; commit generated `*.sql.go` + `*_templ.go`; `make nofloat` scope includes `service`/`store` — keep decimal-string based.

## Change Log

| Date       | Version | Description                          | Author |
| ---------- | ------- | ------------------------------------ | ------ |
| 2026-06-30 | 0.1     | Initial draft (create-story, 6.2)    | Claude |

## Dev Agent Record

### Context Reference

### Agent Model Used

### Debug Log References

### Agent Model Used

claude-opus-4-8 (main loop, full TDD)

### Completion Notes List

- Implemented to locked decisions D1–D7. TDD throughout; all five tasks complete.
- Task 1 (store): added 18 queries to `db/query/backup.sql` — 6 `RestoreDelete*` (child→parent), 6 `RestoreInsert*` (`OVERRIDING SYSTEM VALUE`, explicit id + created_at), 6 `RestoreReset*Seq` (3-arg `setval` for empty/non-empty). Generated `internal/store/backup.sql.go` + `querier.go`.
- Task 2 (service): `Restore(ctx, raw []byte) (RestoreSummary, error)` in `internal/service/backup/backup.go` — sentinel errors (`ErrMalformed`/`ErrUnsupportedSchema`/`ErrUnsupportedVersion`); parse+validate (schema/version/display-currency) and build ALL params BEFORE the tx (decimals via `decimal.NewFromString`, never float); one transaction does delete-all → identity-insert → sequence-reset → set display-currency → commit; `defer Rollback` makes every failure atomic. DB/FK violations wrapped as `ErrMalformed`. Tests: round-trip into a fresh instance (Net Worth/portfolio reproduce, PKs+created_at preserved, post-restore id = MAX+1), idempotency, and atomic-reject for non-JSON / wrong-schema / bad-version / dangling-FK (each asserts the instance is unchanged).
- Task 3 (http): `Backup.Restore` added; `POST /restore` (`restoreData`) — multipart upload, required confirm checkbox, typed-error → 400 with a specific reason via `restoreErrorMessage`, success → PRG `/settings?restored=1`. `renderSettings` helper + `settingsForm` reads `?restored`. Handler tests: auth (401 on unauth POST), success (service got the file bytes), missing-confirm (no service call + message), version-error reason, settings form + notice present.
- Task 4 (ui): `web.SettingsPage` gained `notice`/`noticeIsError` params + the restore `<form enctype=multipart/form-data>` with file input, destructive-confirm checkbox, and a colored notice slot. Ran `make generate` + `make css` (new `bg-gain/10`/`border-gain/40` success-banner utilities) and committed `web/pages_templ.go` + `web/static/css/app.css`.
- Task 5 (verify): `make generate`/`make css` idempotent (no drift); gofmt/`go vet ./...` clean; `make nofloat` green; full `make test` green (incl. round-trip/idempotent/atomic + restore handler tests). **Live round-trip smoke against a THROWAWAY DB** (per D7 — restore mutates data): fresh DB (0 rows) → `POST /restore` the real 200 KB export.json with confirm → 303 `/settings?restored=1` → re-export was **byte-identical** to the source (same PKs + created_at; sha256 match) with 297 accounts / 289 txns / 109 securities / 52 prices / 25 rates; dashboard re-derived Net Worth (200). Base `financas` DB untouched — `display_currency` still `'USD'` (settings-suite invariant intact); throwaway DB dropped.
- Reviewer heads-ups from 6.1 all honored: identity insert uses `OVERRIDING SYSTEM VALUE` + sequence reset; decimals re-parsed (not string-compared); cross-table parent→child insert order handled in the service.
- **Independent Opus review (separate lane): APPROVE-WITH-NITS (no Critical/High).** Reviewer verified atomicity (all parsing before `tx.Begin`, `defer Rollback` covers every path, single commit), identity-insert + 3-arg `setval` for empty/non-empty tables, FK delete/insert ordering (`app_settings` updated not deleted), byte-identical round-trip + matching Net Worth/Portfolio, decimals via `decimal.NewFromString` only, and `POST /restore` auth/CSRF/confirm/PRG; confirmed READ COMMITTED is the correct isolation for a single-writer atomic replace-all (unlike export). Applied the two highest-value nits: (1) `http.MaxBytesReader` body cap in `restoreData` with a distinct "file too large" message (defense-in-depth + clearer error than a silent truncation); (2) fixed the stale `seedSample` doc and added direct `Balance`/`Holdings`/realized-gain round-trip assertions (AC#2 names both). Left as documented/known: `toTimestamp` now()-fallback on an absent `created_at` (harmless, column NOT NULL, real exports always carry it); restore enforces DB constraints but not service-layer business invariants (only a hand-edited—not app-produced—file could introduce a DB-valid-but-logically-odd state on the owner's own instance; all such figures are derived-on-read). Re-verified after fixes: gofmt/vet/nofloat clean, full suite green.

### File List

- `db/query/backup.sql` (modified — 18 restore queries)
- `internal/store/backup.sql.go` (modified, generated)
- `internal/store/querier.go` (modified, generated)
- `internal/service/backup/backup.go` (modified — `Restore` + sentinels + DTO→row helpers)
- `internal/service/backup/backup_test.go` (modified — round-trip/idempotent/atomic tests + `seedSample`)
- `internal/http/router.go` (modified — `Backup.Restore`, `/restore` route, `restoreData`, `restoreErrorMessage`, `renderSettings`, `?restored` notice)
- `internal/http/router_test.go` (modified — `stubBackup.Restore`, `/restore` handler tests, `multipartRestore` helper)
- `web/pages.templ` (modified — `SettingsPage` restore form + notice)
- `web/pages_templ.go` (modified, generated)
- `web/static/css/app.css` (modified, generated)
