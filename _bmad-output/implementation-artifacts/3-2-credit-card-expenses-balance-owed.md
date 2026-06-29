---
baseline_commit: 11a8fc1
---

# Story 3.2: Credit-card expenses & balance owed

Status: review

<!-- Note: Validation is optional. Run validate-create-story for quality check before dev-story. -->

## Story

As the owner,
I want to record expenses on a credit account,
so that I track what I owe.

## Acceptance Criteria

From `epics.md` → Epic 3 → Story 3.2 (realizes part of FR-6). **Given** a credit Account exists, **When** I record an Expense on it, **Then**:

1. The account's **balance owed increases by that amount** (FR-6). Mechanically: an expense debits the credit account (`from_account`), so its signed ledger balance (`domain.AccountBalance`) decreases by the amount; the **amount owed** is the magnitude of that (negative) balance.
2. The credit balance is **displayed as a liability** — the account-detail page shows "Balance owed: {positive amount}" for a credit account (not a raw negative balance).
3. It is **excluded from assets but reduces Net Worth** (per AD conventions). Net Worth is computed in Epic 4 (Story 4.4); this story keeps the signed-balance derivation that makes that automatic (a credit account's negative balance lowers the simple `Σ` of balances), and surfaces the liability now. No Net-Worth view is built here.

> Scope: this story **widens income/expense to credit accounts** (Story 3.1 restricted them to cash) and presents a credit account's balance as a positive **amount owed**. Income on a credit account (a refund) reduces owed; expense increases it — both via the same uniform AD-9 derivation. Investment accounts remain out of scope for plain income/expense (their cash flows are Epic 4). Transfers / credit-card payments are Story 3.3.

## Tasks / Subtasks

- [x] **Task 1 — Allow income/expense on credit accounts (AC: #1)**
  - [x] In `internal/service/transaction/transaction.go`, widen the account-type guard in `validate`: permit **cash and credit** accounts; reject **investment** (plain income/expense don't apply — Epic 4 handles investment cash flow). Rename the sentinel `ErrNotCashAccount` → `ErrUnsupportedAccountType` with message "income/expense require a cash or credit account" (update its doc comment).
  - [x] No schema or query change — the `transaction` ledger (AD-9 from/to shape) already supports a credit account on either side. An expense on credit ⇒ `from_account_id = credit account`, `from_amount = magnitude` (debits it, so the signed balance goes negative = owed). Income on credit ⇒ `to_account_id = credit account` (reduces owed). Direction stays a function of `type` (AD-4).

- [x] **Task 2 — `domain.AmountOwed` (liability presentation figure; AC: #1, #2)**
  - [x] Add to `internal/domain/balance.go`: `AmountOwed(balance money.Money) money.Money` returning `money.New(balance.Amount().Neg(), balance.Currency())` — the magnitude a liability account owes (positive when the signed balance is negative). Pure, no I/O; the single home for this figure (AD-10). It does **not** re-derive the ledger — it transforms the already-derived `AccountBalance`.
  - [x] Extend `internal/domain/balance_test.go`: `AmountOwed` of a −300 balance is +300 (same currency); of a +50 balance is −50; of zero is zero.

- [x] **Task 3 — Credit balance shown as a liability on the account detail page (AC: #2)**
  - [x] In `internal/http/router.go` `renderAccountDetail`: choose the balance **label** and **value** by account type — credit ⇒ label "Balance owed", value `domain.AmountOwed(bal).String()`; cash/investment ⇒ label "Balance", value `bal.String()`. (`http` may call `domain` to render a domain figure — `http → domain` is in the spine diagram and is not `http` doing math itself; AD-10.) Add the `internal/domain` import.
  - [x] Update `web/pages.templ` `AccountDetailPage` to take a `balanceLabel string` param and render it in place of the hardcoded "Balance" heading. `make generate css` after the templ edit; commit `*_templ.go` + rebuilt `app.css`.

- [x] **Task 4 — Tests, verify, docs (AC: #1, #2, #3)**
  - [x] Update `internal/service/transaction/transaction_test.go`: the prior "expense on credit ⇒ `ErrNotCashAccount`" case is now **valid** — change it to assert an expense on a credit account **succeeds and drives the signed balance negative** (owed positive), and add an **investment** account asserting `ErrUnsupportedAccountType`. Cover: credit expense 200 ⇒ `Balance` = −200; credit income 50 ⇒ −150 (owed 150); `domain.AmountOwed` of the −150 balance = 150.
  - [x] Update `internal/http/router_test.go`: extend the stub-backed flow (or add a focused test) — create a **credit** account, record an expense, and assert the detail page shows **"Balance owed"** with the positive amount (and not a negative balance). The existing cash flow test must still show "Balance".
  - [x] `go build`/`go vet`/`go test ./...` + `make nofloat` clean (DB-gated tests skip without a DB; `nofloat` stays green — `decimal.Neg`, no float).
  - [x] Live smoke (compose db + run, logged in): create a credit USD account; record an expense 500 ⇒ detail shows "Balance owed: 500.0000 USD"; record a 120 refund (income) ⇒ "Balance owed: 380.0000 USD"; confirm persistence across reload.
  - [x] Update `README.md` briefly (credit accounts: an expense increases the balance owed, shown as a positive liability; the same signed-balance derivation means a credit balance reduces Net Worth in Epic 4).

## Dev Notes

### Scope boundary — what this story does NOT do (read first)

- **NO Net Worth view** — Story 4.4. AC #3 ("excluded from assets but reduces Net Worth") is satisfied structurally: `domain.AccountBalance` stays the uniform signed figure, so a credit account's negative balance lowers the eventual `Σ`-of-balances Net Worth automatically. Nothing aggregates here.
- **NO transfers / credit-card payments** — Story 3.3 (a transfer from a cash account into a credit account reduces owed via the `to` side).
- **NO investment income/expense** — investment cash flow is Epic 4 (Buy/Sell/Dividend); plain income/expense reject investment accounts.
- **NO schema/query change** — the `transaction` table already models a credit account on either side; this is a service-guard + presentation story.
- **NO change to the accounts LIST balance** — it still shows the per-type label placeholder from Story 2.3 (`Balance owed: —`); the real owed amount is on the account-detail page. (Wiring list balances is deferred.)

### Architecture invariants this story must honor

- **AD-2 / AD-9 — derived on read, uniform one-row shape.** A credit expense is the same `from`-side debit as a cash expense; `domain.AccountBalance` (`Σ to − Σ from`) needs no special case. A credit account's balance is simply negative when money is owed. [Source: ARCHITECTURE-SPINE.md#AD-2, #AD-9]
- **AD-9 conventions — "A Transfer into a credit Account reduces balance owed."** Consistent here: crediting a credit account (income/refund now, transfer in 3.3) moves its balance toward zero. [Source: ARCHITECTURE-SPINE.md#AD-9]
- **AD-10 — one canonical home per derived figure; http does no math.** `AmountOwed` lives in `domain`; `http` calls it to render (the `http → domain` edge in the spine diagram) but performs no arithmetic itself. [Source: ARCHITECTURE-SPINE.md#AD-10]
- **AD-4 — decimal, non-negative magnitudes, direction from type.** Unchanged; `decimal.Neg` for the owed presentation (no float). [Source: ARCHITECTURE-SPINE.md#AD-4]
- **Conventions — archived/asset/liability:** "Archived accounts excluded from both Portfolio total and Net Worth; credit balances are liabilities reducing Net Worth." The signed-balance model encodes the liability sign. [Source: ARCHITECTURE-SPINE.md#Consistency Conventions]

### Previous-story intelligence (3.1 + 2.3) — load-bearing

[Source: 3-1-record-cash-income-expenses.md; 2-3-create-manage-accounts.md; [[financas-epic1-progress]]]

- **`service/transaction` already exists** with `Record/Edit/Delete/Balance/List`, one tx per write (AD-3), reading accounts via `store.GetAccount`. The only logic change is the `validate` type guard (cash → cash+credit) and the error rename. `Record`/`Edit` call `validate`; both now accept credit.
- **`domain.AccountBalance(accountID, currency, []BalanceTxn) money.Money`** is the canonical balance (`Σ to[to=A] − Σ from[from=A]`); `domain` already imports `internal/money`. `AmountOwed` is a tiny pure addition next to it.
- **`money.Money`** has `Amount()` (`decimal.Decimal`), `Currency()`, `New(amount, currency)`, `String()` (`"-300.0000 USD"`). `decimal.Decimal.Neg()` negates. No new `money` method needed.
- **Account-detail page** is `web.AccountDetailPage(...)` rendered by `renderAccountDetail` in `router.go` (active nav `accounts`); it currently hardcodes the "Balance" heading and shows `bal.String()`. The 2.3 `balanceLabel(typ)` helper in `router.go` already returns "Balance owed" for credit / "Cash balance" otherwise — reuse its logic (or a small inline switch) for the detail heading: credit ⇒ "Balance owed", else "Balance".
- **`account.AccountType`** constants `Cash`/`Credit`/`Investment`; the account row's `Type` is `account.AccountType`. `service/transaction` reads the account via `store.GetAccount` and checks `acct.Type` (a `string`: `"cash"`/`"credit"`/`"investment"`).
- **Tests:** the 3.1 `transaction_test.go` asserted expense-on-credit ⇒ `ErrNotCashAccount`; that case is now valid and MUST be updated (this is a required regression edit). http tests use `stubTransactions` + `stubAccounts` (now with `Get`); the credit-detail assertion needs a credit account in `stubAccounts` and the stub balance to go negative on expense (the existing `stubTransactions.Balance` already subtracts expenses → negative; `domain.AmountOwed` negates for display).
- **Build with `GOTOOLCHAIN=local`**; `make nofloat` guards `internal/{money,domain,service,store}`. Local DB host **5433**; DB-gated tests skip without `DATABASE_URL`/`TEST_DATABASE_URL`. Dev login `owner`/`financas`. `baseline_commit` real SHA `11a8fc1`. Commit + push to `main` when done (one commit per story).

### Project Structure Notes

Modified only (no new files): `internal/service/transaction/transaction.go` (widen `validate`, rename error) + `transaction_test.go`; `internal/domain/balance.go` (`AmountOwed`) + `balance_test.go`; `internal/http/router.go` (type-aware balance label/value in `renderAccountDetail`, import `domain`) + `router_test.go`; `web/pages.templ` (`AccountDetailPage` `balanceLabel` param) + regenerated `web/pages_templ.go` + rebuilt `web/static/css/app.css`; `README.md`. No migration, no sqlc, no new package.

### Testing standards

- `domain`: pure unit test for `AmountOwed` (exact decimals, currency preserved).
- `service/transaction`: DB-gated — credit expense drives the signed balance negative; credit income reduces it; investment account rejected (`ErrUnsupportedAccountType`); cash still works.
- `http`: stub-backed — credit account detail shows "Balance owed" + positive amount; cash detail still shows "Balance".
- `go test ./...` green with no DB; `go vet` + `make nofloat` clean.

### References

- [Source: epics.md#Story 3.2] — acceptance criteria; [Source: epics.md FR-6] — credit expense increases balance owed, liability display
- [Source: ARCHITECTURE-SPINE.md#AD-2 / #AD-9 / #AD-10 / #AD-4] — derived on read; uniform from/to shape; one canonical figure, http no math; decimal magnitudes
- [Source: 3-1-record-cash-income-expenses.md] — `service/transaction`, `domain.AccountBalance`, account-detail page, the cash-only guard this story widens

## Dev Agent Record

### Agent Model Used

claude-opus-4-8[1m] (Opus 4.8, 1M context)

### Debug Log References

- No migration / sqlc / new package — service-guard + presentation change only.
- `go build`/`go vet`/`make nofloat` clean (`decimal.Neg`, no float). Full suite green with and without a DB (DB-gated tests skip cleanly).
- Live DB: `domain.TestAmountOwed` (pure) + `transaction.TestTransaction` PASS — credit expense 200 ⇒ signed balance −200; refund 50 ⇒ −150; investment account ⇒ `ErrUnsupportedAccountType`; cash path unchanged.
- One test self-correction: the first credit http assertion wrongly forbade `-500.0000 USD` globally, but the **expense register row legitimately renders the signed −500** (only the balance area is the positive owed). Rewrote it to use two expenses (500 + 30) so the owed total **530.0000 USD** appears only in the balance area — a robust, non-brittle check.
- Live HTTP smoke (server :8097 + db :5433, owner/financas): credit account, expense 500 ⇒ "Balance owed: **500.0000 USD**"; refund (income) 120 ⇒ "Balance owed: **380.0000 USD**"; reload re-derives the same; detail heading reads "Balance owed".

### Completion Notes List

All three acceptance criteria verified (pure unit + live DB + live HTTP):
- **AC1 — balance owed increases by the expense:** widened the `service/transaction` type guard to accept **cash + credit** (reject investment, `ErrUnsupportedAccountType`). An expense debits the credit account's `from` side, so `domain.AccountBalance` goes negative by the amount (owed up); a refund (income) credits it (owed down). No schema/query change — the AD-9 ledger already supported it.
- **AC2 — displayed as a liability:** added `domain.AmountOwed(balance) = −balance` (the single home for the owed figure, AD-10); the account-detail page shows "Balance owed: {positive}" for credit accounts (label + value chosen by type in `renderAccountDetail`, which calls `domain` to render — `http` does no arithmetic itself).
- **AC3 — excluded from assets but reduces Net Worth:** satisfied structurally — `AccountBalance` stays the uniform **signed** figure, so a credit account's negative balance lowers the eventual `Σ`-of-balances Net Worth (Epic 4) with no special case. No Net-Worth view built here.

Decisions / variances (intentional):
- **Renamed `ErrNotCashAccount` → `ErrUnsupportedAccountType`** ("cash or credit"); the Story 3.1 test asserting expense-on-credit ⇒ error was updated (it is now a valid, tested success path; the rejection case moved to an investment account).
- **`http` now imports `internal/domain`** to render `AmountOwed` — the `http → domain` edge is in the spine diagram; `http` renders the domain figure, it does not compute it (AD-10).
- **Income allowed on credit** (a refund reduces owed) in addition to expense, for completeness; the AC's focus (expense increases owed) is the primary path. The accounts **list** balance still shows the Story 2.3 placeholder (`Balance owed: —`); the real owed figure is on the detail page (list-balance wiring deferred).

Reviewer notes: no `sprint-status.yaml` → status tracked in this file only. `baseline_commit` is the real SHA `11a8fc1` (HEAD before this story). Committed + pushed to `main` per the owner's standing instruction.

### File List

Modified (no new files):
- `internal/service/transaction/transaction.go` (widen `validate` to cash+credit; rename `ErrNotCashAccount` → `ErrUnsupportedAccountType`; package doc) + `transaction_test.go` (credit success + investment rejection)
- `internal/domain/balance.go` (`AmountOwed`) + `balance_test.go` (`TestAmountOwed`)
- `internal/http/router.go` (type-aware balance label/value in `renderAccountDetail`; import `domain`) + `router_test.go` (`TestCreditAccountShowsBalanceOwed`)
- `web/pages.templ` (`AccountDetailPage` `balanceLabel` param) + regenerated `web/pages_templ.go`, rebuilt `web/static/css/app.css`
- `README.md` (credit/liability note)

## Change Log

| Date | Change |
| --- | --- |
| 2026-06-28 | Story 3.2 drafted (create-story): widen income/expense to credit accounts; `domain.AmountOwed` liability figure; credit account-detail shows "Balance owed". Status → ready-for-dev. |
| 2026-06-28 | Story 3.2 implemented (dev-story): cash+credit guard (reject investment); `domain.AmountOwed`; credit account-detail renders "Balance owed" as a positive liability. All 3 ACs verified (pure unit + live DB + live HTTP). build/vet/test + nofloat green. Status → review. |
