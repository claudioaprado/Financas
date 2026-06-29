---
baseline_commit: 1c05dce
---

# Story 3.6: Import transactions from a file

Status: review

<!-- Note: Validation is optional. Run validate-create-story for quality check before dev-story. -->

## Story

As the owner,
I want to import a tab-delimited statement file into an account,
so that I don't retype history.

## Acceptance Criteria

From `epics.md` → Epic 3 → Story 3.6 (realizes FR-13). **Given** a target Account and a `date<tab>description<tab>value` file, **When** I import it, **Then**:

1. Dates parse as `dd/mm/yy` or `dd/mm/yyyy` (two-digit-year pivot **00–69 → 2000s, 70–99 → 1900s**) and values parse in **Brazilian format** (dot = thousands, comma = decimal) (FR-13).
2. A **negative value becomes an Expense** and a **positive value an Income**, in the **Account's Currency**.
3. I **preview** parsed rows before committing, and an **unparseable row reports its reason without aborting** the batch.
4. **Re-importing the same file does not duplicate rows** — dedup key `(account_id, date, description, value)` + a stored per-row hash (idempotent).

> Scope: import creates **Income/Expense** rows (sign → type) into a **cash or credit** account (the income/expense rule from 3.1/3.2). Imported rows are uncategorized (the owner can classify later via the register). Transfers and investment trades are not importable here. The file is tab-delimited `date⇥description⇥value`, one row per line; the importer accepts an uploaded file or pasted text.

## Tasks / Subtasks

