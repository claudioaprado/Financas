---
baseline_commit: a2d68ce
---

# Story 3.3: Transfers between accounts

Status: review

<!-- Note: Validation is optional. Run validate-create-story for quality check before dev-story. -->

## Story

As the owner,
I want to transfer money between my own accounts, including to pay my credit card and across currencies,
so that balances stay correct without double-counting.

## Acceptance Criteria

From `epics.md` → Epic 3 → Story 3.3 (realizes part of FR-6). **Given** two of my Accounts exist, **When** I record a Transfer, **Then**:

1. It is stored as **one** `Transaction` row carrying `from_account_id`, `to_account_id`, `from_amount`, `to_amount` (AD-9).
2. Balance derivation **debits the source and credits the destination** from that single row (a credit destination reduces its balance owed) — via the existing `domain.AccountBalance` (`Σ to − Σ from`), with no special case.
3. A **same-currency** transfer has `from_amount == to_amount`; a **cross-currency** transfer records **both legs** (`from_amount` in the source currency, `to_amount` in the destination currency; the transfer-time rate is `to_amount / from_amount` and is **not stored**, AD-9).
4. **No amount is double-counted** in totals (one row, FR-6).

> Scope: this story adds the **transfer use-case + UI** and makes the account register **account-relative** (a transfer shows as a debit on the source and a credit on the destination). The ledger already has the `from`/`to` columns (Story 3.1, AD-9) and `domain.AccountBalance` already handles both sides — the only schema change is widening the `type` CHECK to include `transfer`. Transfers are corrected by delete + re-create (the income/expense inline edit does not apply to them). Categories are Story 3.4; the cross-account register/filtering is Story 3.5; investment trades are Epic 4.

## Tasks / Subtasks

