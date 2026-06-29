---
baseline_commit: 9f9ced7
---

# Story 4.4: Portfolio valuation & Net Worth

Status: review

<!-- Note: Validation is optional. Run validate-create-story for quality check before dev-story. -->

## Story

As the owner,
I want to see what my portfolio is worth and my net worth,
so that I know where I stand.

## Acceptance Criteria

From `epics.md` → Epic 4 → Story 4.4 (realizes FR-10). **Given** holdings, prices, balances, and exchange rates exist, **When** valuation runs, **Then**:

1. Each Holding shows **quantity, current Price, Valuation, Cost Basis, and unrealized Gain/Loss**; **cumulative realized Gain/Loss is shown** (FR-10).
2. The **Portfolio total** and **Net Worth** (all cash + holdings − credit owed, **excluding archived accounts**) are computed in the **Display Currency** by a **single canonical `domain` function** (AD-10).
3. Conversion is **convert-then-sum with banker's rounding** (AD-12).

> **Scope (this story):** the **cross-account, Display-Currency aggregation** that 4.3 deliberately left out. Build the single canonical `domain.NetWorth` (Portfolio total + Net Worth, convert-then-sum, banker's, with **missing-rate tracking** per decision Q5), a new `service/valuation` use-case that orchestrates the reads (all active accounts, the ledger, derived holdings, latest prices, FX rates, the Display Currency) and calls `domain`, and turn the **`/investments` page** (a `ComingSoon` placeholder since 1.4 — always slated for "4.4 portfolio/net worth") into the real portfolio view: per-holding rows across all investment accounts (native valuation from 4.3's `domain.ValueHolding`), the Display-Currency **Net Worth** + **Portfolio value**, **cumulative realized G/L per native currency** (decision below), and a **missing-FX-rate warning** (partial total, never blocked — Q5). **NOT in this story:** the KPI-card dashboard / trend chart / allocation / insight (all **Epic 5**: 5.2 KPI cards reuse this story's `domain.NetWorth`, 5.3 value-over-time, 5.4 allocation); **per-sell-date conversion of cumulative realized G/L** into the Display Currency (decision: deferred — see below); editing prices/rates/trades (owned by 4.3 / 2.2 / 4.2). No new migration, no sqlc change — every read reuses an existing query.

## Tasks / Subtasks

- [x] **Task 1 — `domain.NetWorth`: the single canonical Display-Currency aggregation (AC: #2, #3)**
  - [x] Add `internal/domain/networth.go` (imports only `money` + `decimal`, AD-1):
    - `ValuationInput` struct — the native-currency raw material, each element carrying its own currency:
      ```go
      type ValuationInput struct {
          Cash        []money.Money // cash + investment cash balances (assets), native
          Liabilities []money.Money // credit balances owed, as POSITIVE magnitudes, native
          Holdings    []money.Money // per-holding market value, native (priced holdings only)
      }
      ```
    - `Valuation` result struct (Display Currency; rounded once at the boundary):
      ```go
      type Valuation struct {
          PortfolioValue money.Money      // Σ converted Holdings market value
          NetWorth       money.Money      // Σ converted (Cash + Holdings) − Σ converted Liabilities
          Missing        []money.Currency // native currencies with NO rate to Display — excluded from the totals (Q5)
      }
      ```
    - `func NetWorth(display money.Currency, in ValuationInput, rates map[money.Currency]decimal.Decimal) Valuation` — the **single** home (AD-10) for both figures (they share inputs, so one walk). **Convert-then-sum** (AD-12): for each native amount, if its currency == `display` use it as-is; else if `rates[currency]` exists, `money.Convert(m, rates[currency], display)` at **full precision**; else **skip it and record the currency in `Missing`** (Q5 — partial total, never invert/guess, AD-6). Sum the converted amounts at full precision, then **round once** to the money scale (`money.New(sum, display).Rounded()`, banker's) for each of `PortfolioValue` and `NetWorth`. `NetWorth = round(ΣcashConv + ΣholdingsConv − ΣliabConv)`. `Missing` is **deduplicated, sorted, and only includes a currency when a NON-ZERO amount was skipped** (a zero balance in an unrated currency must not raise a spurious warning).
  - [x] Pure unit test `internal/domain/networth_test.go` (no DB): all-same-currency as Display (no rates needed) → exact totals; a USD holding + a `USD→BRL` rate, Display BRL → convert-then-sum, rounded once; a liability subtracts; a missing rate → that currency excluded from both totals AND listed in `Missing` (partial Net Worth), while the convertible part is still summed; a **zero** amount in an unrated currency → NOT in `Missing`; convert-then-sum vs sum-then-convert ordering proven (two amounts that round differently if summed in native first).

- [x] **Task 2 — `service/valuation`: orchestrate the reads + call `domain` (AC: #1, #2, #3)**
  - [x] Add `internal/service/valuation/valuation.go`, package doc in the house style (derives on read AD-2; reads everything via `store` — store-not-service, AD-1; no FX inversion AD-6; convert-then-sum AD-12). It reads but never writes (no tx needed). Types:
    ```go
    type HoldingValuation struct { // one per active (qty>0) holding, native currency
        AccountID int64; AccountName string
        SecurityID int64; Symbol, Name string
        Currency money.Currency
        Quantity decimal.Decimal
        HasPrice bool
        Price money.Money; PriceDate time.Time
        Valuation money.Money      // market value (qty×price), native; zero when !HasPrice
        CostBasis money.Money
        UnrealizedGain money.Money  // native; zero when !HasPrice
    }
    type Portfolio struct {
        Holdings           []HoldingValuation
        PortfolioValue     money.Money      // Display Currency
        NetWorth           money.Money      // Display Currency
        RealizedByCurrency []money.Money    // cumulative realized G/L per NATIVE currency (decision: no FX)
        Missing            []money.Currency // currencies excluded from the totals (no rate)
        Unpriced           []string         // symbols of held positions with no price (excluded from PortfolioValue)
        Display            money.Currency
    }
    ```
  - [x] `New(pool)`; `Portfolio(ctx) (Portfolio, error)`. Steps (all via `store.New(s.pool)`):
    1. `display := store.GetDisplayCurrency`.
    2. `accounts := store.ListActiveAccounts` — **non-archived only** (this is exactly how archived accounts are excluded from Net Worth, AC2; do NOT use `ListAllAccounts`).
    3. `ledger := store.ListTransactions` (the **whole** ledger, once) — build `[]domain.BalanceTxn` once; for holdings, bucket the buy/sell/dividend rows per account into `[]domain.TradeEvent` (reverse to chronological ASC, exactly like `service/transaction.deriveHoldings`).
    4. `prices := store.LatestPrices(time.Now())` → `map[securityID]LatestPricesRow`; `secMeta := store.ListSecurities` → id→{symbol,name}.
    5. For each active account compute its balance via **`domain.AccountBalance(acct.ID, acct.Currency, allLegs)`** (the canonical fn — it filters the full leg slice by account id; O(accounts×txns) is fine at single-user volume). Classify: `cash`/`investment` → the balance is an **asset** → `Cash`; `credit` → **liability** → `domain.AmountOwed(balance)` (positive magnitude) → `Liabilities`.
    6. For each **investment** account: `holdings := domain.DeriveHoldings(acct.Currency, eventsForAcct)`. For every holding sum its `RealizedGain` (incl. closed qty==0 positions) into an all-realized slice; for each **active** (qty>0) holding resolve price from `prices` and, when present, `market, unreal := domain.ValueHolding(h, price)` → append `market` to `Holdings` (for Net Worth) and build a `HoldingValuation` row; when no price set `HasPrice=false`, add the symbol to `Unpriced`, and **do not** add it to the `Holdings` slice (an unpriced holding contributes 0 to Portfolio value — it cannot be valued without a price, AD-6). Surface `domain.ErrOversold` as a typed error the handler can warn on (same as 4.2/4.3).
    7. Build the `rates` map: for each distinct native currency `C` present across `Cash`/`Liabilities`/`Holdings` with `C != display`, `r, err := store.RateEffectiveAt(C, display, today)`; on success `rates[C]=r`; on `pgx.ErrNoRows` leave it absent (→ `domain.NetWorth` reports it in `Missing`, Q5). **Never invert** a `display→C` rate (AD-6) — look up the exact `C→display` direction only.
    8. `v := domain.NetWorth(display, ValuationInput{Cash, Liabilities, Holdings}, rates)`.
    9. `RealizedByCurrency := domain.SumByCurrency(allRealized)` (reuse the existing canonical aggregation — AD-10).
    10. Return `Portfolio{Holdings, v.PortfolioValue, v.NetWorth, RealizedByCurrency, v.Missing, Unpriced, display}`.
  - [x] DB-gated test `valuation_test.go`: a BRL investment account (buy → priced holding) + a USD investment account (buy → priced holding) + a credit account with a balance owed; Display = BRL; add a `USD→BRL` rate. Assert: `PortfolioValue` = BRL holdings + converted USD holdings (convert-then-sum), `NetWorth` = cash + holdings − owed, `Missing` empty. Then **delete/omit the USD→BRL rate** → `Missing == [USD]`, the BRL part still totals (partial Net Worth), USD excluded. An **archived** account's balance is **excluded** (use `SetArchived`, re-run, assert it drops out). `RealizedByCurrency` groups realized G/L by currency. An unpriced holding appears in `Unpriced` and contributes 0 to `PortfolioValue`.

- [x] **Task 3 — `/investments` portfolio page + wiring (AC: #1, #2, #3)**
  - [x] Add a `Valuation` interface to `internal/http/router.go` (alongside the others): `Portfolio(ctx) (valuation.Portfolio, error)`. Add a `Valuation Valuation` field to `Deps`; wire `valuation.New(pool)` in `cmd/server/main.go` (`http → service` import, AD-1).
  - [x] Replace the `/investments` `ComingSoon` route (router.go:180) with a real handler `investmentsPage(deps)` → calls `deps.Valuation.Portfolio`, maps to view structs, renders `web.InvestmentsPage`. Keep the active-nav key `"investments"` and the five-item nav unchanged. On `Portfolio` error: if `errors.Is(err, transaction-style ErrOversold)` (re-exported or matched), show the oversold warning; otherwise render a graceful error message (consistent with the project-wide render-error pattern — do NOT start the project-wide swallow fix here, but a top-level page must not 500 silently: render an error banner).
  - [x] Add `web.InvestmentsPage(data ShellData, p InvestmentsView)` templ in `web/pages.templ` + view structs in `web/shell.go`:
    - A **Net Worth** hero (large number, Display Currency) + **Portfolio value** + **cumulative realized G/L** (one chip per native currency from `RealizedByCurrency`, gain-green / loss-red / zero-neutral — reuse the 4.3 colour convention).
    - A **missing-rate warning** banner when `Missing` is non-empty: e.g. *"Net worth excludes USD — no USD→BRL rate. [Add a rate](/exchange-rates)"* (list each missing code; link to `/exchange-rates`). This realizes the FR-2 prompt in the Net-Worth context (Q5).
    - An **unpriced** note when `Unpriced` is non-empty: *"No price for VOO, PETR4 — excluded from portfolio value. [Add prices](/prices)"*.
    - A **holdings table across accounts**: Account · Security · Quantity · Price (as of {date}, or "—") · Market value · Cost basis · Unrealized G/L — all in each holding's **native** currency (per-holding figures are single-currency; only the Net Worth / Portfolio totals are converted). Empty state when there are no holdings.
    - All money pre-formatted in the handler via `money.Money.String()` (the layer does no math, AD-10/AD-1). The unrealized-gain colour flags are computed in the handler exactly like 4.3 (`UnrealizedPositive`/`UnrealizedNegative` from the rounded amount).
  - [x] `make generate css` after the `.templ` edits; commit `web/pages_templ.go` + `web/static/css/app.css`.

- [x] **Task 4 — Tests, verify, docs (AC: all)**
  - [x] `internal/http/router_test.go`: add a stub `Valuation` (returns a canned `valuation.Portfolio`) and register it in `testDeps`. Tests: unauth `GET /investments` → 302 `/login`; authed GET shows the Net Worth + Portfolio value + a holding row; with a non-empty `Missing` the warning banner + `/exchange-rates` link render; with `Unpriced` the unpriced note renders.
  - [x] `GOTOOLCHAIN=local go build ./... && go vet ./... && go test ./...` green (DB-gated tests skip without a DB). **`make nofloat` stays green** — all valuation/conversion math is `shopspring/decimal`/`money` in `domain`/`service`. `gofmt -l` clean.
  - [x] Live smoke (compose db :5433 + run, owner/financas): set Display = BRL; with the 4.3 smoke data (a BRL holding priced) plus a USD investment account holding a priced USD security and a `USD→BRL` rate, open **`/investments`** ⇒ Net Worth + Portfolio value in BRL (USD holding converted), per-holding rows in native currency, cumulative realized per currency. **Remove the USD→BRL rate** ⇒ the page still renders with a **partial Net Worth** + a "excludes USD" warning linking `/exchange-rates` (never blocked, Q5). **Archive** an account ⇒ it drops out of Net Worth. A holding with no price ⇒ listed under the unpriced note, excluded from Portfolio value.
  - [x] Update `README.md` (portfolio & net worth: `/investments` shows Net Worth + Portfolio value in the Display Currency via convert-then-sum (banker's, AD-12); per-holding valuation in native currency; cumulative realized G/L per currency; archived accounts excluded; a missing FX rate yields a partial total + a prompt to add the rate, never a guess; the KPI dashboard/chart/allocation come in Epic 5).

## Dev Notes

### Scope boundary — what this story does NOT do (read first)

- **NO KPI-card dashboard, trend chart, allocation, or insight card** — all **Epic 5**. Story 5.2's KPI cards will **reuse this story's `domain.NetWorth`** (do not duplicate the aggregation there); 5.3 is value-over-time (AD-11), 5.4 is allocation. This story renders a functional `/investments` portfolio page, not the visual hero dashboard.
- **NO per-sell-date conversion of cumulative realized G/L** (decision below) — realized G/L is shown **per native currency** (no FX), honoring AD-12's "never rebased at today's rate" by simply not converting. A single Display-Currency realized figure (each Sell converted at its effective-date rate, AD-12) is deferred; revisit in Epic 5 if the dashboard needs one number.
- **NO new migration / NO sqlc change** — every read reuses an existing query (`GetDisplayCurrency`, `ListActiveAccounts`, `ListTransactions`, `LatestPrices`, `ListSecurities`, `RateEffectiveAt`). If you find yourself writing SQL, stop — the data is already queryable.
- **NO writes** — valuation is pure read/derive (AD-2). No new `Add`/edit anywhere. Prices are entered in 4.3 (`/prices`), rates in 2.2 (`/exchange-rates`).
- **NO rate inversion / NO guessed rate** — a missing `native→Display` rate yields a partial total + warning (Q5/AD-6), never `1/rate` or a `Display→native` inversion.

### Decided for Epic 4 (apply here) — see memory `financas-epic4-decisions`

[Source: [[financas-epic4-decisions]]]

- **Q5 — Net Worth with a missing rate:** show a **partial** Net Worth (only the convertible currencies) plus a warning like *"excludes USD (no rate) — enter a rate"*; never block, never invert/guess (AD-6). Convert-then-sum (AD-12) via `RateEffectiveAt` + `money.Convert`. → `domain.NetWorth` returns `Missing []Currency`; the `/investments` page renders the warning + a `/exchange-rates` link.
- **Q1 same-currency-only** (4.2): a holding's price, cash, and basis are one currency, so **per-holding** valuation is single-currency (no FX). FX happens **only** in the cross-account `domain.NetWorth` aggregation here.
- **Cumulative realized G/L — owner decision (2026-06-29):** show **per native currency** (no FX) for 4.4; full AD-12 per-sell-date Display-Currency conversion deferred to Epic 5.

### Architecture invariants this story must honor

- **AD-10 — one canonical home.** `domain.NetWorth` is the **single** function for Portfolio total + Net Worth; `service/valuation` loads inputs and calls it; `http` only renders. Per-holding market value/unrealized reuse the existing `domain.ValueHolding` (4.3); per-currency realized reuses `domain.SumByCurrency`; balances reuse `domain.AccountBalance`/`AmountOwed`. **Do not re-implement any of these.** [Source: ARCHITECTURE-SPINE.md#AD-10]
- **AD-12 — convert-then-sum, banker's, round once.** Convert each native amount via `money.Convert` (full precision, no round), sum, then `money.New(sum, display).Rounded()` once at the boundary. Same order everywhere. [Source: #AD-12; internal/money/convert.go]
- **AD-5 / AD-6 — store native, convert only at read; owner-entered directional rates, no inversion/feed.** Conversion happens only in the `domain` projection here; `RateEffectiveAt(C, display, today)` is the exact direction; a missing pair → `Missing` (the FR-2 prompt), never `1/rate`. [Source: #AD-5, #AD-6]
- **AD-2 — derived on read.** Net Worth / Portfolio / holdings / balances are all derived on read from the ledger + prices + rates; nothing is stored. Archived accounts are excluded by reading `ListActiveAccounts`. [Source: #AD-2]
- **AD-1 — layering.** `domain.NetWorth` imports only `money`; `service/valuation` reads via `store` (accounts, ledger, prices, securities, rates, settings — **never** `service/account`/`transaction`/`price`/`exchangerate`/`settings`; store-not-service, exactly as 4.2/4.3 read securities/prices via `store`); `http` defines the `Valuation` interface and renders; `cmd/server` injects. [Source: #AD-1]
- **Capability-map note:** the spine names `service/valuation` for FR-10 — this story creates it (the first service that reads *across* accounts/entities). It deliberately re-derives balances/holdings via `domain` from `store` rows rather than calling `service/transaction`, keeping the single-direction import rule (a documented, intentional re-load — the alternative, service→service, is forbidden by AD-1). [Source: #Capability → Architecture Map]

### Previous-story intelligence (4.1 + 4.2 + 4.3 + 2.x) — load-bearing

[Source: 4-3-manual-security-prices.md; 4-2-investment-transactions-derived-holdings.md; 2-2-exchange-rates.md; [[financas-epic1-progress]]; [[financas-epic4-decisions]]]

- **Reuse, don't rebuild.** `domain.ValueHolding(h, price) → (market, unrealized)` (4.3, native, rounds once), `domain.DeriveHoldings(currency, events)` (4.2, average-cost, `ErrOversold`, incl. zero-qty for realized), `domain.AccountBalance`/`AmountOwed` (balances), `domain.SumByCurrency` (per-currency totals), `money.Convert(amount, rate, target)` (full precision, no round), `money.New(...).Rounded()` (banker's at scale 4). `domain.NetWorth` is the ONLY new domain function. [Source: internal/domain/valuation.go, holding.go, balance.go, sum.go; internal/money/convert.go]
- **The holdings read pattern is established in `service/transaction.deriveHoldings`/`Holdings` (4.2/4.3):** load `ListAccountTransactions` (DESC), reverse to chronological ASC, build `[]domain.TradeEvent`, call `DeriveHoldings`; value via `store.LatestPrices(time.Now())` + `domain.ValueHolding`. `service/valuation` does the same but **across all investment accounts** from the single `ListTransactions` read (bucket by account). Mirror it; don't import `service/transaction`. [Source: internal/service/transaction/transaction.go:469–548]
- **`store.LatestPrices` takes `$1::date`** (4.3 review fix) — pass `time.Now()` (cast to today's date server-side; tz-stable). [Source: db/query/price.sql]
- **`store.RateEffectiveAt`** (2.2): `RateEffectiveAtParams{FromCurrency, ToCurrency, EffectiveDate}` → `(decimal.Decimal, error)`, `pgx.ErrNoRows` when none — the no-inversion `ErrNoRate` path. Look up `C→display` only. [Source: internal/service/exchangerate/exchangerate.go RateAt; db/query/exchange_rate.sql]
- **`/investments` is currently `web.ComingSoon` (router.go:180)** — replace it. The 4.1 notes explicitly reserved the Investments area for "4.2 (holdings) and 4.4 (portfolio/net worth)". Five-item nav unchanged; `shellData(deps, ctx, "investments")`. [Source: internal/http/router.go:180; 4-1-manage-securities.md]
- **`store.ListActiveAccounts` excludes archived** (the AC2 "excluding archived" requirement is satisfied purely by choosing this query over `ListAllAccounts`). [Source: internal/service/account/account.go:178; db/query/account.sql]
- **Display Currency** via `store.GetDisplayCurrency` (settings). The shell header already shows it; the page totals must be in it. [Source: internal/service/settings/settings.go]
- **Colour convention (4.3):** gain → `text-gain` (green), loss → `text-loss` (red), zero → neutral, via `UnrealizedPositive`/`UnrealizedNegative` bools computed in the handler from the rounded amount (no `strings` in templ). Reuse for unrealized G/L and per-currency realized chips. [Source: web/pages.templ InvestmentAccountDetailPage; internal/http/router.go renderInvestmentDetail]
- **Project-wide deferrals (do NOT fix here):** the render-error swallow + raw `err.Error()` echo are tracked in `deferred-work.md`. BUT `/investments` is a top-level page: on a `Portfolio` error, render a graceful error banner rather than a blank 200 or a 500 — do not silently swallow a page-level failure (a light, local handling, not the project-wide refactor). [Source: _bmad-output/implementation-artifacts/deferred-work.md]
- **Environment:** build/test `GOTOOLCHAIN=local`; local DB host **5433** (`docker compose up -d db`), DB at migration version 11 (no new migration this story); DB-gated tests skip without `DATABASE_URL`/`TEST_DATABASE_URL`; dev login `owner`/`financas`; `make generate css` after `.templ`/CSS; `make nofloat` must stay green. **Commit + push to `main` when done** (one commit per story, trunk-based — owner's standing instruction). `baseline_commit` is the real HEAD `9f9ced7`.

### Project Structure Notes

New: `internal/domain/networth.go` (+ `networth_test.go`); `internal/service/valuation/valuation.go` (+ `valuation_test.go`); `web.InvestmentsPage` (regenerated `web/pages_templ.go`) + view structs in `web/shell.go`.
Modified: `internal/http/router.go` (`Valuation` iface + `Deps` field, `/investments` route now `investmentsPage(deps)` instead of `ComingSoon`, handler + view mapping) + `router_test.go` (stub `Valuation` + tests); `cmd/server/main.go` (wire `valuation.New(pool)`); `web/pages.templ` (`InvestmentsPage`) + rebuilt `app.css`; `web/shell.go` (`InvestmentsView` + `PortfolioHoldingRow` + realized-chip structs); `README.md`. No migration, no `db/query` change, no sqlc regen — `service/valuation` is a new sibling package that reads existing queries and composes existing `domain` derivations.

### Testing standards

- `domain` (pure unit): `NetWorth` — convert-then-sum vs sum-then-convert ordering, round-once, liability subtraction, missing-rate partial + `Missing` list, zero-amount-unrated not flagged, all-same-currency no-rate path.
- `service/valuation` (DB-gated): multi-currency Net Worth/Portfolio with a rate; missing-rate partial; archived exclusion; per-currency realized; unpriced holding excluded from Portfolio value.
- `http` (stub-backed): `/investments` auth-gating; renders Net Worth + Portfolio + a holding row; missing-rate warning + `/exchange-rates` link; unpriced note.
- `go test ./...` green with no DB; `go vet` + `make nofloat` clean.

### References

- [Source: epics.md#Story 4.4] — ACs; [Source: epics.md FR-10] — per-holding valuation + Gain/Loss, Portfolio total + Net Worth in Display Currency, cumulative realized
- [Source: ARCHITECTURE-SPINE.md#AD-10] — one canonical home (`NetWorth`); [#AD-12] — convert-then-sum, banker's, round once; [#AD-5/#AD-6] — native storage, directional rates no inversion; [#AD-2] — derived on read (archived excluded); [#AD-1] — layering / store-not-service; [#Capability → Architecture Map] — `service/valuation`
- [Source: financas-epic4-decisions] — Q5 (missing-rate partial + warning) and the cumulative-realized-per-currency decision
- [Source: internal/domain/valuation.go, holding.go, balance.go, sum.go] — the derivations to reuse; [internal/money/convert.go] — `Convert`
- [Source: internal/service/transaction/transaction.go] — the holdings load/derive/value read pattern to mirror (do not import)
- [Source: db/query/{account,transaction,exchange_rate,price}.sql] — the existing queries this story composes
- [Source: 4-3-manual-security-prices.md] — `ValueHolding`, `LatestPrices($1::date)`, colour convention, store-not-service

## Dev Agent Record

### Agent Model Used

claude-opus-4-8[1m] (Opus 4.8, 1M context) — bmad-dev-story workflow.

### Debug Log References

- **Cross-account DB-gated test isolation.** `service/valuation.Portfolio` aggregates across EVERY active account/security/rate AND reads the global Display-Currency singleton. Running it against the shared dev/CI DB (a) saw leftover rows from prior runs and (b) raced the `settings` test on the `app_settings.display_currency` row, because `go test` runs distinct package test binaries in parallel. First attempt (TRUNCATE + restore display currency) fixed (a) but not the concurrent-writer race (b). Final fix: the valuation integration test provisions a **private throwaway database** (`isolatedDB` → `CREATE DATABASE` … migrate … `DROP DATABASE … WITH (FORCE)` on cleanup), giving the cross-account test a pristine, uncontended schema. Verified deterministic in both `settings,valuation` and `valuation,settings` orderings (`-count=1`).
- **Pre-existing, out of scope:** `internal/domain/balance_test.go` is flagged by go1.26's `gofmt` (trailing-comment alignment) in HEAD already — untouched here. A stale pre-existing server instance was occupying the default port during smoke; ran the freshly-built binary on a free port instead.

### Completion Notes List

- **`domain.NetWorth` (new, the only new domain fn):** single canonical home for Portfolio total + Net Worth; convert-then-sum at full precision via `money.Convert`, banker's round-once at the boundary; `Missing` is deduped, sorted, and records only NON-ZERO skipped (unrated) currencies (a zero unrated balance never raises a spurious warning). Reuses `money` only (AD-1).
- **`service/valuation` (new sibling package):** reads everything via `store` (accounts, ledger, prices, securities, rates, settings — store-not-service, AD-1); re-derives balances/holdings via `domain` (`AccountBalance`/`AmountOwed`/`DeriveHoldings`/`ValueHolding`/`SumByCurrency`) rather than importing `service/transaction`; never inverts a rate (looks up `C→display` only, missing → `Missing`); archived accounts excluded by reading `ListActiveAccounts`; unpriced active holdings excluded from Portfolio value and surfaced in `Unpriced`; `ErrOversold` re-exported for the handler. No new migration / sqlc change.
- **`/investments` page (was `ComingSoon` since 1.4):** real handler `investmentsPage` → `web.InvestmentsPage`. Net Worth hero + Portfolio value (Display Currency) + per-currency realized-G/L chips (4.3 colour convention), a partial-total missing-rate warning linking `/exchange-rates`, an unpriced note linking `/prices`, and a cross-account native-currency holdings table. All money pre-formatted in the handler (no math in the view, AD-10/AD-1); a page-level load failure renders a graceful banner (oversold gets a specific hint) instead of a blank/silent 500. Five-item nav and the `"investments"` active key unchanged. Wired `valuation.New(pool)` in `cmd/server/main.go`.
- **Verification:** `go build ./...`, `go vet ./...`, `go test -count=1 ./...` (DB-gated incl. valuation) all green; `make nofloat` green; `gofmt -l` clean on all touched files. Updated `TestNavTargetAuthed` (no longer "Coming soon") and added `TestInvestmentsPageRendersPortfolio` + `TestInvestmentsPageWarnings`.
- **Live smoke (freshly-built binary, owner/financas, Display = BRL):** seeded a BRL priced holding + a USD priced holding + an unpriced BRL holding + a BRL credit liability. (1) No USD→BRL rate ⇒ Portfolio 660 BRL, Net Worth −310 BRL (partial), "excludes USD … no rate to BRL" + `/exchange-rates`, NOPX unpriced note + `/prices`, realized 80 BRL / 0 USD chips, native per-holding figures. (2) Add USD→BRL = 5 ⇒ Portfolio 1410 BRL (convert-then-sum), Net Worth −60 BRL, warning gone. (3) Archive the USD account ⇒ USD/VOO dropped, Portfolio 660 BRL, Net Worth −310 BRL. (Note: the DB-isolation test work and the smoke seed overwrote the prior 4.3 dev-DB smoke data; re-seed locally if needed.)

### File List

- `internal/domain/networth.go` (new)
- `internal/domain/networth_test.go` (new)
- `internal/service/valuation/valuation.go` (new)
- `internal/service/valuation/valuation_test.go` (new)
- `internal/http/router.go` (modified — `Valuation` iface + `Deps` field, `/investments` route + `investmentsPage` handler, valuation import)
- `internal/http/router_test.go` (modified — `stubValuation` + `cannedPortfolio`, registered in `testDeps`, updated/added handler tests)
- `cmd/server/main.go` (modified — wire `valuation.New(pool)`)
- `web/pages.templ` (modified — `InvestmentsPage`)
- `web/pages_templ.go` (regenerated)
- `web/shell.go` (modified — `InvestmentsView`, `RealizedChip`, `PortfolioHoldingRow`)
- `web/static/css/app.css` (rebuilt)
- `README.md` (modified — Portfolio & Net Worth section)

## Change Log

| Date | Change |
| --- | --- |
| 2026-06-29 | Story 4.4 implemented (dev-story): `domain.NetWorth` (convert-then-sum, banker's round-once, deduped/sorted `Missing` for non-zero unrated currencies) + pure unit tests; new `service/valuation` orchestrating cross-account reads via `store`/`domain` (archived excluded, no rate inversion, unpriced excluded, realized per native currency) + isolated-DB integration test; `/investments` page (was `ComingSoon`) → portfolio view with Net Worth + Portfolio value (Display Currency), per-holding native rows, realized-G/L chips, missing-rate + unpriced notices, graceful page-error banner; wired `valuation.New(pool)`. README updated. Build/vet/test/nofloat/gofmt green; live smoke confirmed partial-then-full-then-archived states. Status → review. |
| 2026-06-29 | Story 4.4 drafted (create-story): single canonical `domain.NetWorth` (Portfolio total + Net Worth, convert-then-sum/banker's, missing-rate `Missing` list per Q5); new `service/valuation` orchestrating cross-account reads (accounts, ledger, derived holdings, latest prices, FX rates, Display Currency) via `store` + `domain`; `/investments` page (was `ComingSoon`) → portfolio view with Net Worth + Portfolio value (Display Currency), per-holding native valuation, cumulative realized G/L per currency, and a partial-total + missing-rate warning. Reuses `ValueHolding`/`DeriveHoldings`/`AccountBalance`/`AmountOwed`/`SumByCurrency`/`money.Convert` — only `NetWorth` is new. No migration/sqlc change. Owner decision: cumulative realized per native currency (no FX); per-sell-date AD-12 conversion deferred to Epic 5. Status → ready-for-dev. |
