---
baseline_commit: 22d47da
---

# Story 4.2: Investment transactions & derived holdings

Status: review

<!-- Note: Validation is optional. Run validate-create-story for quality check before dev-story. -->

## Story

As the owner,
I want to record buys, sells, and dividends and see my holdings update,
so that my positions and cost basis stay accurate.

## Acceptance Criteria

From `epics.md` → Epic 4 → Story 4.2 (realizes FR-5, FR-4). **Given** a Security and an investment Account exist, **When** I record a trade, **Then**:

1. A **Buy** increases the Holding's quantity and Cost Basis by `quantity×price + fees` and **decreases the account's cash balance by the same amount** (FR-5, AD-2).
2. A **Sell** credits cash by `quantity×price − fees`, **reduces Cost Basis proportionally**, and **records realized Gain/Loss** using a single shared `basis_sold` domain function (AD-10); an **oversell is rejected** (selling more than held).
3. A **cash Dividend** credits the account's cash by an entered amount and **leaves quantity and Cost Basis unchanged**.
4. **Holdings are read-only and derived on read** (average-cost), never edited directly (FR-4); zero-quantity holdings are hidden from the active list but their realized Gain/Loss is retained.

> **Scope (this story):** extend the `transaction` ledger with investment columns (`security_id`, `quantity`, `price`, `fees`) + widen the `type` CHECK to add `buy|sell|dividend` (`00010`); the canonical average-cost derivation in `domain` (`Holding`, `DeriveHoldings`, and the single shared `BasisSold` function); `service/transaction` `Buy`/`Sell`/`Dividend` write use-cases + a `Holdings` read; and the investment-account-detail UI (trade forms + derived holdings table + cash balance + the account's investment register). **Trades are corrected via delete + re-add (NO in-place edit), mirroring transfers (3.3)** — delete reuses the existing generic `Delete`. **NOT in this story:** prices/valuation/unrealized gain (Story 4.3/4.4 — no `Price` yet, so holdings show qty, avg cost, cost basis, realized G/L, but no market value), the portfolio dashboard / Net-Worth aggregation (4.4), and any cross-currency trade (rejected at entry — see decisions).

## Tasks / Subtasks

- [x] **Task 1 — Extend the `transaction` ledger for investment trades (AC: #1, #2, #3)**
  - [x] Add goose migration `db/migrations/00010_investment_transactions.sql`. Up (one `-- +goose StatementBegin/End` per statement, mirroring `00006`'s CHECK-widen style):
    ```sql
    ALTER TABLE transaction ADD COLUMN security_id BIGINT REFERENCES security (id);
    ALTER TABLE transaction ADD COLUMN quantity NUMERIC(28, 10) NOT NULL DEFAULT 0 CHECK (quantity >= 0);
    ALTER TABLE transaction ADD COLUMN price    NUMERIC(19, 4)  NOT NULL DEFAULT 0 CHECK (price >= 0);
    ALTER TABLE transaction ADD COLUMN fees     NUMERIC(19, 4)  NOT NULL DEFAULT 0 CHECK (fees >= 0);
    ALTER TABLE transaction DROP CONSTRAINT IF EXISTS transaction_type_check;
    ALTER TABLE transaction ADD CONSTRAINT transaction_type_check
        CHECK (type IN ('income', 'expense', 'transfer', 'buy', 'sell', 'dividend'));
    CREATE INDEX transaction_security ON transaction (security_id);
    ```
    Down: drop the index, restore the narrower CHECK (`income|expense|transfer`), drop `fees`/`price`/`quantity`/`security_id` (reverse order).
  - [x] **Column types mirror the existing magnitude columns**: `quantity NUMERIC(28,10)` per the spine's share-quantity scale; `price`/`fees NUMERIC(19,4)` (money scale); all three `NOT NULL DEFAULT 0` (like `from_amount`/`to_amount`) so sqlc keeps generating `decimal.Decimal` (NOT `pgtype.Numeric`). `security_id` is **nullable** (→ `pgtype.Int8`); it is NULL for income/expense/transfer and set for buy/sell/dividend.
  - [x] **No raw sign anywhere** (AD-4): the cash effect comes from the one-row from/to placement, exactly as today — a **buy debits** the account (`from_account_id = account`, `from_amount = quantity×price + fees`); a **sell credits** (`to_account_id = account`, `to_amount = quantity×price − fees`); a **dividend credits** (`to_account_id = account`, `to_amount = entered amount`). This means **`domain.AccountBalance` already derives the investment cash balance with NO change** (buy debit / sell+dividend credit fall straight out of the existing from/to sum).

- [x] **Task 2 — sqlc: thread the 4 new columns through every full-row query (AC: #1, #2, #3)**
  - [x] **CRITICAL sqlc lesson (bit 3.4 and 3.6 — do not repeat):** the ALTERs append `security_id, quantity, price, fees` as the **last** physical columns. Every full-row `SELECT`/`RETURNING` list MUST end `… created_at, category_id, import_hash, security_id, quantity, price, fees` (exact physical order) or sqlc emits a bespoke `*Row` type that breaks `toTransaction`/`store.Transaction`. Update in `db/query/transaction.sql`: `CreateTransaction` (RETURNING **and** add `$9 security_id, $10 quantity, $11 price, $12 fees` to the INSERT column list + VALUES), `ListAccountTransactions` (SELECT), `ListTransactions` (SELECT). **Also `db/query/category.sql` `ListCategoryTransactions`** (SELECT — it returns full rows too). `UpdateTransaction` is unchanged (income/expense edit never touches trade columns; trades aren't edited). `CreateImportedTransaction` is unchanged (the new columns default to 0/NULL).
  - [x] Run `make sqlc` (pinned `sqlc/sqlc:1.27.0` Docker image — NOT `go run`). Confirm `store.Transaction` gains `SecurityID pgtype.Int8`, `Quantity/Price/Fees decimal.Decimal`, and that `CreateTransaction`/`ListAccountTransactions`/`ListTransactions`/`ListCategoryTransactions` all still return `store.Transaction` (no bespoke row). Commit the regenerated `internal/store/*.sql.go` + `models.go`/`querier.go`.

- [x] **Task 3 — `domain`: canonical average-cost Holdings + the shared `BasisSold` (AC: #1, #2, #4)**
  - [x] Add `internal/domain/holding.go`:
    - `BasisSold(qtySold, qtyHeld, basisBefore decimal.Decimal) decimal.Decimal` — **the single shared function** (AD-2/AD-10): if `qtySold.Equal(qtyHeld)` return `basisBefore` exactly (zero-crossing wipe — decision Q4, no rounding crumb); otherwise `basisBefore.Mul(qtySold.Div(qtyHeld))` rounded **once** to `money.MoneyScale` with banker's rounding (`.RoundBank(money.MoneyScale)`). `decimal.DivisionPrecision` is already set to 12 globally in `money.init` — intermediates carry full precision. Both `remaining_basis = basisBefore − BasisSold(...)` and `realized_gain = proceeds − BasisSold(...)` use this same value so they reconcile by construction (AD-2).
    - A `TradeEvent` input struct (pure projection of a ledger row): `SecurityID int64`, `Type string` (`buy|sell|dividend`), `Quantity, Price, Fees decimal.Decimal`, `CashAmount decimal.Decimal` (the dividend's credited amount; unused for buy/sell). Events are passed **already sorted chronologically** (`occurred_on ASC, id ASC`).
    - `Holding` struct: `SecurityID int64`, `Quantity decimal.Decimal`, `CostBasis money.Money`, `RealizedGain money.Money` (all in the account's native currency).
    - `ErrOversold = errors.New("domain: sell exceeds holdings")`.
    - `DeriveHoldings(currency money.Currency, events []TradeEvent) ([]Holding, error)` — fold events per security in order: **buy** → `qty += Quantity`, `basis += Quantity×Price + Fees`; **sell** → if `Quantity` (exact decimal compare) `> qtyHeld` return `ErrOversold`; `bs := BasisSold(Quantity, qtyHeld, basis)`; `proceeds := Quantity×Price − Fees`; `realized += proceeds − bs`; `basis -= bs`; `qty -= Quantity`; **dividend** → no qty/basis/realized change (cash only). Return holdings sorted by SecurityID; **include zero-quantity holdings** (their `RealizedGain` is retained — AC#4) so callers can show realized G/L; the caller hides qty==0 rows from the *active* list. `CostBasis`/`RealizedGain` are `money.New(decimal, currency)`.
  - [x] Add `internal/domain/holding_test.go` (pure unit): single buy (qty+basis incl. fees); two buys then a partial sell (average-cost basis_sold proportional, rounded once); **full-position sell → exact wipe** (`BasisSold == basisBefore`, remaining basis 0, no crumb); realized gain = `proceeds − basis_sold` with fees reducing proceeds (decision Q2); dividend leaves qty/basis unchanged; **oversell → `ErrOversold`**; `BasisSold` direct table tests incl. the zero-crossing branch.

- [x] **Task 4 — `service/transaction`: Buy / Sell / Dividend writes + Holdings read (AC: #1, #2, #3, #4)**
  - [x] Add `TxType` consts `Buy = "buy"`, `Sell = "sell"`, `Dividend = "dividend"`. **Keep `TxType.IsValid()` = income|expense only** (so the existing `Record`/`Edit` still reject trades), exactly as transfers are excluded.
  - [x] New typed errors: `ErrNotInvestmentAccount` ("buy/sell/dividend require an investment account"), `ErrSecurityNotFound`, `ErrTradeCurrencyMismatch` ("security quote currency must equal the account currency"), `ErrNonPositiveQuantity`, `ErrNonPositivePrice`, `ErrNegativeFees`, `ErrNegativeProceeds` ("fees exceed gross proceeds"), `ErrOversold` (wrap/return `domain.ErrOversold`).
  - [x] `validateTrade(ctx, accountID, securityID)` helper → returns the loaded account + security or a typed error: account exists and `Type == "investment"` (else `ErrNotInvestmentAccount`); security exists via **`store.GetSecurity`** (NOT `service/security` — store-not-service rule, like `GetAccount`/`GetCategory`); **same-currency-only (decision Q1): `account.currency == security.quote_currency` else `ErrTradeCurrencyMismatch`**.
  - [x] `Buy(ctx, accountID, securityID int64, quantity, price, fees decimal.Decimal, date time.Time, description string) (Transaction, error)`: validateTrade; `quantity>0` (`ErrNonPositiveQuantity`), `price>0` (`ErrNonPositivePrice`), `fees>=0` (`ErrNegativeFees`); `cost := quantity×price + fees`; one tx → `CreateTransaction{Type:"buy", FromAccountID:account, FromAmount:cost, ToAmount:0, SecurityID:security, Quantity:quantity, Price:price, Fees:fees, CategoryID:NULL}`.
  - [x] `Sell(...)` same signature: validateTrade + amount checks; **oversell guard** — derive current held qty for (account, security) by reading the account's existing trade rows and calling `domain.DeriveHoldings`, reject with `ErrOversold` if `quantity > held` (exact decimal compare, NUMERIC(28,10), no epsilon — decision Q3); `proceeds := quantity×price − fees`; if `proceeds < 0` → `ErrNegativeProceeds`; one tx → `CreateTransaction{Type:"sell", ToAccountID:account, ToAmount:proceeds, FromAmount:0, SecurityID:security, Quantity, Price, Fees}`.
  - [x] `Dividend(ctx, accountID, securityID int64, amount decimal.Decimal, date, description) (Transaction, error)`: validateTrade; `amount>0` (`ErrNonPositiveAmount`); one tx → `CreateTransaction{Type:"dividend", ToAccountID:account, ToAmount:amount, SecurityID:security, Quantity:0, Price:0, Fees:0}`.
  - [x] **Update the EXISTING `CreateTransaction` callers** (`Record`, `Transfer`) to pass the four new params: `SecurityID: pgtype.Int8{}`, `Quantity/Price/Fees: decimal.Zero`. (They keep storing no security and zero trade fields.)
  - [x] `Holdings(ctx, accountID int64) ([]HoldingView, error)`: load the account (must be investment) + its rows via `ListAccountTransactions`, **sort chronological** (the query already returns `occurred_on DESC, id DESC` — reverse to ASC for the fold, or add a dedicated ASC query; document which), build `[]domain.TradeEvent` from buy/sell/dividend rows, resolve a security id→{symbol,name} map (new `securityNames`/`securityMeta` helper over `store.ListSecurities`), call `domain.DeriveHoldings(account.currency, events)`. Return a `HoldingView` per **active** holding (qty>0): `{Symbol, Name, Quantity, AvgCost money.Money (=CostBasis/qty, rounded at display), CostBasis, RealizedGain}` plus the cumulative realized G/L (sum of all securities' realized, incl. closed positions). Surface `domain.ErrOversold` (a data inconsistency from a delete) as a typed error the handler renders as a warning.
  - [x] Extend the display `Transaction` struct + `toTransaction` so a trade row shows its security + qty/price: add `SecurityID int64`, `Security string` (symbol), `Quantity, Price decimal.Decimal`. Build a security id→symbol map in `List` (like `accountNames`/`categoryNames`). A buy is outgoing (from=acct), a sell/dividend incoming (to=acct) — the existing `toTransaction` perspective logic already sets `Incoming`/`Amount` correctly from from/to.
  - [x] **Extend `Register`'s switch** so the cross-account register (`/transactions`, Story 3.5) renders trades instead of mis-classifying them: `buy` → outgoing cash on `from` (security-labelled), `sell`/`dividend` → incoming cash on `to`. Add the security symbol to the register row label. (Otherwise a sell with no `from_account` renders blank under the current `default: // Expense` branch.)
  - [x] DB-gated tests in `transaction_test.go`: buy (cash debited by `qty×price+fees`, holding qty+basis); two buys + partial sell (avg-cost basis_sold, realized gain with fees reducing proceeds); **full sell → exact wipe + position closed**; dividend (cash credited, holding unchanged); **oversell rejected**; **cross-currency trade rejected** (`ErrTradeCurrencyMismatch`); trade on a cash/credit account rejected (`ErrNotInvestmentAccount`); `Holdings` returns the derived active holdings + cumulative realized G/L; income/expense `Record` still rejects investment accounts (unchanged).

- [x] **Task 5 — Investment account-detail UI: trade forms + holdings + register (AC: #1, #2, #3, #4)**
  - [x] On `GET /accounts/{id}` for an **investment** account, `renderAccountDetail` branches to show: (a) the **cash balance** (existing `Balance`, label "Cash balance"); (b) a **Holdings table** (Security, Quantity, Avg cost, Cost basis, Realized G/L) from `deps.Transactions.Holdings`, plus a cumulative realized G/L line; (c) **trade forms** — Buy and Sell (security `<select>` filtered to securities whose `quote_currency == account.currency`, quantity, price, fees, date) and Dividend (security `<select>`, amount, date); (d) the account's investment **transaction list** (existing `List`, showing each trade's security + qty + price + signed cash), each row with a **Delete** button (reuse `POST /accounts/{id}/transaction/delete`). Non-investment accounts keep today's income/expense/transfer UI unchanged.
  - [x] If `Holdings` returns `ErrOversold`, render a clear warning banner ("a sell exceeds holdings — delete or fix the offending trade") instead of the holdings table; do not block the rest of the page.
  - [x] Extend the `Transactions` interface (in `internal/http/router.go`) with `Buy`, `Sell`, `Dividend`, and `Holdings`. Add handlers `tradeBuy`/`tradeSell`/`tradeDividend` + routes `pr.Post("/accounts/{id}/buy")`, `/sell`, `/dividend` (alongside the existing `/transaction`, `/transfer` routes). Parse `security_id`, `quantity`, `price`, `fees`/`amount`, `date` as **decimal strings** (never float, AD-4) via `decimal.NewFromString`; on parse/validation error re-render the detail page with the message + 400 (mirror `txTransfer`). On success redirect to the account detail (303).
  - [x] Add `web` view structs (`web/shell.go`): `HoldingRow{Symbol, Name, Quantity, AvgCost, CostBasis, RealizedGain string}` and a `SecurityChoice{ID int64, Symbol string}` for the trade `<select>`; extend `TxRow` with `Security string`, `Quantity string`, `Price string` (shown for trade rows). Add the investment branch + the three trade forms + holdings table to `AccountDetailPage` (or a dedicated `InvestmentAccountDetailPage` component — pick one and keep the five-item nav + `shellData(..., "accounts")` active key). `make generate css`; commit `*_templ.go` + `app.css`.

- [x] **Task 6 — Tests, verify, docs (AC: all)**
  - [x] `internal/http/router_test.go`: extend the stub `Transactions` with `Buy`/`Sell`/`Dividend`/`Holdings` (in-memory; reject oversell + non-investment + currency mismatch like the real service) and register it. Add a test: on an investment account, record a buy then see the holding; record a sell (partial) and see qty/basis reduced + realized G/L; a dividend leaves the holding unchanged; an oversell returns 400; the trade list shows the security; delete a trade.
  - [x] `GOTOOLCHAIN=local go build ./... && go vet ./... && go test ./...` green (DB-gated tests skip without a DB). `make nofloat` stays green — **all new quantity/price/basis math is `shopspring/decimal` in `domain`/`service`; no float32/64 anywhere** (incl. the `quantity/price/fees` round-trip). `gofmt -l` clean.
  - [x] Live smoke (compose db :5433 + run, owner/financas): create an investment account (BRL) + a BRL security; **Buy** 100 @ 10.00 fee 5.00 ⇒ cash −1005.00, holding qty 100 / basis 1005.00 / avg 10.05; **Buy** 100 @ 12.00 fee 0 ⇒ qty 200 / basis 2205.00 / avg 11.025; **Sell** 50 @ 15.00 fee 3.00 ⇒ cash += 747.00, basis_sold = 2205×(50/200)=551.25, realized = 747.00 − 551.25 = 195.75, remaining qty 150 / basis 1653.75; **Sell** all 150 @ 16 ⇒ exact wipe (basis_sold = 1653.75, qty 0, position closed, realized accrues); **Dividend** 40.00 ⇒ cash +40, holding unchanged; **oversell** (sell 999) ⇒ rejected; **cross-currency** (USD security on the BRL account) ⇒ rejected; delete a trade ⇒ holdings re-derive. Persistence across reload.
  - [x] Update `README.md` (investment transactions: buy/sell/dividend on an investment account; holdings derived average-cost on read; fees add to basis on buy / reduce proceeds on sell; oversell rejected; same-currency-only; corrected via delete + re-add; prices/valuation come in 4.3/4.4).

## Dev Notes

### Scope boundary — what this story does NOT do (read first)

- **NO prices / valuation / unrealized gain / net worth** — there is no `Price` entity until Story 4.3 and no portfolio aggregation until 4.4. Holdings show quantity, average cost, cost basis, and **realized** G/L only — **no market value / unrealized G/L** (it would need a price).
- **NO in-place edit of trades** — corrected via **delete + re-add** (decision Q2), exactly like transfers (3.3, which also have no edit). Delete reuses the existing generic `Delete(txID)`.
- **NO cross-currency trade** — same-currency-only (decision Q1): a security trades only in an account whose currency equals the security's quote currency. No FX inside a trade; basis and cash share one currency, so a holding's `CostBasis`/`RealizedGain` are single-currency `money.Money`.
- **NO change to income/expense/transfer behavior** — `TxType.IsValid()` stays income|expense; `Record`/`Edit` still reject investment accounts and trades; investment cash balance derivation is unchanged (it already works via from/to).
- **Dividend = an entered cash amount tied to a security** (decision: total cash, not qty×per-share); `quantity/price/fees` are 0 for dividends; it never affects qty/basis/realized gain (it is income, not a capital gain).

### Architecture invariants this story must honor

- **AD-2 / AD-10 — derived on read, one home.** Holdings (qty, cost basis), realized Gain/Loss are **derived**, never stored. `BasisSold` is the **single** shared function feeding both `remaining_basis` and `realized_gain` so they reconcile by construction. `service` loads ledger rows and calls `domain`; `http` only renders. [Source: ARCHITECTURE-SPINE.md#AD-2, #AD-10]
- **AD-3 — one tx per use-case.** `Buy`/`Sell`/`Dividend` each wrap their single insert in one `pool.Begin` tx. The oversell guard reads (no write) before the tx. [Source: #AD-3]
- **AD-4 — decimal, non-negative magnitudes, no raw sign.** `quantity NUMERIC(28,10)`, `price`/`fees NUMERIC(19,4)`; cash direction is the from/to placement, never a sign. Average-cost uses `decimal.DivisionPrecision = 12` (already set in `money`), rounding once to `money.MoneyScale` (banker's) in `BasisSold`. `make nofloat` must stay green. [Source: #AD-4, #Consistency Conventions (`basis_sold` rule)]
- **AD-5 — native currency.** Same-currency-only means a trade's basis and cash are one currency; nothing is converted here (Display-Currency aggregation is 4.4). [Source: #AD-5]
- **AD-9 — one-row shape.** A trade is one `transaction` row; buy uses the `from` leg (cash out), sell/dividend the `to` leg (cash in). No second row. [Source: #AD-9]
- **AD-1 — layering.** New derivation in `domain` (imports only `money`); `service/transaction` reads securities via `store.GetSecurity` (no service→service) and calls `domain`; `http` defines the interface and renders. [Source: #AD-1]
- **Capability map note:** the spine tentatively names a `service/holding`; this story instead **extends `service/transaction`** (the established single owner of the `transaction` table — it already owns `Balance`/`List`/`Register`/`CategoryTransactions`). Adding a second service writing/reading the same table would tension AD-3's single-mutation-path and duplicate ledger reads. Documented variance.

### Decided for Epic 4 (apply here) — see memory `financas-epic4-decisions`

1. **Same-currency-only (confirmed)** → `validateTrade` rejects `account.currency != security.quote_currency` (`ErrTradeCurrencyMismatch`). Trade-form security `<select>` is filtered to matching-currency securities.
2. **Fees on Sell reduce proceeds, not basis** → `proceeds = qty×price − fees`; basis is untouched by sell fees (only `BasisSold` reduces it). Buy fees DO add to basis (`cost = qty×price + fees`).
3. **Oversell** → exact `NUMERIC(28,10)` compare, no epsilon/float; `qtySold > qtyHeld` ⇒ reject (`ErrOversold`).
4. **Zero-crossing** → selling the entire position makes `BasisSold = basisBefore` exactly (remaining basis 0), not the proportional rounded value — no cent crumb / phantom gain.
5. (Net-Worth-with-missing-rate is 4.4 — not used here.)

### Previous-story intelligence (3.x + 4.1) — load-bearing

[Source: 3-1/3-3/3-4-*.md; 4-1-manage-securities.md; [[financas-epic1-progress]]; [[financas-epic4-decisions]]]

- **The ledger + its derivation already exist.** `domain.AccountBalance` (one canonical balance fn) sums `to` credits − `from` debits — so buy/sell/dividend cash effects need **no balance change**, only correct from/to placement. `domain.SumByCurrency` is the per-currency aggregation home. Add `Holding`/`BasisSold`/`DeriveHoldings` as the next `domain` derivations. [Source: internal/domain/balance.go, sum.go]
- **sqlc full-row column-order lesson (bit 3.4 + 3.6 — THIRD time):** appending columns to `transaction` requires appending them, in physical order, to **every** full-row `SELECT`/`RETURNING` (`CreateTransaction`, `ListAccountTransactions`, `ListTransactions`, **and `category.sql` `ListCategoryTransactions`**). Current order ends `… created_at, category_id, import_hash`; after `00010` it ends `… import_hash, security_id, quantity, price, fees`. Miss one ⇒ bespoke `*Row` type breaks `toTransaction`. [Source: db/query/transaction.sql, category.sql; [[financas-epic1-progress]]]
- **`store.Transaction` is shared** by all those queries; every `CreateTransactionParams` builder (`Record`, `Transfer`, + new `Buy`/`Sell`/`Dividend`) must set the new fields (zero/NULL for non-trades). [Source: internal/service/transaction/transaction.go]
- **Security reads** go through `store.GetSecurity`/`store.ListSecurities` (added in 4.1) — never `service/security` (AD-1, store-not-service rule, same as `GetAccount`/`GetCategory`). [Source: 4-1-manage-securities.md]
- **Investment accounts exist (2.3) but are currently inert** — `validate` rejects income/expense on them; this story gives them their first real behavior. The `account.type == "investment"` enum is already defined (`service/account.Investment`). [Source: internal/service/account/account.go, internal/service/transaction/transaction.go:417]
- **UI pattern:** `renderAccountDetail` (router.go:738) builds the per-account page; it already branches credit vs cash for the balance label. Add an investment branch. The trade form mirrors `txTransfer`'s decimal parsing (`decimal.NewFromString`, never float). Delete reuses `txDelete`. [Source: internal/http/router.go]
- **Environment:** build/test `GOTOOLCHAIN=local`; `make sqlc` via Docker (not `go run`); local DB host **5433** (`docker compose up -d db`), DB-gated tests skip without `DATABASE_URL`/`TEST_DATABASE_URL`; dev login `owner`/`financas`; `make generate css` after `.templ`/CSS; `make nofloat` must stay green. Commit + push to `main` when done. `baseline_commit` real SHA `22d47da`.

### Project Structure Notes

New: `db/migrations/00010_investment_transactions.sql`; `internal/domain/holding.go` (+ `holding_test.go`).
Modified: `db/query/transaction.sql` + `db/query/category.sql` (+4 columns on full-row lists) → regenerated `internal/store/*.sql.go`/`models.go`/`querier.go`; `internal/service/transaction/transaction.go` (`Buy`/`Sell`/`Dividend`/`Holdings`, `validateTrade`, trade `TxType` consts + errors, security id→symbol map, `Register` switch, `toTransaction`/`Transaction` trade fields, updated `Record`/`Transfer` param builders) + `transaction_test.go`; `internal/http/router.go` (`Transactions` iface +Buy/Sell/Dividend/Holdings, trade routes/handlers, investment branch in `renderAccountDetail`) + `router_test.go`; `web/pages.templ` (investment detail: trade forms + holdings table) + regenerated `pages_templ.go`, `web/shell.go` (`HoldingRow`, `SecurityChoice`, `TxRow` trade fields), rebuilt `app.css`; `README.md`. No `cmd/server/main.go` change (same `transaction.New`).

### Testing standards

- `domain` (pure unit, no DB): `BasisSold` table tests incl. zero-crossing branch; `DeriveHoldings` — buy avg-cost, partial sell proportional basis, full sell exact wipe, dividend no-op, oversell → `ErrOversold`, realized gain reconciles (`remaining + basis_sold == basis_before`; `realized == proceeds − basis_sold`).
- `service/transaction` (DB-gated): the smoke arithmetic above (buy/sell/dividend cash + holding effects), oversell/currency-mismatch/non-investment rejection, `Holdings` output, income/expense still rejecting investment accounts.
- `http` (stub-backed): investment-account detail trade forms + holdings render; oversell → 400; trade list + delete.
- `go test ./...` green with no DB; `go vet` + `make nofloat` clean.

### References

- [Source: epics.md#Story 4.2] — ACs; [Source: epics.md FR-5, FR-4] — buy/sell/dividend cash + basis effects, derived average-cost holdings, oversell
- [Source: ARCHITECTURE-SPINE.md#AD-2/#AD-10] — derived-on-read, one `basis_sold` home; [#Consistency Conventions] — `basis_sold = total_basis × (qty_sold/qty_held)` rounded once, banker's, DivisionPrecision 12; [#AD-3/#AD-4/#AD-5/#AD-9/#AD-1]
- [Source: internal/domain/balance.go] — `AccountBalance` already derives investment cash; [internal/service/transaction/transaction.go] — Record/Transfer/legs/toTransaction/Register patterns to extend
- [Source: financas-epic4-decisions] — the 5 resolved decisions (same-currency-only, fees, oversell, zero-crossing, missing-rate)
- [Source: 4-1-manage-securities.md] — `store.GetSecurity`/`ListSecurities`, `security.quote_currency`

## Dev Agent Record

### Agent Model Used

claude-opus-4-8[1m] (Opus 4.8, 1M context)

### Debug Log References

- `make sqlc` regenerated `store.Transaction` with `SecurityID pgtype.Int8` + `Quantity/Price/Fees decimal.Decimal`; **zero bespoke `*Row` types** (the 4 columns were appended in physical order to every full-row SELECT/RETURNING incl. `category.sql`'s `ListCategoryTransactions` — the 3.4/3.6 hazard avoided). `make migrate` applied `00010` (goose → version 10).
- `domain` test helper `dec` already exists in `balance_test.go` — removed the duplicate from `holding_test.go` (build error) and its now-unused `decimal` import.
- **Test flakiness fixed:** trade/security tests originally built symbols with `run%100000`, which collides across repeated `go test` runs (the DB persists securities) → an intermittent duplicate-symbol failure in `TestTradeValidation`. Switched to the full `time.Now().UnixNano()` for symbol uniqueness.
- `internal/domain/balance_test.go` shows under `gofmt -l` but is **pre-existing drift** (reverted my incidental reformat to keep this story's diff scoped); `make` doesn't gate gofmt.
- build/vet/`go test ./...` green with and without a DB; `make nofloat` green (all quantity/price/basis math is `shopspring/decimal`).
- Live HTTP smoke (server :8099 + db :5433, owner/financas): investment BRL account; **the trade form lists only BRL securities** (USD security filtered out — same-currency-only); Buy 100@10 fee5 + Buy 100@12 → basis 2205; **Sell 50@15 fee3 → realized 195.75, remaining basis 1653.75** (rendered on the page); Dividend 40 (holding unchanged); **oversell 999 → 400**; **cross-currency buy (USD security, direct POST) → 400 "quote currency must equal the account currency"**.

### Completion Notes List

All four ACs verified (pure domain unit + live DB + live HTTP):
- **AC1 — Buy:** `Buy` debits cash by `qty×price+fees` (one row, from-leg); the holding's qty + basis grow by the same, derived on read. Investment cash balance falls straight out of the unchanged `domain.AccountBalance` (no balance-fn change).
- **AC2 — Sell:** credits cash by `qty×price−fees` (fees reduce proceeds, not basis); `domain.BasisSold` is the **single** function feeding both remaining basis and realized gain (reconcile by construction); oversell rejected via the same canonical derivation (exact NUMERIC(28,10) compare).
- **AC3 — Dividend:** credits cash by the entered amount; qty/basis/realized untouched (cash income, not a capital gain).
- **AC4 — Holdings:** read-only, derived average-cost in `domain.DeriveHoldings`; closed (qty 0) positions hidden from the active list but their realized G/L is retained in the cumulative figure.

Decisions / variances (intentional, documented):
- **Extended `service/transaction`** (the single owner of the `transaction` table) rather than the spine's tentative `service/holding` — avoids two services writing one table (AD-3 tension) and reuses the established Balance/List/Register reads. The Holdings derivation reverses the DESC ledger query to feed the chronological average-cost fold.
- **No in-place trade edit** — corrected via delete + re-add (decision Q2), like transfers; the generic `Delete` handles it. **Dividend = entered cash amount** (decision), `quantity/price/fees` 0.
- **Same-currency-only** enforced in `validateTrade` (`ErrTradeCurrencyMismatch`) and the trade-form security `<select>` is filtered to matching-currency securities. **`Register` switch extended** so trades render correctly on `/transactions` (a sell with no `from_account` no longer renders blank).
- `domain.ErrOversold` surfaces from a delete that orphans a later sell as a warning banner on the investment page (holdings can't be derived until fixed) — honors AD-2 (fix via the ledger).

### File List

New:
- `db/migrations/00010_investment_transactions.sql`
- `internal/domain/holding.go`, `internal/domain/holding_test.go`
- `internal/service/transaction/trade_test.go`

Modified:
- `db/query/transaction.sql` (+`security_id,quantity,price,fees` on `CreateTransaction`/`ListAccountTransactions`/`ListTransactions`), `db/query/category.sql` (`ListCategoryTransactions` full-row) → regenerated `internal/store/transaction.sql.go`/`category.sql.go`/`models.go` (querier.go unchanged — method signatures stable)
- `internal/service/transaction/transaction.go` (Buy/Sell/Dividend/Holdings/validateTrade/insertTrade/heldQuantity/deriveHoldings/securityMeta/securitySymbols, trade `TxType` consts + errors, `Transaction` trade fields, `toTransaction` +secNames, `Register` switch + `RegisterRow.Security`, updated `Record`/`Transfer` param builders) + `transaction_test.go` (unchanged) — new tests in `trade_test.go`
- `internal/service/security/security_test.go` (symbol uniqueness: full `run`)
- `internal/http/router.go` (`Transactions` iface +Buy/Sell/Dividend/Holdings, trade routes + `tradeBuy`/`tradeSell`/`tradeDividend`/`parseTradeForm`, `renderInvestmentDetail` + investment branch in `renderAccountDetail`, register `Security` mapping, `errors` import)
- `internal/http/router_test.go` (stub `Transactions` +Buy/Sell/Dividend/Holdings + `stubHolding`, `TestInvestmentAccountDetail`)
- `web/pages.templ` (`InvestmentAccountDetailPage` + `tradeForm`) + regenerated `web/pages_templ.go`; `web/shell.go` (`HoldingRow`, `SecurityChoice`, `TxRow` trade fields, `RegisterRow.Security`); rebuilt `web/static/css/app.css`
- `README.md` (investment transactions section)

## Change Log

| Date | Change |
| --- | --- |
| 2026-06-29 | Story 4.2 drafted (create-story): `00010` investment columns + type-CHECK widen; `domain` `Holding`/`DeriveHoldings`/`BasisSold` (average-cost, zero-crossing wipe); `service/transaction` Buy/Sell/Dividend + Holdings; investment account-detail UI. Decisions applied: same-currency-only, fees reduce proceeds, oversell reject, dividend = cash amount, no in-place edit (delete+re-add). Status → ready-for-dev. |
| 2026-06-29 | Story 4.2 implemented (dev-story): `00010` ledger extension; `domain.BasisSold`/`DeriveHoldings` (single shared basis_sold, exact zero-crossing wipe); `service/transaction` Buy/Sell/Dividend + Holdings (oversell + same-currency guards); investment account-detail UI (holdings + trade forms + delete). All 4 ACs verified (domain unit + live DB + live HTTP incl. oversell→400, cross-currency→400). build/vet/test/nofloat green. Status → review. |