- [x] **Task 1 — Widen the `type` CHECK to include `transfer` (AC: #1)**
  - [x] Add goose migration `db/migrations/00006_transfer_type.sql`. Up: drop the existing `type` check and re-add it with the third value:
    ```sql
    ALTER TABLE transaction DROP CONSTRAINT IF EXISTS transaction_type_check;
    ALTER TABLE transaction ADD CONSTRAINT transaction_type_check
        CHECK (type IN ('income', 'expense', 'transfer'));
    ```
    Down: drop and re-add the two-value check (`income`, `expense`). Use `-- +goose StatementBegin/End` per the existing migration style. **Verify the original constraint name** against the running DB first (`\d transaction` — Postgres names an inline column CHECK `transaction_type_check`); use the exact name. No sqlc change (no new/changed query columns).

- [x] **Task 2 — `service/transaction.Transfer` + account-relative display (AC: #1, #2, #3, #4)**
  - [x] Add a `Transfer` type constant (`TxType` `"transfer"`).
  - [x] Add `Transfer(ctx, fromID, toID int64, fromAmount, toAmount decimal.Decimal, date time.Time, description string) error` — writes **one** row in one tx (AD-3):
    - Load both accounts via `store.GetAccount` (each missing ⇒ `ErrAccountNotFound`); `fromID != toID` (else `ErrSameAccount`); `fromAmount.IsPositive()` (else `ErrNonPositiveAmount`).
    - **Same currency** (`from.Currency == to.Currency`): the destination leg equals the source leg. If `toAmount` is provided (`> 0`) and `!= fromAmount` ⇒ `ErrSameCurrencyAmountMismatch`; otherwise set `to_amount = from_amount` (AC #3).
    - **Cross currency**: `toAmount.IsPositive()` required (else `ErrCrossCurrencyToAmountRequired`); store `from_amount`/`to_amount` as given (no rate stored).
    - Insert `type='transfer'`, `from_account_id=fromID`, `to_account_id=toID`, both amounts. Transfers into a credit destination reduce its owed (the `to` side credits it) — automatic via `AccountBalance`.
  - [x] **Make the register account-relative.** Extend the display `Transaction` struct with `Incoming bool` (true when the row credits the viewed account) and `Counterparty string` (the other account's name, for transfers). Refactor `toTransaction(accountID, row, names)`:
    - if `to_account_id == accountID` ⇒ `Amount = to_amount`, `Incoming = true`, counterpart = `from_account` name;
    - else (`from_account_id == accountID`) ⇒ `Amount = from_amount`, `Incoming = false`, counterpart = `to_account` name.
    - `Counterparty` is set only for `transfer` rows (income/expense have one side).
  - [x] In `List`, build an id→name map from `store.ListAllAccounts` (includes archived, so every counterpart resolves) and pass it to `toTransaction`. `Balance` is unchanged (it reads raw `from`/`to`, already transfer-correct). `Record`/`Edit` stay income/expense-only (their `legs()`/`validate` reject `transfer` via `ErrInvalidType`/the type guard — keep the income/expense edit path off transfers).
  - [x] New typed errors: `ErrSameAccount`, `ErrSameCurrencyAmountMismatch`, `ErrCrossCurrencyToAmountRequired`.
  - [x] DB-gated test additions in `transaction_test.go`: same-currency transfer cash→cash (source −X, dest +X, stored `from_amount==to_amount`, exactly one row); cross-currency transfer (USD→BRL) stores both legs (source −fromAmt USD, dest +toAmt BRL); transfer cash→**credit** reduces the credit's owed; validation (`ErrSameAccount`, missing account, non-positive `from`, same-currency mismatch, cross-currency missing `to`).

- [x] **Task 3 — Transfer UI on the account detail page (AC: #1, #3)**
  - [x] In `web/pages.templ` `AccountDetailPage`, add a **Transfer** form below the income/expense form: a "To account" `<select>` of the owner's **other** accounts (id + name + currency), an "Amount" (from, this account's currency), an optional "Amount received" (to, destination currency — used only when currencies differ; blank ⇒ same-currency), a date, and an optional description. Pass `transferTargets []TransferTarget{ID int64; Name, Currency string}`.
  - [x] Render transfers in the register account-relatively: signed amount (`+` incoming green / `−` outgoing red, reusing the existing styling), `type` shown as `transfer`, and the counterpart shown (e.g. description + " · to/from {Counterparty}"). **Hide the Edit link for transfer rows** (keep Delete); income/expense rows keep Edit + Delete. Add `Counterparty string` and `Editable bool` to `web.TxRow`.
  - [x] Routes in `router.go`: `POST /accounts/{id}/transfer` — parse `to_account_id`, `from_amount` (decimal string), `to_amount` (optional decimal string → `decimal.Zero` when blank), `date`, `description`; call `deps.Transactions.Transfer(...)`; 303 to `/accounts/{id}` on success, re-render with a message + 400 on error. Add `Transfer` to the `http.Transactions` interface. The detail handler also loads the account list (via `deps.Accounts.List(false)`) and passes the **other** accounts as transfer targets.
  - [x] `make generate css` after templ edits; commit `*_templ.go` + rebuilt `app.css`.

- [x] **Task 4 — Tests, verify, docs (AC: all)**
  - [x] Update `internal/http/router_test.go`: extend `stubTransactions` with `Transfer` (in-memory: append a `Transfer`-typed row touching both accounts so `Balance`/`List` reflect it account-relatively); add a test — create two cash accounts, transfer from one to the other, assert the source balance drops and the destination rises by the same amount (no double-count), and the transfer row appears in both registers with the right sign + counterpart; Edit link absent on the transfer row.
  - [x] `go build`/`go vet`/`go test ./...` + `make nofloat` clean (DB-gated tests skip without a DB; `nofloat` stays green — decimal only).
  - [x] Live smoke (compose db + run, logged in): create a USD cash account, a second USD cash account, and a USD credit account. Same-currency transfer 200 cash→cash ⇒ source −200, dest +200; transfer 150 cash→credit ⇒ credit "Balance owed" drops by 150; create a BRL cash account and do a cross-currency transfer (e.g. 100 USD → 520 BRL) ⇒ source −100 USD, dest +520 BRL; confirm one ledger row per transfer and persistence across reload.
  - [x] Update `README.md` briefly (transfers: one row with `from`/`to` (AD-9); same-currency `from==to`, cross-currency both legs, rate not stored; debits source / credits destination, a credit destination reduces owed; no double-counting; corrected via delete + re-create).

## Dev Notes

### Scope boundary — what this story does NOT do (read first)

- **NO new derivation** — `domain.AccountBalance` already debits `from` and credits `to`; transfers need no new domain function. No Net Worth / aggregation here.
- **NO transfer editing via the income/expense form** — transfers are corrected by delete + re-create. The inline Edit is income/expense-only; it is hidden on transfer rows.
- **NO stored transfer rate** — `to_amount / from_amount` is implicit (AD-9). No FX conversion is applied; the owner enters both legs for cross-currency.
- **NO categories** (3.4), **NO cross-account register/filter** (3.5), **NO investment trades** (Epic 4). Transfers may target any of the owner's accounts (cash/credit/investment cash side), consistent with AD-9.

### Architecture invariants this story must honor

- **AD-9 — one canonical Transfer storage shape.** Exactly one `Transaction` row with `from_account_id`/`to_account_id`/`from_amount`/`to_amount`; same-currency ⇒ `from_amount == to_amount`; the transfer-time rate is not stored; a Transfer into a credit account reduces balance owed. [Source: ARCHITECTURE-SPINE.md#AD-9]
- **AD-2 / AD-10 — derived on read, one canonical balance.** Both accounts' balances re-derive from the single row via `AccountBalance`; no double counting; `http` does no math. [Source: ARCHITECTURE-SPINE.md#AD-2, #AD-10]
- **AD-3 — one DB transaction per use-case.** `Transfer` writes its one row in one tx. [Source: ARCHITECTURE-SPINE.md#AD-3]
- **AD-4 / AD-5 — decimal magnitudes, native currency.** Both legs are non-negative `NUMERIC(19,4)` decimals in their account's own currency; no conversion. [Source: ARCHITECTURE-SPINE.md#AD-4, #AD-5]
- **AD-1 — layering.** `service/transaction` reads accounts via `store`; `http` defines the `Transactions` interface; `domain` unchanged. [Source: ARCHITECTURE-SPINE.md#AD-1]

### Previous-story intelligence (3.1 + 3.2 + 2.3) — load-bearing

[Source: 3-1-record-cash-income-expenses.md; 3-2-credit-card-expenses-balance-owed.md; 2-3-create-manage-accounts.md; [[financas-epic1-progress]]]

- **The ledger already has the AD-9 from/to shape** (Story 3.1): `transaction(type, from_account_id, to_account_id pgtype.Int8, from_amount, to_amount decimal, occurred_on, description, created_at)`. A transfer simply populates **both** sides. The `00005` `type` CHECK only allows `income`/`expense` — **this story widens it** (forward-only goose). `store.CreateTransaction` already takes all the params (set both `pgtype.Int8{...,Valid:true}` and both amounts).
- **`domain.AccountBalance(accountID, currency, []BalanceTxn)`** = `Σ to[to=A] − Σ from[from=A]` — already correct for transfers (its `balance_test.go` even includes a both-sides leg). `service.Balance` builds `BalanceTxn` from `ListAccountTransactions` — unchanged.
- **`legs(accountID, typ, amount)`** in `service/transaction` maps income→to-only / expense→from-only; **do not** route transfers through it (transfers set both sides explicitly in `Transfer`). `validate` (cash+credit guard, type IsValid) is for income/expense; `Transfer` does its own validation (no cash/credit restriction — any distinct accounts).
- **Display mapping** `toTransaction(accountID, row)` currently assumes one side; refactor to perspective-aware (`Incoming`, `Counterparty`) as in Task 2. The http handler's `isIncome := t.Type == transaction.Income` becomes `t.Incoming`; `web.TxRow` gains `Counterparty`/`Editable`. The existing income/expense edit-prefill (`edit TxRow`) and cash/credit detail tests must still pass.
- **Account-detail page** `web.AccountDetailPage(...)` + `renderAccountDetail` in `router.go` (active nav `accounts`); it already shows the balance (Story 3.2 made it type-aware: "Balance owed" for credit) and the income/expense form + register. Add the transfer form + targets. `deps.Accounts.List(false)` gives active accounts; exclude the current `acctID` for the dropdown. `store.ListAllAccounts` exists for the id→name map in the service.
- **`money.Money`/`decimal`**: `decimal.NewFromString` (parse, never float), `IsPositive`, `IsZero`, `Equal`; `money.New`, `Money.String`. **Build `GOTOOLCHAIN=local`**; `make nofloat` guards `internal/{money,domain,service,store}`. Local DB host **5433**; DB-gated tests skip without `DATABASE_URL`/`TEST_DATABASE_URL`. Dev login `owner`/`financas`. `baseline_commit` real SHA `a2d68ce`. Commit + push to `main` when done (one commit per story).
- **chi routing:** static `/accounts/rename`,`/accounts/archive` keep precedence over `/accounts/{id}`; `POST /accounts/{id}/transfer` sits beside the existing `/transaction`(`/edit`/`/delete`) routes. `Delete` (existing) works for transfer rows by id.

### Project Structure Notes

New: `db/migrations/00006_transfer_type.sql`. Modified: `internal/service/transaction/transaction.go` (`Transfer`, `Transfer` type, perspective-aware `toTransaction` + name map in `List`, new errors) + `transaction_test.go`; `internal/http/router.go` (`Transfer` route + handler, `Transactions.Transfer`, transfer targets in `renderAccountDetail`, `Incoming`/`Counterparty`/`Editable` mapping) + `router_test.go` (stub `Transfer`, transfer test); `web/pages.templ` (transfer form + transfer-aware register) + `web/shell.go` (`TxRow.Counterparty`/`.Editable`, a `TransferTarget` struct) + regenerated `web/pages_templ.go` + rebuilt `web/static/css/app.css`; `README.md`. No sqlc regen (no query/column changes — only a CHECK constraint).

### Testing standards

- `service/transaction`: DB-gated — same-currency (`from==to`, one row, balances move), cross-currency (both legs), cash→credit (owed reduced), and all transfer validations.
- `http`: stub-backed — transfer moves both balances without double-count; transfer row renders account-relatively in both registers; Edit hidden on transfer rows.
- `go test ./...` green with no DB; `go vet` + `make nofloat` clean.

### References

- [Source: epics.md#Story 3.3] — acceptance criteria; [Source: epics.md FR-6] — transfers, cross-currency two legs, no double-count
- [Source: ARCHITECTURE-SPINE.md#AD-9] — one-row from/to transfer shape, same-currency equality, rate not stored, credit destination reduces owed
- [Source: ARCHITECTURE-SPINE.md#AD-2 / #AD-10 / #AD-3 / #AD-4 / #AD-5 / #AD-1] — derived on read; one balance fn; one tx; decimal magnitudes; native currency; layering
- [Source: 3-1-record-cash-income-expenses.md] — ledger shape, `legs()`, `toTransaction`, `domain.AccountBalance`, account-detail page; [Source: 3-2-credit-card-expenses-balance-owed.md] — type-aware balance, credit handling

## Dev Agent Record

### Agent Model Used

claude-opus-4-8[1m] (Opus 4.8, 1M context)

### Debug Log References

- Confirmed the existing CHECK constraint name (`transaction_type_check`) via `pg_constraint` before writing `00006`; `make migrate` applied it (goose v6) and `pg_get_constraintdef` shows the widened `(income, expense, transfer)`.
- No sqlc regen (only a CHECK constraint changed — no query/column changes). `gofmt -w` applied to the service file (error-var alignment).
- `go build`/`go vet`/`make nofloat` clean. Full suite green with and without a DB (DB-gated tests skip cleanly).
- Live DB: `transaction.TestTransfer` PASS — same-currency cash→cash (source −200, dest +200, one row, `from==to`, incoming row names the counterpart), cash→credit (credits the credit account), cross-currency USD→BRL (both legs; checking nets −450), and all validations (`ErrSameAccount`, missing account, non-positive `from`, same-currency mismatch, cross-currency missing `to`).
- Live HTTP smoke (server :8098 + db :5433, owner/financas): same-currency 200 cash→cash ⇒ −200 / +200; pay-card 150 cash→credit ⇒ the credit account credited (owed reduced 0 → −150); cross-currency 100 USD → 520 BRL ⇒ +520 BRL / checking −450 USD. DB shows **one** `transfer` row per move with both sides (`200/200`, `150/150`, `100/520`). The savings register renders "transfer · from TXChk · +200.0000 USD".

### Completion Notes List

All four acceptance criteria verified (DB-gated unit + live DB + live HTTP):
- **AC1 — one row, AD-9 shape:** `service/transaction.Transfer` writes a single `type='transfer'` row with both `from_account_id`/`to_account_id` and `from_amount`/`to_amount` (one tx, AD-3). Ledger confirmed one row per transfer.
- **AC2 — debit source / credit destination:** unchanged `domain.AccountBalance` (`Σ to − Σ from`) handles both sides; a transfer into a credit account credits it (reduces owed). No new derivation.
- **AC3 — same vs cross currency:** same-currency stores `from_amount == to_amount` (the destination leg defaults to the source amount; a provided mismatch ⇒ `ErrSameCurrencyAmountMismatch`); cross-currency requires and stores both legs (`ErrCrossCurrencyToAmountRequired` if the received amount is missing); the transfer-time rate is **not** stored.
- **AC4 — no double-count:** one row per transfer; the source's net (−450 = −(200+150+100)) confirms no double counting.

Decisions / variances (intentional):
- **Only schema change is widening the `type` CHECK** (`00006`, `ALTER ... DROP/ADD CONSTRAINT transaction_type_check`) to add `transfer` — the AD-9 from/to columns already existed (Story 3.1). No sqlc change.
- **`Transfer` has its own validation** (any two distinct accounts; no cash/credit restriction — transfers may fund an investment account's cash per AD-9) and does **not** route through the income/expense `legs()`/`validate`. `IsValid()` still returns true only for income/expense, so `Record`/`Edit` reject `transfer`.
- **Account-relative register:** `toTransaction` is now perspective-aware (`Incoming`, `Counterparty`); `List` builds an id→name map via `store.ListAllAccounts` so transfer counterparts resolve. `web.TxRow` gained `Counterparty`/`Editable` and `IsIncome`→`Incoming`; a transfer row shows the counterpart ("· to/from {name}"), a signed amount (green in / red out), and **no Edit link** (transfers are corrected via delete + re-create; the existing `Delete` works by id).
- **Transfer UI** is on the account-detail page (a "Transfer to another account" form listing the owner's other active accounts; optional "Received" amount for cross-currency). `http.Transactions` gained `Transfer`; the detail handler builds the transfer targets via `deps.Accounts.List(false)`.

Reviewer notes: no `sprint-status.yaml` → status tracked in this file only. `baseline_commit` is the real SHA `a2d68ce` (HEAD before this story). Committed + pushed to `main` per the owner's standing instruction.

### File List

New:
- `db/migrations/00006_transfer_type.sql`

Modified:
- `internal/service/transaction/transaction.go` (`Transfer` use-case + `Transfer` type + transfer errors; perspective-aware `toTransaction`/`List` with `accountNames` map) + `transaction_test.go` (`TestTransfer`)
- `internal/http/router.go` (`Transactions.Transfer`, `POST /accounts/{id}/transfer` route + `txTransfer` handler, `renderAccountDetail` builds transfer targets + `Incoming`/`Counterparty`/`Editable`; import `strings`) + `router_test.go` (stub `Transfer` two-row model, `TestTransferMovesBothBalances`)
- `web/pages.templ` (`AccountDetailPage` transfer form + transfer-aware register) + regenerated `web/pages_templ.go`, `web/shell.go` (`TxRow.Counterparty`/`.Editable`/`.Incoming`, `TransferTarget`), rebuilt `web/static/css/app.css`
- `README.md` (transfers section)

## Change Log

| Date | Change |
| --- | --- |
| 2026-06-29 | Story 3.3 drafted (create-story): transfer use-case (AD-9 one-row from/to), widened `type` CHECK, account-relative register, transfer UI. Status → ready-for-dev. |
| 2026-06-29 | Story 3.3 implemented (dev-story): `00006` widens the `type` CHECK; `service/transaction.Transfer` (one row, same/cross-currency, AD-9); account-relative register (`Incoming`/`Counterparty`); transfer form on the account-detail page. All 4 ACs verified (live DB + live HTTP). build/vet/test + nofloat green. Status → review. |
