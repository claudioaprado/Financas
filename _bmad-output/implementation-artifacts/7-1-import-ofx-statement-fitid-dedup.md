---
baseline_commit: 325980b
epic: 7
story: 7.1
phase: 2
---

# Story 7.1: Import an OFX statement (FITID dedup)

Status: review

<!-- Note: Validation is optional. Run validate-create-story for quality check before dev-story. -->

## Story

As the owner,
I want to import a bank/credit-card OFX file into an account,
so that I don't retype statements and re-imports never duplicate.

## Acceptance Criteria

From `epics-phase2.md` → Epic 7 → Story 7.1 (realizes FR-16). **Given** an OFX file and a chosen Account, **When** I preview then commit the import, **Then**:

1. Each `STMTTRN` maps to an Income/Expense **in the Account's Currency** — `TRNAMT` sign → type (negative ⇒ Expense, positive ⇒ Income), `DTPOSTED` → date, `NAME`/`MEMO` → description.
2. A row whose **`(account, FITID)` already exists** is shown as a duplicate and **not inserted** (idempotent re-import) — this is the **ONLY** dedup key.
3. Two transactions with the **same date, description, and value are BOTH imported** (never treated as duplicates — identical fields are legitimate); a `STMTTRN` **with no FITID is imported as new**, with a **visible warning** that re-importing it may duplicate.
4. **Malformed/unparseable records are reported per-row without aborting the batch**, and the commit writes in **one DB transaction** (AD-3).

> **Scope:** OFX import creates **Income/Expense** rows (sign → type) into a **cash or credit** account — exactly like the tab-delimited importer (Story 3.6), reusing its Preview→Commit machinery and page. Imported rows are **uncategorized** (auto-categorization suggestions are **Story 7.2**, not this story). Transfers and investment trades are **not** importable. Amounts are taken in the **Account's Currency** (OFX `CURDEF` is not validated/converted in v1 — see Non-goals).

## Locked Decisions (from `epics-phase2.md` frontmatter — do NOT relitigate)

- **OFX dedup is FITID-ONLY.** Content dedup by `(date, description, value)` is **explicitly REJECTED**: two legitimate transactions can share the same date/description/value and must **NEVER** be discarded. Only an identical OFX `FITID` (scoped to the account) means "same transaction". A `STMTTRN` with no `FITID` is imported as **new** (never content-deduped) with a warning.
- **This story must NOT touch the tab-delimited importer's content-hash dedup** (`rowHash`/`import_hash`, Story 3.6). Whether to revise that is a **separate owner decision** flagged in the plan — out of scope here.
- **No auto-categorization** in this story (that is 7.2). Imported rows are uncategorized.

## Tasks / Subtasks