- [x] **Task 1 — `import_hash` column + idempotency index (AC: #4)**
  - [x] Add goose migration `db/migrations/00008_import_hash.sql`. Up: `ALTER TABLE transaction ADD COLUMN import_hash TEXT;` then `CREATE UNIQUE INDEX transaction_import_hash ON transaction (import_hash) WHERE import_hash IS NOT NULL;`. Down: drop the index, drop the column. (`-- +goose StatementBegin/End` per style.)
  - [x] The hash is the stored per-row natural key. Manually-entered transactions keep `import_hash = NULL` (the partial unique index ignores them); imported rows store a hash of `(account_id, date, description, signed value)`, so a re-import inserts nothing new.

- [x] **Task 2 — sqlc: imported-insert + existing-hashes queries (AC: #2, #4)**
  - [x] Add to `db/query/transaction.sql` (do **not** change `CreateTransaction` — avoid rippling Record/Transfer):
    - `CreateImportedTransaction :execrows` — `INSERT INTO transaction (type, from_account_id, to_account_id, from_amount, to_amount, occurred_on, description, import_hash) VALUES ($1..$8)` (income ⇒ to-side, expense ⇒ from-side; `category_id` defaults NULL).
    - `ListAccountImportHashes :many` — `SELECT import_hash FROM transaction WHERE import_hash IS NOT NULL AND (from_account_id = $1 OR to_account_id = $1)`.
  - [x] `make sqlc` (pinned Docker image); commit generated files. (`store.Transaction` gains `ImportHash pgtype.Text` — appended last, so no SELECT/RETURNING reorder needed for the existing queries.)

- [x] **Task 3 — `service/importer` parse (pure) (AC: #1, #2, #3)**
  - [x] Add `internal/service/importer/parse.go` (package **`importer`** — `import` is a Go keyword, so the package can't be named `import`). `Parse(content string) []ParsedRow` where `ParsedRow{Line int; Raw string; Date time.Time; Description string; Amount decimal.Decimal; Type TxType; OK bool; Reason string}` (`TxType` re-exported or use `transaction.TxType`). For each non-empty line, split on `\t` into exactly 3 fields (else `Reason = "expected 3 tab-separated fields"`):
    - **Date** `parseBRDate`: split on `/` → day/month/year ints; 2-digit year ⇒ pivot (`<= 69` → 2000+yy, else 1900+yy); build `time.Date(y, m, d, 0,0,0,0, time.UTC)` and **reject** if the normalized day/month differ from the input (catches `31/02`) ⇒ `Reason = "invalid date"`.
    - **Value** `parseBRDecimal`: strip thousands `.`, replace decimal `,` with `.`, `decimal.NewFromString`; **reject zero** (`Reason = "value must be non-zero"`) and parse errors (`Reason = "invalid value"`). `Amount = value.Abs()`, `Type = Expense` if value is negative else `Income`.
    - Description is the middle field (trimmed).
  - [x] Pure unit tests `parse_test.go` (no DB): valid `dd/mm/yy` + `dd/mm/yyyy`; pivot boundaries (`69` → 2069, `70` → 1970); Brazilian numbers (`1.234,56` → 1234.56, `-50,00` → expense 50, `1.000` → 1000); sign → type; invalid rows (bad date `31/02`, non-numeric value, zero, wrong field count) each flagged with a reason while other rows still parse.

- [x] **Task 4 — `service/importer` Preview/Commit (DB) (AC: #2, #3, #4)**
  - [x] Add `internal/service/importer/importer.go`: `New(pool)`; `Preview(ctx, accountID int64, content string) (Result, error)` and `Commit(ctx, accountID int64, content string) (Result, error)`. `Result{Account account-info; Rows []PreviewRow; New, Duplicate, Errors int}`; `PreviewRow{ParsedRow; Status string /* "new"|"duplicate"|"error" */; Display string}`.
  - [x] Both load the account via `store.GetAccount` (must exist and be **cash or credit**, else a typed error — reuse the income/expense rule), run `Parse`, and compute each OK row's **hash** = hex SHA-256 of `accountID + "\x00" + date(2006-01-02) + "\x00" + description + "\x00" + signedAmountString`. `Preview` loads `ListAccountImportHashes` into a set and labels each row new/duplicate/error (no writes). `Commit` inserts every **new** row (skip duplicates + errors) via `CreateImportedTransaction` in **one** transaction (AD-3) — income ⇒ `to_account = acct`, expense ⇒ `from_account = acct`, amount in the account's currency; dedupe within the same file too (a hash seen earlier in the batch ⇒ duplicate). Returns the counts.
  - [x] DB-gated test `importer_test.go`: import a small batch (income + expense + an error line) into a cash account ⇒ Preview marks 2 new / 1 error; Commit inserts 2 (correct types/amounts/currency, derivable via `transaction.Balance`); a second Commit of the same content inserts **0** (all duplicates); an error line never commits; reject a non-cash/credit account.

- [x] **Task 5 — Import page (preview → commit) (AC: #1, #2, #3, #4)**
  - [x] Add an authenticated import page reachable from the account-detail page (a link "Import transactions" on cash/credit accounts): `GET /accounts/{id}/import` (a file input + a textarea fallback + "Preview"). `POST /accounts/{id}/import/preview` reads the uploaded file (multipart `file`) or the `content` textarea, calls `Preview`, and renders the parsed rows table (line, date, description, type, amount, **status** new/duplicate/error+reason), the counts, the raw content in a hidden field, and a "Commit N new rows" button (disabled/hidden when `New == 0`). `POST /accounts/{id}/import/commit` reads the hidden `content`, calls `Commit`, and redirects to the account detail with the new rows visible (or re-renders the result summary).
  - [x] Add an `Imports` interface to `http.Deps` (`Preview`, `Commit`); inject `importer.New(pool)` in `main.go`. Add view structs to `web/shell.go` (`ImportRow`, etc.) and the templ `ImportPage`. `make generate css`; commit `*_templ.go` + `app.css`. Keep the five-item nav unchanged.

- [x] **Task 6 — Tests, verify, docs (AC: all)**
  - [x] `go build`/`go vet`/`go test ./...` + `make nofloat` clean (DB-gated tests skip without a DB; `nofloat` stays green — `decimal` parsing, **no float**, including the Brazilian-number path).
  - [x] http test (stub `Imports`): unauth `GET /accounts/{id}/import` → 302; authed GET renders the form; `POST …/import/preview` with sample text shows the rows + statuses; `POST …/import/commit` redirects; the stub records what was committed.
  - [x] Live smoke (compose db + run, logged in): on a cash account, import a few tab-delimited lines (mix of `dd/mm/yy`/`dd/mm/yyyy`, a `1.234,56`, a negative ⇒ expense, a deliberately bad date) ⇒ preview shows 1 error with a reason and the rest new; commit ⇒ rows appear in the register with correct sign/amount; **re-import the same content ⇒ preview shows all duplicates, commit adds 0**; confirm via the account balance.
  - [x] Update `README.md` briefly (import: tab-delimited `date⇥description⇥value`, `dd/mm/yy`/`dd/mm/yyyy` with the year pivot, Brazilian numbers, sign → income/expense in the account's currency, preview before commit, per-row error reporting, idempotent re-import via the stored hash).

## Dev Notes

### Scope boundary — what this story does NOT do (read first)

- **NO transfers / investment trades via import** — only Income/Expense (sign → type) into one cash/credit account.
- **NO categorization on import** — imported rows are uncategorized; classify later in the register (Story 3.4/3.5).
- **NO change to `CreateTransaction`** — a separate `CreateImportedTransaction` carries `import_hash`, so Record/Transfer/their callers are untouched (avoids a category_id-style ripple).
- **NO float anywhere** — Brazilian numbers are normalized to a decimal string and parsed with `decimal.NewFromString` (NFR-5).

### Architecture invariants this story must honor

- **Conventions — import dedup + year pivot.** Dedup key `(account_id, date, description, value)`; a per-row natural-key hash is stored so re-import is idempotent; two-digit years pivot 00–69 → 2000s, 70–99 → 1900s. [Source: ARCHITECTURE-SPINE.md#Consistency Conventions]
- **AD-3 — one DB transaction per use-case.** `Commit` inserts the whole new batch in one tx; a failure rolls back whole. [Source: ARCHITECTURE-SPINE.md#AD-3]
- **AD-2 / AD-4 / AD-5 — ledger rows, decimal magnitudes, native currency.** Imported rows are ordinary `Transaction` rows (Income/Expense) in the account's currency; balances/register derive as usual. [Source: ARCHITECTURE-SPINE.md#AD-2, #AD-4, #AD-5]
- **AD-1 — layering.** `service/importer` reads the account + existing hashes via `store`; `http` defines the `Imports` interface and renders preview/commit. Parsing is pure (no I/O). [Source: ARCHITECTURE-SPINE.md#AD-1]

### Previous-story intelligence (3.1–3.5) — load-bearing

[Source: 3-1 / 3-4 / 3-5; [[financas-epic1-progress]]]

- **The `transaction` ledger + `Balance`/`Register` exist.** Imported Income/Expense use the same one-row shape: income ⇒ `to_account = acct` + `to_amount`, expense ⇒ `from_account = acct` + `from_amount` (the `legs()` mapping from 3.1). `category_id` defaults NULL; the new `import_hash` defaults NULL for manual rows.
- **Column-append discipline (Story 3.4 gotcha):** `import_hash` is appended last by the ALTER, so existing `SELECT`/`RETURNING` lists (which end `… created_at, category_id`) are unaffected and keep reusing `store.Transaction` (which now also has `ImportHash pgtype.Text` at the end). Only the new `CreateImportedTransaction` lists `import_hash`.
- **Account read rule:** `service/importer` validates the account via `store.GetAccount` (type cash/credit), not by importing `service/account` (AD-1) — same as `service/transaction`.
- **Page pattern:** account-relative routes live under `/accounts/{id}/…` (3.1/3.3); add `/accounts/{id}/import`, `/import/preview`, `/import/commit`. Multipart upload: `req.ParseMultipartForm(…)` + `req.FormFile("file")`; fall back to the `content` form value. Carry the raw content to commit via a hidden field (re-parse on commit — deterministic).
- **money/decimal:** `decimal.NewFromString` (never float), `decimal.Decimal.Abs()`, `IsZero()`, `IsNegative()`; `money.New(amount, currency)`, `Money.String()`. `crypto/sha256` + `encoding/hex` for the hash. **Build `GOTOOLCHAIN=local`**; `make nofloat` guards `internal/{money,domain,service,store}` — the importer is under `service`, so **no float in the BR-number parser**. Local DB host **5433**; DB-gated tests skip without `DATABASE_URL`/`TEST_DATABASE_URL`. Dev login `owner`/`financas`. `baseline_commit` real SHA `1c05dce`. Commit + push to `main` when done.

### Project Structure Notes

New: `db/migrations/00008_import_hash.sql`, `internal/service/importer/parse.go` (+ `parse_test.go`), `internal/service/importer/importer.go` (+ `importer_test.go`), `web` `ImportPage` (+ regenerated `pages_templ.go`), `web/shell.go` view structs. Modified: `db/query/transaction.sql` (+`CreateImportedTransaction`, `ListAccountImportHashes`) + regenerated `internal/store/transaction.sql.go`/`models.go`/`querier.go`; `internal/http/router.go` (`Imports` iface, import routes/handlers, link from account detail) + `router_test.go`; `cmd/server/main.go` (wire `importer.New`); `web/pages.templ` (account-detail import link), `README.md`.

### Testing standards

- `importer` (pure): exhaustive `Parse` table tests — dates (both formats, pivot boundaries, invalid), Brazilian numbers, sign→type, per-row error reasons, batch resilience.
- `importer` (DB-gated): Preview new/duplicate/error labelling; Commit inserts new only, idempotent re-commit (0 new), one-tx; non-cash/credit account rejected.
- `http`: stub `Imports` — auth gate, preview render, commit redirect.
- `go test ./...` green with no DB; `go vet` + `make nofloat` clean (no float in the parser).

### References

- [Source: epics.md#Story 3.6] — acceptance criteria; [Source: epics.md FR-13] — tab-delimited import, formats, preview, per-row errors, idempotency
- [Source: ARCHITECTURE-SPINE.md#Consistency Conventions] — dedup key + per-row hash, two-digit-year pivot
- [Source: ARCHITECTURE-SPINE.md#AD-3 / #AD-2 / #AD-1] — one tx; ledger rows; layering, store-read rule
- [Source: 3-1-record-cash-income-expenses.md] — `legs()` income/expense mapping; [Source: 3-4] — column-append sqlc discipline

## Dev Agent Record

### Agent Model Used

claude-opus-4-8[1m] (Opus 4.8, 1M context)

### Debug Log References

- `make migrate` applied `00008` (goose v8: `import_hash TEXT` + a partial unique index `WHERE import_hash IS NOT NULL`). `make sqlc` generated `CreateImportedTransaction`/`ListAccountImportHashes` and added `Transaction.ImportHash pgtype.Text`.
- **Column-append gotcha (again):** adding `import_hash` made the partial-column `SELECT`/`RETURNING` lists (`… created_at, category_id`) no longer match the full table, so sqlc regenerated bespoke `CreateTransactionRow`/`ListAccountTransactionsRow`/etc. — breaking `toTransaction`. Fixed by appending `import_hash` to the full-row column lists of `CreateTransaction`, `ListAccountTransactions`, `ListTransactions`, and `ListCategoryTransactions` so they keep reusing `store.Transaction`. (Lesson: when a table column is added, every `SELECT *`-style query must list it.)
- `go build`/`go vet`/`make nofloat` clean — the Brazilian-number parser normalizes to a decimal string and uses `decimal.NewFromString`; **no float** in `service/importer`. `gofmt -w` applied.
- Live DB: `importer.TestParse`/`TestPivotBoundary` (pure: both date formats, pivot 69→2069 / 70→1970, `1.234,56`→1234.56, sign→type, bad date/value/zero/field-count reasons), `importer.TestImport` (Preview 2 new / 1 error; Commit inserts 2 → balance 3765.44; re-Commit 0 new; non-cash/credit rejected) PASS.
- Live HTTP smoke (server :8101 + db :5433, owner/financas): **file-upload** preview shows income `+5000.0000 USD`, expense `-1234.5600 USD`, the `31/02` row as `error: invalid date`, and "Commit 2 new rows"; commit → balance `3765.4400 USD`; **re-import preview ⇒ all duplicate / "No new rows to import"**, re-commit leaves the balance unchanged; the DB holds 2 rows with `import_hash`.

### Completion Notes List

All four acceptance criteria verified (pure unit + live DB + live HTTP):
- **AC1 — formats:** `parseBRDate` handles `dd/mm/yy`/`dd/mm/yyyy` with the 00–69→2000s / 70–99→1900s pivot and rejects impossible dates (e.g. `31/02`); `parseBRDecimal` reads Brazilian numbers (strip `.` thousands, `,`→`.` decimal).
- **AC2 — sign → type, native currency:** negative ⇒ Expense, positive ⇒ Income, amount = `value.Abs()`, stored via the AD-9 `legs()` mapping (income→to, expense→from) in the account's currency.
- **AC3 — preview + per-row errors:** `Preview` parses and labels rows new/duplicate/error without writing; an unparseable row carries a Reason and does **not** abort the batch (valid rows still import). The page shows the table + counts before a `Commit` button.
- **AC4 — idempotent:** a stored per-row SHA-256 of `(account_id, date, description, signed value)` (`import_hash`, unique partial index) + an in-batch `seen` set make re-import a no-op; `Commit` inserts only `new` rows in **one** tx (AD-3).

Decisions / variances (intentional):
- **Separate `CreateImportedTransaction` query** carries `import_hash`, so `CreateTransaction`/Record/Transfer are untouched (no ripple). Imported rows are uncategorized; manual rows keep `import_hash = NULL` (excluded by the partial unique index).
- **`service/importer` (package can't be `import`)** reads the account + existing hashes via `store` (cash/credit only, `ErrUnsupportedAccountType`) — no `service → service` dep; it replicates the 2-line `legs()` income/expense placement rather than importing `service/transaction`.
- **Preview→Commit carries the raw content in a hidden field** and re-parses on commit (deterministic) — the file is uploaded once for preview (multipart `file`), with a textarea fallback; commit posts the content urlencoded.
- **Full-row query column lists now end `… category_id, import_hash`** (see Debug Log) to keep reusing `store.Transaction`.

Reviewer notes: no `sprint-status.yaml` → status tracked in this file only. `baseline_commit` is the real SHA `1c05dce` (HEAD before this story). Committed + pushed to `main` per the owner's standing instruction. **This completes Epic 3.**

### File List

New:
- `db/migrations/00008_import_hash.sql`
- `internal/service/importer/parse.go`, `parse_test.go`, `importer.go`, `importer_test.go`
- `web` `ImportPage` (in `web/pages.templ`)

Modified:
- `db/query/transaction.sql` (+`CreateImportedTransaction`, `+ListAccountImportHashes`, `import_hash` appended to full-row SELECT/RETURNING lists), `db/query/category.sql` (`import_hash` appended to `ListCategoryTransactions`) → regenerated `internal/store/transaction.sql.go`/`category.sql.go`/`models.go`/`querier.go`
- `internal/http/router.go` (`Imports` iface, `/accounts/{id}/import`(`/preview`|`/commit`) routes + handlers, `readImportContent` multipart/textarea, import link import `io`) + `router_test.go` (stub `Imports`, `TestImportPreviewAndCommit`)
- `cmd/server/main.go` (wire `importer.New(pool)`)
- `web/pages.templ` (`ImportPage` + account-detail import link) + regenerated `web/pages_templ.go`, `web/shell.go` (`ImportRow`), rebuilt `web/static/css/app.css`
- `README.md` (import section)

## Change Log

| Date | Change |
| --- | --- |
| 2026-06-29 | Story 3.6 drafted (create-story): tab-delimited import (`dd/mm/yy[yy]` + year pivot, Brazilian numbers, sign→type), preview-before-commit with per-row error reporting, idempotent re-import via stored `import_hash`. Status → ready-for-dev. |
| 2026-06-29 | Story 3.6 implemented (dev-story): `00008` `import_hash` + partial unique index; `service/importer` (pure BR parser + Preview/Commit, idempotent, one tx); `/accounts/{id}/import` preview→commit page (file upload + textarea). All 4 ACs verified (pure unit + live DB + live HTTP). build/vet/test + nofloat green. **Epic 3 complete.** Status → review. |
