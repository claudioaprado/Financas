---
baseline_commit: 4919704
---

# Story 3.5: Browse & filter the register

Status: review

<!-- Note: Validation is optional. Run validate-create-story for quality check before dev-story. -->

## Story

As the owner,
I want to browse and filter my transactions,
so that I can review my money movements quickly.

## Acceptance Criteria

From `epics.md` → Epic 3 → Story 3.5 (realizes FR-8). **Given** transactions exist, **When** I open the register for an Account or across all Accounts, **Then**:

1. Transactions list **newest-first** (FR-8) at `/transactions` (across all accounts) and optionally scoped to one account.
2. I can **filter by type and Category** (and by account), and results **update without a full page reload** (HTMX). *(Security filtering is in the FR but Securities don't exist until Epic 4 — see scope note; the filter is added there.)*
3. Each row shows **date, description, a signed colored amount** (green income / red expense; transfers neutral), and a **type/Category badge** (UX-DR5).

> Scope: this story builds the cross-account register at `/transactions` (replacing the Story 1.4 ComingSoon placeholder — `Transactions` is a primary-nav target), and **wires HTMX** for the first time (vendored `htmx.min.js` + a script tag in the shell; the scaffold reserved the slot but never added the library). Filtering is by **account, type, and Category** — **Security filtering is deferred to Epic 4** (no Securities/Holdings exist yet). No new transaction data is created here; it is a read/render story over the existing ledger.

## Tasks / Subtasks

- [x] **Task 1 — Wire HTMX into the shell (AC: #2)**
  - [x] Vendor HTMX 2.x: download `htmx.min.js` into `web/static/js/htmx.min.js` (committed, like the CSS — the Dockerfile only compiles; assets are embedded via `web/embed.go`). Pin a specific 2.x version and note it.
  - [x] Add `<script src="/static/js/htmx.min.js"></script>` to the shell (`web/shell.templ`, before `</body>`). Confirm `web.StaticFS` already serves `/static/*` (it does — Story 1.x). `make generate` to rebuild `shell_templ.go`.

- [x] **Task 2 — `store.ListTransactions` (AC: #1)**
  - [x] Add to `db/query/transaction.sql`: `ListTransactions :many` — `SELECT id, type, from_account_id, to_account_id, from_amount, to_amount, occurred_on, description, created_at, category_id FROM transaction ORDER BY occurred_on DESC, id DESC` (table column order, so it reuses `store.Transaction`). `make sqlc`; commit generated files.

- [x] **Task 3 — `service/transaction.Register` (AC: #1, #2, #3)**
  - [x] Add a `RegisterFilter{AccountID int64; Type TxType; CategoryID int64}` (0 = "all"; an empty `Type` = "all") and a `RegisterRow` display struct: `ID int64`, `Date time.Time`, `Type TxType`, `Description string`, `Category string`, `Account string` (the account name for income/expense; `"From → To"` for transfers), `Amount money.Money` (primary leg), `ToAmount money.Money` (the destination leg for cross-currency transfers; zero otherwise), `Incoming bool`, `IsTransfer bool`, `CrossCurrency bool`.
  - [x] `Register(ctx, f RegisterFilter) ([]RegisterRow, error)`: load `ListTransactions`, build id→name and id→currency maps (`ListAllAccounts`) and a category id→name map (`ListCategories`); for each row apply the filters (skip when `f.AccountID != 0` and the account is on neither side; when `f.Type != ""` and the type differs; when `f.CategoryID != 0` and the row's category differs) and build the `RegisterRow`:
    - **income** ⇒ `Account = name[to]`, `Amount = Money(to_amount, cur[to])`, `Incoming = true`;
    - **expense** ⇒ `Account = name[from]`, `Amount = Money(from_amount, cur[from])`, `Incoming = false`;
    - **transfer** ⇒ `Account = name[from] + " → " + name[to]`, `Amount = Money(from_amount, cur[from])`, `IsTransfer = true`; if `cur[from] != cur[to]` ⇒ `CrossCurrency = true`, `ToAmount = Money(to_amount, cur[to])`.
  - [x] Newest-first is the SQL order; the service preserves it. DB-gated test: across two accounts with income/expense/transfer rows, `Register` returns newest-first; filter by type, by category, and by account each narrow correctly; a transfer row carries both account names and (for cross-currency) both legs.

- [x] **Task 4 — `/transactions` register page + HTMX partial (AC: #1, #2, #3)**
  - [x] Replace the `/transactions` ComingSoon route with `GET /transactions` (active nav `transactions`). Read query params `account`, `type`, `category` (each optional; blank/0 = all) into a `RegisterFilter`, call `deps.Transactions.Register`, and render. **HTMX partial:** when `req.Header.Get("HX-Request") == "true"`, render **only** the rows component; otherwise render the full page (filter form + a `<tbody id="tx-rows">` wrapping the rows). Add `Register` to the `http.Transactions` interface.
  - [x] templ: `TransactionsPage(data, accounts []FilterOption, categories []CategoryOption, sel RegisterSelection, rows []RegisterRow)` and a `TransactionRows(rows []RegisterRow)` component the page embeds inside `#tx-rows`. The filter form is `method="get" action="/transactions"` (graceful no-JS reload) **and** HTMX-enhanced: `hx-get="/transactions"`, `hx-target="#tx-rows"`, `hx-swap="innerHTML"`, `hx-trigger="change"` (so changing any select updates the list without a full reload). The selects (account / type / category) keep their current selection (they live outside `#tx-rows`). Each row: date, type badge, description + Category badge, the **Account** label, and the **signed colored amount** — `+`/green for incoming, `−`/red for expense, **neutral** for transfers (show `from → to` legs). Build the signed string + color in `http`/templ (presentation; the magnitudes come from `domain`-free stored amounts).
  - [x] Add `web` view structs (`RegisterRow`, `FilterOption`, reuse `CategoryOption`) in `web/shell.go`. `make generate css`; commit `*_templ.go` + `app.css`.

- [x] **Task 5 — Tests, verify, docs (AC: all)**
  - [x] Update `internal/http/router_test.go`: extend the stub `Transactions` with `Register`; replace the `/transactions` ComingSoon nav assertions (Story 1.4's `TestNavTargetRequiresAuth` includes `/transactions` — still auth-gated, fine; any test asserting `/transactions` is "Coming soon" must change). Add tests: unauth `GET /transactions` → 302; authed full page renders the filter form + rows; an **HTMX request** (`HX-Request: true`) returns only the rows partial (no `<html>`/shell); a type filter narrows the rows.
  - [x] `go build`/`go vet`/`go test ./...` + `make nofloat` clean (DB-gated tests skip without a DB; `nofloat` green — no float).
  - [x] Live smoke (compose db + run, logged in): with a couple of accounts and a mix of income/expense/transfer (some categorized), open `/transactions` → newest-first across accounts; change the type filter → list updates without a full reload (verify the response to the HXR is just rows); filter by category and by account; confirm signed colors + badges render.
  - [x] Update `README.md` briefly (the register at `/transactions`: cross-account, newest-first, filter by account/type/category with HTMX live updates; Security filter arrives with Epic 4).

## Dev Notes

### Scope boundary — what this story does NOT do (read first)

- **NO Security filter** — Securities/Holdings are Epic 4; this story filters by account/type/Category. Add the Security filter when securities exist (the filter form + `RegisterFilter` extend cleanly).
- **NO new writes** — read/render only over the existing ledger. Editing/deleting stays on the account-detail page (Story 3.1/3.3); rows here can link to their account detail (optional).
- **NO cross-currency totals** — the register lists native amounts per row; it does not sum across currencies (Display-Currency aggregation is Epic 5). Each row shows its own currency.
- **NO dashboard** — the dashboard register widget is Story 5.5; this is the full register.

### Architecture invariants this story must honor

- **AD-2 — derived/read on demand.** The register reads ledger rows; it stores nothing. [Source: ARCHITECTURE-SPINE.md#AD-2]
- **AD-9 — transfer is one row.** A transfer appears **once** in the cross-account register (showing `from → to`), not double-counted as two rows. [Source: ARCHITECTURE-SPINE.md#AD-9]
- **AD-10 / AD-4 — http does no math; decimal display.** The signed `+/−` and color are presentation keyed off the row's type/direction; amounts render via `money.Money.String()` (never a float). No aggregation here. [Source: ARCHITECTURE-SPINE.md#AD-10, #AD-4]
- **AD-1 — layering.** `service/transaction.Register` resolves names via `store` (accounts, categories); `http` defines the consumer interface and renders; the HTMX partial is a render concern. [Source: ARCHITECTURE-SPINE.md#AD-1]
- **UX-DR5 — register rows:** per-row icon/colored signed amount + type/Category badge, links into detail. [Source: epics.md#UX-DR5]

### Previous-story intelligence (3.1–3.4) — load-bearing

[Source: 3-4-categories-classification.md; 3-3-transfers-between-accounts.md; 3-1-record-cash-income-expenses.md; [[financas-epic1-progress]]]

- **`/transactions` is currently a `web.ComingSoon` placeholder** (Story 1.4), registered in `router.go` via `renderPage(deps, "transactions", ...)`. Replace that single registration with the real handler; keep the five-item nav (UX-DR1) — `Transactions` is nav item #3.
- **HTMX was reserved but never wired** — `web/static/js/` holds only `.gitkeep`; the shell loads only `/static/css/app.css`. This story vendors `htmx.min.js` and adds the script tag. `web.StaticFS` already serves `/static/*` (so the file is reachable once committed).
- **The ledger + maps pattern exists:** `store.ListAllAccounts` (id→name+currency), `store.ListCategories` (id→name); `service/transaction` already builds these maps (`accountNames`, `categoryNames`) and the account-relative `toTransaction`. Reuse the same approach for `Register`. `nullID(pgtype.Int8)→int64` (0 when NULL) and `idParam` helpers exist.
- **`http.Transactions`** interface currently has `Record/Edit/Delete/Transfer/Balance/List/CategoryTransactions`; **add `Register`**. The handler also needs the accounts list (`deps.Accounts.List(false)`) and categories (`deps.Categories.List`) for the filter dropdowns.
- **templ partial pattern (new here):** define `TransactionRows(rows)` and render it both inside `TransactionsPage` (within `<tbody id="tx-rows">`) and standalone for the HXR. Detect HTMX via the `HX-Request` header. The filter `<form method="get">` degrades without JS; `hx-*` attributes enhance it. Money renders via `money.Money.String()`; semantic colors `text-gain`/`text-loss` exist (Story 2.x/3.x).
- **Build `GOTOOLCHAIN=local`**; `make nofloat` guards `internal/{money,domain,service,store}`. Local DB host **5433**; DB-gated tests skip without `DATABASE_URL`/`TEST_DATABASE_URL`. Dev login `owner`/`financas`. `baseline_commit` real SHA `4919704`. Commit + push to `main` when done.

### Project Structure Notes

New: `web/static/js/htmx.min.js` (vendored). Modified: `db/query/transaction.sql` (+`ListTransactions`) + regenerated `internal/store/transaction.sql.go`/`querier.go`; `internal/service/transaction/transaction.go` (`Register`, `RegisterFilter`, `RegisterRow`) + `transaction_test.go`; `internal/http/router.go` (`/transactions` handler + HTMX partial, `Transactions.Register`) + `router_test.go`; `web/shell.templ` (htmx script tag), `web/pages.templ` (`TransactionsPage` + `TransactionRows`) + regenerated `*_templ.go`, `web/shell.go` (`RegisterRow`/`FilterOption` view structs), `web/static/css/app.css`; `README.md`. No migration.

### Testing standards

- `service/transaction`: DB-gated — newest-first ordering; filter by account/type/category; transfer row carries both account names + (cross-currency) both legs.
- `http`: stub-backed — full page vs HTMX-partial (`HX-Request`) rendering; filter narrows rows; auth gate.
- `go test ./...` green with no DB; `go vet` + `make nofloat` clean.

### References

- [Source: epics.md#Story 3.5] — acceptance criteria; [Source: epics.md FR-8 / UX-DR5] — register, filters, row anatomy
- [Source: ARCHITECTURE-SPINE.md#AD-2 / #AD-9 / #AD-10 / #AD-1] — read-on-demand; transfer one row; http no math; layering
- [Source: 3-3 / 3-4] — `toTransaction` account-relative mapping, account/category name maps, the `Transactions` interface + stubs

## Dev Agent Record

### Agent Model Used

claude-opus-4-8[1m] (Opus 4.8, 1M context)

### Debug Log References

- **HTMX was never actually wired** (scaffold left only `web/static/js/.gitkeep`, shell loaded only the CSS). Vendored `htmx.org@2.0.4` `htmx.min.js` (~51 KB) into `web/static/js/`, added `<script src="/static/js/htmx.min.js">` to `web/shell.templ` before `</body>`; `web.StaticFS` already serves it. Confirmed the script tag is in the regenerated `shell_templ.go`.
- `make sqlc` added `ListTransactions(ctx) ([]store.Transaction, ...)` (table column order → reuses `store.Transaction`, no bespoke row type). No migration.
- `go build`/`go vet`/`make nofloat` clean. Full suite green with and without a DB. `gofmt -w` applied.
- Live DB: `transaction.TestRegister` PASS — newest-first; filter by type / category / account; the transfer row names both accounts and (cross-currency) carries both legs (25 USD / 130 BRL).
- Live HTTP smoke (server :8100 + db :5433, owner/financas): full `/transactions` page has the htmx script + filter form + all rows (income/expense/transfer, the transfer shown as `→ RegBrl` with the category badge); an **`HX-Request: true` GET returns a bare rows partial** (0 doctype/`Welcome back` markers); `?type=expense`, `?category=…`, and `?account=…` each narrow correctly; the transfer appears **once** and is reachable from the destination account; the cross-currency transfer renders `100.0000 USD → 520.0000 BRL`.

### Completion Notes List

All three acceptance criteria verified (DB-gated unit + live DB + live HTTP):
- **AC1 — newest-first, cross-account or scoped:** `store.ListTransactions` (ORDER BY `occurred_on` DESC, `id` DESC) feeds `service/transaction.Register`; an `account` filter scopes to one account (matching either side).
- **AC2 — filter + no full reload:** `Register` filters by account/type/category; the `/transactions` handler renders the full page normally and returns **only** `TransactionRows` when `HX-Request: true`. The filter form is a plain GET form (works without JS) enhanced with `hx-get`/`hx-target="#tx-rows"`/`hx-trigger="change"`.
- **AC3 — UX-DR5 rows:** date, type badge, account label, description + Category badge, and a signed colored amount (`+`/green income, `−`/red expense, **neutral** transfer shown as `from → to`, cross-currency showing both legs).

Decisions / variances (intentional):
- **Security filter deferred to Epic 4** — Securities/Holdings don't exist yet; `RegisterFilter` + the filter form extend cleanly when they do.
- **First HTMX integration** — vendored, committed `htmx.min.js` (no CDN; consistent with the committed-assets policy). The partial/full split keys off the `HX-Request` header; one `TransactionRows` component is shared by the page and the partial.
- **A transfer appears once** in the cross-account register (AD-9), shown as `From → To` with a neutral colour (it's a move, not a gain/loss); income/expense keep the green/red signed colour. Amounts are per-row native currency — no cross-currency sum (Epic 5).
- **`http` composes the amount string** (`registerAmount`: sign + `money.String()`, or the transfer legs) — presentation, no math; `service.Register` returns typed `money.Money` + flags.

Reviewer notes: no `sprint-status.yaml` → status tracked in this file only. `baseline_commit` is the real SHA `4919704` (HEAD before this story). Committed + pushed to `main` per the owner's standing instruction.

### File List

New:
- `web/static/js/htmx.min.js` (vendored htmx 2.0.4)

Modified:
- `db/query/transaction.sql` (+`ListTransactions`) + regenerated `internal/store/transaction.sql.go`/`querier.go`
- `internal/service/transaction/transaction.go` (`Register`, `RegisterFilter`, `RegisterRow`) + `transaction_test.go` (`TestRegister`)
- `internal/http/router.go` (`Transactions.Register`, `/transactions` register handler + HTMX partial, `registerAmount`) + `router_test.go` (stub `Register`, `TestTransactionsRegister`)
- `web/shell.templ` (htmx script tag) + regenerated `web/shell_templ.go`; `web/pages.templ` (`TransactionsPage` + `TransactionRows`) + regenerated `web/pages_templ.go`; `web/shell.go` (`FilterOption`, `RegisterRow`); rebuilt `web/static/css/app.css`
- `README.md` (register section)

## Change Log

| Date | Change |
| --- | --- |
| 2026-06-29 | Story 3.5 drafted (create-story): cross-account `/transactions` register, HTMX wiring + live filter (account/type/category), UX-DR5 rows; Security filter deferred to Epic 4. Status → ready-for-dev. |
| 2026-06-29 | Story 3.5 implemented (dev-story): vendored htmx + shell wiring; `store.ListTransactions`; `service/transaction.Register` (filter by account/type/category, newest-first); `/transactions` page + HTMX rows partial (UX-DR5). All 3 ACs verified (live DB + live HTTP). build/vet/test + nofloat green. Status → review. |