- [x] **Task 1 — `fitid` column + per-account idempotency index (AC: #2, #3)**
  - [x] Add goose migration `db/migrations/00012_transaction_fitid.sql` (next number after `00011_prices.sql`). Up:
    - `ALTER TABLE transaction ADD COLUMN fitid TEXT;`
    - `CREATE UNIQUE INDEX transaction_account_fitid ON transaction (COALESCE(from_account_id, to_account_id), fitid) WHERE fitid IS NOT NULL;`
    - Down: `DROP INDEX transaction_account_fitid;` then `ALTER TABLE transaction DROP COLUMN fitid;` (`-- +goose StatementBegin/End` around each statement, matching `00008`'s style).
  - [x] **Why `COALESCE(from_account_id, to_account_id)`:** an imported Income sets `to_account_id`, an Expense sets `from_account_id` — exactly one side is set (AD-9 income/expense shape), so `COALESCE` is the deterministic "owning account". This scopes FITID uniqueness **per account** (a bank's `FITID` is unique only within its own statement/account, so two accounts may legitimately share a `FITID`). Manually-entered rows and transfers keep `fitid = NULL` and are excluded by the partial index.
  - [x] `make migrate` (goose; local DB host **5433**). Verify up+down.

- [x] **Task 2 — sqlc: OFX-insert + existing-fitids queries + append `fitid` to every full-row list (AC: #1, #2)**
  - [x] Add to `db/query/transaction.sql` (do **NOT** change `CreateImportedTransaction` — the tab path stays intact):
    - `CreateOFXTransaction :execrows` — `INSERT INTO transaction (type, from_account_id, to_account_id, from_amount, to_amount, occurred_on, description, fitid) VALUES ($1..$8)` (income ⇒ to-side, expense ⇒ from-side; `category_id`, `import_hash` default NULL).
    - `ListAccountFitids :many` — `SELECT fitid FROM transaction WHERE fitid IS NOT NULL AND (from_account_id = $1 OR to_account_id = $1)`.
  - [x] **CRITICAL column-append discipline (this bit the team twice — Story 3.4 and 3.6).** Adding a column makes every `SELECT *`-equivalent full-row list stop matching `store.Transaction`, so sqlc regenerates bespoke row structs and breaks `toTransaction`/backup mapping. **Append `fitid` (last) to ALL of these full-row column lists** so they keep reusing `store.Transaction` (which gains `Fitid pgtype.Text` at the end):
    - `db/query/transaction.sql`: `CreateTransaction` (RETURNING …), `ListAccountTransactions`, `ListTransactions`.
    - `db/query/category.sql`: `ListCategoryTransactions`.
    - `db/query/backup.sql`: **`ExportTransactions`** (SELECT) **and** **`RestoreInsertTransaction`** (add column + a `$16` value) — see Task 3 for why backup must carry it.
  - [x] `make sqlc` (pinned Docker image); commit the regenerated `internal/store/*.sql.go`, `models.go`, `querier.go`. Confirm `store.Transaction` gained exactly one trailing field `Fitid pgtype.Text` and no existing row struct got renamed/split.

- [x] **Task 3 — backup round-trip preserves `fitid` (AC: #2 — regression guard)**
  - [x] `fitid` is **authored state stored on the transaction row**, so an export→restore must preserve it or a restore silently drops all dedup keys (re-importing the same OFX after a restore would then duplicate). In `internal/service/backup/backup.go`, add `Fitid` to the transaction export DTO and to the `RestoreInsertTransaction` params mapping (mirror how `ImportHash` is already carried).
  - [x] **Backward-compat:** an OLD export (JSON without a `fitid` field) must still restore — Go's `encoding/json` leaves the missing field at zero value ⇒ `fitid = NULL`. Add/extend a backup test asserting: (a) a transaction with a `fitid` survives export→restore with the same value; (b) decoding an export that omits `fitid` yields `NULL` (no error). Do **not** bump the export version unless the existing backup format already versions and a reviewer requires it — a nullable additive field is forward/backward tolerant.

- [x] **Task 4 — `service/importer`: pure OFX parser (AC: #1, #3, #4)**
  - [x] Add `internal/service/importer/ofx.go` (same package `importer`). Extend `ParsedRow` with **`FITID string`** (the tab `Parse` leaves it `""`). `ParseOFX(content string) []ParsedRow`.
  - [x] **No OFX library** — hand-roll a tolerant tag scanner (go.mod adds nothing). Must handle **both** OFX 1.x SGML (unclosed tags; a tag's value runs to end-of-line or the next `<`) **and** OFX 2.x XML (`<TAG>value</TAG>`). Extract each `<STMTTRN>…</STMTTRN>` block (in OFX 1.x, a `STMTTRN` ends at the next `<STMTTRN>` or `</BANKTRANLIST>`). Within a block read: `FITID`, `DTPOSTED`, `TRNAMT`, `NAME`, `MEMO` (`TRNTYPE` optional/ignored).
    - **Date** (`DTPOSTED`): OFX format is `YYYYMMDD[HHMMSS[.XXX]][[±TZ:name]]`. Take the **first 8 chars** → `time.Date(y, m, d, 0,0,0,0, time.UTC)`; reject if not 8 digits or not a real calendar date (same normalization check as `parseBRDate`) ⇒ error row `Reason = "invalid DTPOSTED"`.
    - **Value** (`TRNAMT`): standard OFX numeric — `.` is the **decimal** point, optional leading `-`, **no** thousands separator. Parse with `decimal.NewFromString` directly (**NOT** `money.ParseDecimal`, which is pt-BR). Reject empty/unparseable ⇒ `Reason = "invalid TRNAMT"`; reject zero ⇒ `Reason = "value must be non-zero"`. `Amount = value.Abs()`; `Type = expense` if negative else `income`. **No float** (NFR-5).
    - **Description:** `NAME` trimmed; if empty use `MEMO`; if both present, `NAME` (keep it simple — do not concatenate). If both empty, description `""`.
    - **FITID:** trimmed. **Empty FITID is NOT an error** — the row is `OK=true` with `FITID=""`; the "may duplicate" warning is applied later in classify (Task 5), never here.
    - A block missing `TRNAMT` or `DTPOSTED` ⇒ error row (with `Line`/`Raw` best-effort pointing at the block) — batch continues.
  - [x] Pure unit tests `ofx_test.go` (no DB): parse a small **OFX 1.x SGML** sample with several `STMTTRN` — assert FITID/date/amount/type/description; `TRNAMT` `-1234.56` ⇒ expense 1234.56, `+5000.00` ⇒ income; `DTPOSTED` with a time+tz suffix (`20240115120000.000[-3:BRT]`) ⇒ date `2024-01-15`; a `STMTTRN` with **no FITID** ⇒ `OK=true, FITID=""`; a block with bad `DTPOSTED` / bad `TRNAMT` / missing `TRNAMT` each ⇒ error row while the others still parse; **two `STMTTRN` with identical date+NAME+TRNAMT but different FITID ⇒ two parsed rows** (regression guard: no content dedup at parse). Add one **OFX 2.x XML** sample to prove both flavors parse.

- [x] **Task 5 — `service/importer`: OFX Preview/Commit (FITID dedup) (AC: #2, #3, #4)**
  - [x] In `internal/service/importer/importer.go` add `PreviewOFX(ctx, accountID int64, content string) (Result, error)` and `CommitOFX(ctx, accountID int64, content string) (Result, error)`. Reuse the existing `account()` validation (cash/credit only, `ErrUnsupportedAccountType`), `Result`, and `PreviewRow`.
  - [x] Add a `Warning string` field to `PreviewRow` (tab path leaves it `""`). Add a `classifyOFX(acct, content, existing map[string]bool) Result` that:
    - Loads existing fitids via new `existingFitids(ctx, accountID)` (mirrors `existingHashes`, but uses `ListAccountFitids`).
    - For each `ParseOFX` row: `!OK` ⇒ Status `"error"`. Else if `FITID == ""` ⇒ Status `"new"` **+ `Warning = "no FITID — re-importing may duplicate"`** (NEVER deduped). Else dedup **by FITID only** against `existing` **and** an in-batch `seen` set ⇒ `"duplicate"` or `"new"`.
    - **Never compute or consult a content hash for OFX.** Do not call `rowHash`.
  - [x] `CommitOFX` inserts every `"new"` row via `CreateOFXTransaction` (income ⇒ `to_account = acct`, expense ⇒ `from_account = acct`, amount in the account's currency, `fitid` = the row's FITID or `pgtype.Text{}` invalid when empty) in **one** tx (AD-3), reusing the `legs()` mapping. Rows with empty FITID insert `fitid = NULL` (so the partial index doesn't collide them — they are intentionally re-importable duplicates).
  - [x] DB-gated test (extend `importer_test.go` or add `ofx_test.go`, reuse the `testDatabaseURL` skip + `store.Migrate` + real `account.New` pattern already in `importer_test.go`):
    - Preview an OFX batch (2 valid with FITID + 1 error) ⇒ 2 new / 1 error; Commit inserts 2 with correct type/amount/currency (verify via `transaction.Balance`).
    - **Re-import the same OFX ⇒ 0 new** (all duplicate by FITID) — idempotent.
    - **Two `STMTTRN` with identical date/description/value but different FITID ⇒ BOTH import** (the core anti-content-dedup guard).
    - **A `STMTTRN` with no FITID ⇒ imports as new WITH `Warning` set; re-importing it imports it AGAIN** (documents/proves the known duplicate risk — asserts the count increases, proving no content dedup).
    - Non-cash/credit account rejected (`ErrUnsupportedAccountType`).

- [x] **Task 6 — Import page: OFX format option + FITID warning (AC: #1, #3, #4)**
  - [x] Extend the existing `/accounts/{id}/import` page (`web.ImportPage`) with an explicit **format selector** — a `<select name="format">` (or radio): `tab` = "Separado por tabulação" (default, current behavior) and `ofx` = "OFX (extrato bancário)". No new route; the same preview/commit endpoints branch on `format`.
  - [x] Extend the `Imports` interface in `internal/http/router.go` with `PreviewOFX`/`CommitOFX`. In `importPreview`/`importCommit`, read `req.FormValue("format")`; `ofx` ⇒ call the OFX methods, else the tab methods. Carry `format` to commit via a **hidden field** next to the existing hidden `content` (commit must reuse the same format the preview used). `readImportContent` already reads file-or-textarea — reuse it unchanged (an OFX file arrives as multipart `file`).
  - [x] Add `Warning string` to `web.ImportRow`; in `renderImport` copy `r.Warning` from the `PreviewRow`. In the templ, when `Status == "new"` and `Warning != ""` render the warning inline (e.g. `novo · sem FITID (pode duplicar)` in a muted/`text-loss` hint) — keep the existing new/duplicate/error rendering for the rest.
  - [x] `make generate` (templ) and rebuild css if touched; commit `*_templ.go` (+ `app.css` if changed). Keep the nav unchanged.

- [x] **Task 7 — Wire, tests, verify, docs (AC: all)**
  - [x] The importer `Service` already satisfies the extended `Imports` interface once `PreviewOFX`/`CommitOFX` exist (it's already injected in `cmd/server/main.go` — no new wiring, just the wider interface). Confirm `main.go` still compiles.
  - [x] http test (`router_test.go`): extend the `Imports` **stub** with `PreviewOFX`/`CommitOFX`; assert `POST …/import/preview` with `format=ofx` routes to the OFX path and renders rows incl. a FITID warning; `format=tab` (or absent) still routes to the tab path (no regression); commit redirects and records the format used.
  - [x] `GOTOOLCHAIN=local go build ./... && go vet ./...`, `go test ./...` (DB-gated tests skip without `TEST_DATABASE_URL`), `make nofloat` green (the OFX amount parser uses `decimal`, **no float**), `gofmt` clean.
  - [x] **Live smoke** (`docker compose up -d db`; free :8080 first with `lsof -ti tcp:8080 | xargs kill`; login `owner`/`financas`): on a cash account, OFX-import a small sample (2 rows with FITID, 1 without, 1 malformed) ⇒ preview shows 2 new / 1 new+warning / 1 error and the counts; commit ⇒ rows appear in the register with correct sign/amount/currency; **re-import the same OFX ⇒ the FITID rows are all duplicate (0 new), the no-FITID row shows new again**; confirm via the account balance. If the smoke touched the base `financas` DB, restore `UPDATE app_settings SET display_currency='USD';` and free :8080 after.
  - [x] Update `README.md` briefly (OFX import: `STMTTRN` → Income/Expense in the account's currency, `TRNAMT` sign → type, `DTPOSTED` → date, `NAME`/`MEMO` → description, **FITID-only** idempotent dedup scoped per account, no-FITID rows import with a duplicate warning, per-row error reporting, one-tx commit).

## Dev Notes

### Scope boundary — what this story does NOT do (read first)

- **NO content dedup for OFX** — dedup is `(account, FITID)` only. Do **not** call `rowHash`/reuse the `import_hash` path for OFX. Identical date/description/value rows with different FITIDs are BOTH legitimate.
- **NO change to the tab-delimited importer** (`Parse`, `rowHash`, `import_hash`, `CreateImportedTransaction`, `Preview`/`Commit`). Its content-hash dedup is a separate, flagged owner decision — untouched here.
- **NO auto-categorization** — imported rows are uncategorized (Story 7.2 adds rule-suggested categories in the preview).
- **NO transfers / investment trades** via OFX — Income/Expense only into one cash/credit account (same as 3.6).
- **NO CURDEF currency validation/conversion** — amounts are taken in the account's currency (AC1). See Non-goals.
- **NO float anywhere** — OFX `TRNAMT` is normalized to a decimal string and parsed with `decimal.NewFromString` (NFR-5); `make nofloat` guards `service/importer`.

### Architecture invariants this story must honor

- **AD-2 / AD-4 / AD-5 — ledger rows, decimal magnitudes, native currency.** OFX rows are ordinary `Transaction` Income/Expense rows in the account's currency; balances/register/valuation derive as usual. `fitid` is a new authored column on the row (not a derived figure). [Source: ARCHITECTURE-SPINE.md#AD-2, #AD-4, #AD-5]
- **AD-3 — one DB transaction per use-case.** `CommitOFX` inserts the whole new batch in one tx; a failure rolls back whole. [Source: ARCHITECTURE-SPINE.md#AD-3]
- **AD-9 — one-row income/expense shape.** income ⇒ `to_account = acct` + `to_amount`; expense ⇒ `from_account = acct` + `from_amount` (reuse `legs()`). Exactly-one-side is why the FITID index keys on `COALESCE(from,to)`. [Source: ARCHITECTURE-SPINE.md#AD-9]
- **AD-1 — layering.** `service/importer` reads the account + existing fitids via `store`; parsing is pure (no I/O); `http` defines the widened `Imports` interface and renders. No `service → service` dep. [Source: ARCHITECTURE-SPINE.md#AD-1]
- **Import idempotency convention** is superseded **for OFX** by FITID-only (the convention table's `(account,date,description,value)` key describes the tab importer). The plan's locked decision overrides the generic convention for OFX. [Source: epics-phase2.md#lockedDecisions; ARCHITECTURE-SPINE.md#Consistency Conventions]

### Previous-story intelligence — load-bearing

[Source: 3-6-import-transactions-from-file.md; ARCHITECTURE-SPINE.md; [[financas-quality-faxina]], [[financas-i18n]]]

- **Reuse the 3.6 Preview→Commit spine.** `Service{pool}`; `account()` validates cash/credit; `Result{AccountName, Currency, Rows, New, Duplicate, Errors}`; `PreviewRow{ParsedRow; Status}`; `legs()` maps income→to / expense→from; `classify` labels new/duplicate/error and dedups in-batch with a `seen` set. OFX gets a **parallel** `ParseOFX` + `classifyOFX` + `PreviewOFX`/`CommitOFX`; the tab path is byte-for-byte unchanged.
- **The column-append gotcha is now bigger.** In 3.6 only 4 lists needed `import_hash`. Since then Epic 4 added `security_id, quantity, price, fees` and Epic 6 added `backup.sql` export+restore. The current full-row tail everywhere is `… created_at, category_id, import_hash, security_id, quantity, price, fees`. Append `fitid` **last** in all 6 sites (Task 2). If sqlc emits new bespoke `…Row` structs instead of reusing `store.Transaction`, a list was missed.
- **Separate insert query, no ripple.** Just as 3.6 added `CreateImportedTransaction` rather than touching `CreateTransaction`, add `CreateOFXTransaction` rather than parameterizing the existing one. Keeps Record/Transfer/tab-import untouched.
- **http error mapping (faxina).** Reuse `problemMsg`/`knownErrMsg`; `ErrAccountNotFound`/`ErrUnsupportedAccountType` already map to pt-BR at router.go:346–350. Do not leak raw `err.Error()`. Primary-load failures → `logLoad` + banner.
- **i18n:** UI strings pt-BR; the OFX warning/labels render pt-BR. `templ` escapes `&`→`&amp;` and `'`→`&#39;` — adjust http asserts accordingly. As-of/DTPOSTED dates are UTC.
- **money/decimal:** `decimal.NewFromString` (never float), `Abs()`, `IsZero()`, `IsNegative()`. Build `GOTOOLCHAIN=local`. Local DB host **5433**; DB-gated tests skip without `TEST_DATABASE_URL`/`DATABASE_URL`; the importer test reuses `store.Migrate` + `account.New` (no `isolatedDB` needed — it already skips cleanly and creates uniquely-named accounts).
- **settings suite shares the base DB** and assumes `display_currency='USD'` — if a smoke changes it, restore it. Free :8080 before/after live runs.

### Project Structure Notes

- **New:** `db/migrations/00012_transaction_fitid.sql`; `internal/service/importer/ofx.go` (+ `ofx_test.go`).
- **Modified:** `db/query/transaction.sql` (+`CreateOFXTransaction`, `+ListAccountFitids`, `fitid` appended to `CreateTransaction`/`ListAccountTransactions`/`ListTransactions`), `db/query/category.sql` (`fitid` on `ListCategoryTransactions`), `db/query/backup.sql` (`fitid` on `ExportTransactions` + `RestoreInsertTransaction`) → regenerated `internal/store/{transaction,category,backup}.sql.go`, `models.go`, `querier.go`.
- `internal/service/importer/importer.go` (`FITID` on `ParsedRow`, `Warning` on `PreviewRow`, `existingFitids`, `classifyOFX`, `PreviewOFX`, `CommitOFX`) + test.
- `internal/service/backup/backup.go` (carry `Fitid` in export DTO + restore params) + `backup_test.go`.
- `internal/http/router.go` (widen `Imports` iface with `PreviewOFX`/`CommitOFX`; `format` branch in `importPreview`/`importCommit`; `Warning` into `web.ImportRow`) + `router_test.go` (stub + `format=ofx` test).
- `web/shell.go` (`ImportRow.Warning`), `web/pages.templ` (`ImportPage` format selector + warning render) + regenerated `web/pages_templ.go` (+ `app.css` if touched).
- `README.md` (OFX import section).
- `cmd/server/main.go` — no code change expected (already injects `importer.New(pool)`); confirm it still satisfies the widened interface.

### Testing standards

- **`ofx.go` (pure):** exhaustive `ParseOFX` table tests — OFX 1.x SGML + one 2.x XML; sign→type; `DTPOSTED` with/without time+tz; NAME vs MEMO description; empty FITID (OK, no error); malformed blocks flagged per-row without aborting; two-different-FITID/same-content ⇒ two rows.
- **`importer` (DB-gated):** `PreviewOFX`/`CommitOFX` new/duplicate(by FITID)/error; idempotent re-import (0 new); different-FITID/same-content BOTH import; no-FITID imports+warns and re-imports again; one tx; non-cash/credit rejected.
- **`backup` (DB-gated):** `fitid` survives export→restore; an export omitting `fitid` restores as NULL.
- **`http`:** stub `Imports` incl. `PreviewOFX`/`CommitOFX`; `format=ofx` routes + renders warning; `format=tab`/absent unchanged; commit redirect.
- `go test ./...` green with no DB; `go vet` + `make nofloat` clean (no float in the OFX parser); `gofmt` clean.

### References

- [Source: epics-phase2.md#Story 7.1] — acceptance criteria; [Source: epics-phase2.md#FR-16] — STMTTRN mapping, FITID-only idempotency, no-FITID warning, per-row errors, one tx.
- [Source: epics-phase2.md#lockedDecisions] — FITID-only; content dedup rejected.
- [Source: epics-phase2.md#New DB objects] — `transaction.fitid TEXT` + partial-unique `(account scope, fitid)`.
- [Source: ARCHITECTURE-SPINE.md#AD-3 / #AD-2 / #AD-9 / #AD-1] — one tx; ledger rows; one-row income/expense shape; layering.
- [Source: 3-6-import-transactions-from-file.md] — Preview→Commit spine, `legs()`, column-append gotcha, `service/importer` package rationale.
- [Source: db/query/transaction.sql, category.sql, backup.sql] — the 6 full-row column lists that must gain `fitid`.

### Non-goals / noted future refinements

- **CURDEF vs account currency:** v1 imports in the account's currency and does not validate/convert `CURDEF`. A future refinement could warn when `CURDEF` ≠ account currency. (Not required by AC1.)
- **Auto-categorization** of imported rows — Story 7.2.
- **Tab-importer content-hash dedup revision** — separate flagged owner decision (plan §"Flag: existing tab-delimited import dedup").

## Dev Agent Record

### Agent Model Used

claude-opus-4-8[1m] (Opus 4.8, 1M context)

### Debug Log References

- `make migrate` applied `00012_transaction_fitid.sql` (goose v3.21.1) → `fitid TEXT` + partial-unique `transaction_account_fitid` on `COALESCE(from_account_id, to_account_id), fitid`. `make sqlc` (Docker sqlc 1.27.0) regenerated cleanly: `store.Transaction` gained one trailing `Fitid pgtype.Text`, new `CreateOFXTransaction`/`ListAccountFitids`; **no new bespoke Row structs** (the 6 full-row lists were appended in lock-step, confirming the column-append gotcha was handled).
- Parser TDD: `ofx_test.go` (pure) written first and confirmed RED (`undefined: ParseOFX`), then `ofx.go` made it GREEN. Handles OFX 1.x SGML (unclosed leaf tags, value → EOL/next `<`) and 2.x XML (closed tags), DTPOSTED time+tz suffix, SGML/XML entity unescape, per-row errors.
- `go build`/`go vet`/`gofmt`/`make nofloat` all clean — the OFX `TRNAMT` parser uses `decimal.NewFromString` (dot-decimal, **no float**; distinct from the pt-BR `money.ParseDecimal`).
- DB-gated (`TEST_DATABASE_URL`, host 5433): `TestImportOFX` (Preview 3 new/0 dup/1 err → 1 warning; Commit balance 3755.44; re-import → 1 new/2 dup, no-FITID re-imports → 3745.44; different-FITID/same-content BOTH import → -84; investment rejected), `TestImport` (tab path unchanged), `TestRestorePreservesFitid` (round-trip + pre-7.1 export → NULL). Full `go test ./...` green (incl. settings suite — `display_currency` untouched/restored to USD).
- Live smoke (server :8080, owner/financas, cash account): OFX file with income+expense (FITID), a no-FITID row, and a 30-Feb error → preview "Confirmar 3 novas linhas" + "erro: invalid DTPOSTED" + "sem FITID" warning; commit → balance 3755.44; **re-import preview → 2 duplicado (by FITID) + 1 new (the no-FITID row)**; commit → 3745.44. An investment account correctly showed the pt-BR guard "A importação exige uma conta de caixa ou crédito." Env restored (display_currency='USD', :8080 freed).

### Completion Notes List

All four ACs verified (pure unit + live DB + live HTTP smoke):
- **AC1 — STMTTRN mapping:** `TRNAMT` sign → Income/Expense, `DTPOSTED` (first 8 digits, UTC) → date, `NAME` (else `MEMO`) → description, amount in the account's currency.
- **AC2 — FITID-only idempotency:** dedup key is `(account, FITID)` via a per-account partial-unique index + a service-level existing/seen set; re-importing skips known FITIDs, never a content hash.
- **AC3 — no content dedup / no-FITID warning:** two transactions with identical date/description/value + different FITIDs both import (proven pure + DB); a no-FITID `STMTTRN` imports as new with a visible "sem FITID" caveat and is intentionally re-importable.
- **AC4 — resilient batch + one tx:** malformed records are flagged per-row (`invalid DTPOSTED`/`invalid TRNAMT`/`value must be non-zero`/missing field) without aborting; `CommitOFX` inserts all new rows in one DB transaction (AD-3).

Decisions / variances (intentional):
- **Parallel OFX path** (`ParseOFX`/`classifyOFX`/`PreviewOFX`/`CommitOFX` + `CreateOFXTransaction`/`ListAccountFitids`) leaves the tab importer (content-hash `rowHash`/`import_hash`) byte-for-byte unchanged — mirrors Epic 3's "separate query, no ripple" lesson.
- **Per-account FITID scope** via `COALESCE(from_account_id, to_account_id)` (imported income sets to-side, expense from-side ⇒ exactly one side; AD-9).
- **Backup round-trip carries `fitid`** (authored state) with a `fitid,omitempty` DTO field so a pre-7.1 export restores it as NULL.
- **No OFX library** added (hand-rolled tolerant scanner) and **no CURDEF validation** (amounts taken in the account's currency per AC1) — CURDEF handling noted as a future refinement.

### File List

New:
- `db/migrations/00012_transaction_fitid.sql`
- `internal/service/importer/ofx.go`, `ofx_test.go`, `ofx_import_test.go`

Modified:
- `db/query/transaction.sql` (+`CreateOFXTransaction`, +`ListAccountFitids`, `fitid` appended to `CreateTransaction`/`ListAccountTransactions`/`ListTransactions`), `db/query/category.sql` (`ListCategoryTransactions`), `db/query/backup.sql` (`ExportTransactions` + `RestoreInsertTransaction`) → regenerated `internal/store/{transaction,category,backup}.sql.go`, `models.go`, `querier.go`
- `internal/service/importer/parse.go` (`FITID` on `ParsedRow`), `internal/service/importer/importer.go` (`Warning` on `PreviewRow`, `existingFitids`, `classifyOFX`, `PreviewOFX`, `CommitOFX`)
- `internal/service/backup/backup.go` (`Fitid` in `TransactionDTO` + export/restore mapping) + `backup_test.go` (`TestRestorePreservesFitid`, `pgtype` import)
- `internal/http/router.go` (widen `Imports` iface; `format` branch in `importPreview`/`importCommit`; `importFormat` helper; `Warning`/`format` threaded through `renderImport`→`ImportPage`) + `router_test.go` (stub `PreviewOFX`/`CommitOFX`, `TestImportOFXFormat`)
- `web/shell.go` (`ImportRow.Warning`), `web/pages.templ` (`ImportPage` format selector + FITID warning) → regenerated `web/pages_templ.go`
- `README.md` (OFX import section)

## Change Log

| Date | Change |
| --- | --- |
| 2026-06-30 | Story 7.1 drafted (create-story): OFX import (STMTTRN→Income/Expense, TRNAMT sign→type, DTPOSTED→date, NAME/MEMO→description), **FITID-only** per-account idempotent dedup (`transaction.fitid` + partial-unique index on `COALESCE(from,to), fitid`), no-FITID rows import with warning, hand-rolled tolerant OFX 1.x/2.x parser, format selector on the existing import page, backup round-trip carries fitid. Reuses the 3.6 Preview→Commit spine; tab path untouched. Status → ready-for-dev. |
| 2026-06-30 | Story 7.1 implemented (dev-story, TDD): `00012` fitid column + per-account partial-unique index; hand-rolled OFX 1.x/2.x parser; `PreviewOFX`/`CommitOFX` (FITID-only dedup, no-FITID warning, one tx); backup export/restore carry fitid; import page format selector + warning. All 4 ACs verified (pure unit + live DB + live HTTP smoke). build/vet/gofmt/nofloat green; tab importer untouched. Status → review. |
| 2026-06-30 | Independent code review (Opus, separate lane): APPROVE WITH NITS. Applied the MEDIUM fix — `ParseOFX` now delimits each `<STMTTRN>` block at the earliest of its close tag, the next `<STMTTRN>`, or `</BANKTRANLIST>`, so lenient SGML that omits `</STMTTRN>` no longer swallows following transactions. Added 3 parser tests (unclosed aggregates, zero TRNAMT, MEMO-fallback description). Full suite + nofloat green. |
