---
baseline_commit: 8f4cbb1
---

# Story 4.3: Manual security prices

Status: review

<!-- Note: Validation is optional. Run validate-create-story for quality check before dev-story. -->

## Story

As the owner,
I want to enter prices for my securities,
so that valuations reflect what I believe they're worth.

## Acceptance Criteria

From `epics.md` → Epic 4 → Story 4.3 (realizes FR-9). **Given** a Security exists, **When** I enter a Price with an effective date, **Then**:

1. It is stored as an **append-only, effective-dated** row and **re-values affected Holdings** (FR-9, AD-6).
2. The **most recent Price** (effective ≤ today) is used for current Valuation and **its date/staleness is visible**.
3. There is **no online or automated price fetch** anywhere — owner-entered only.

> **Scope (this story):** add the `price` entity (append-only, effective-dated, per security — the exact analog of `exchange_rate` from 2.2), `db/query/price.sql`, a `service/price` use-case (`Add`/`PriceAt`/`LatestPrices`/`List`, mirroring `service/exchangerate`), a `/prices` management page linked from `/settings`, the **single canonical `domain.ValueHolding`** (market value + unrealized gain, AD-10), and the **per-holding market value / unrealized G/L columns on the investment account-detail page** (the figures 4.2 deferred because no `Price` existed). All valuation here is in the **holding's own native currency** — same-currency-only means a security's price, its account's cash, and its cost basis are all one currency, so **NO FX/conversion happens in this story**. **NOT in this story:** the Display-Currency aggregation — Portfolio total, **Net Worth**, cumulative cross-currency totals, and the missing-FX-rate prompt — all **Story 4.4** (`domain` convert-then-sum, AD-12); the dashboard (Epic 5); value-over-time (AD-11, Story 5.3); editing/deleting a price (append-only — corrections are new effective-dated rows, exactly like exchange rates).

## Tasks / Subtasks

- [x] **Task 1 — `price` schema (append-only, effective-dated, per security) (AC: #1)**
  - [x] Add goose migration `db/migrations/00011_prices.sql`, **mirroring `00003_exchange_rates.sql`** (plain statements, no `StatementBegin/End` needed — pure DDL). Up:
    ```sql
    CREATE TABLE price (
        id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
        security_id    BIGINT NOT NULL REFERENCES security (id),
        effective_date DATE NOT NULL,
        price          NUMERIC(19, 4) NOT NULL CHECK (price > 0),
        created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
    );
    CREATE INDEX price_security_effective ON price (security_id, effective_date DESC);
    ```
    Down: `DROP TABLE price;` (the index drops with it).
  - [x] **Directional analog of `exchange_rate`:** a row means "1 unit of `security_id` was worth `price` units of the security's quote currency as of `effective_date`". Rows are only **inserted** (never updated/deleted in normal flow — corrections are new rows). `price NUMERIC(19,4)` = the money/Price scale per conventions (NOT the `(18,8)` rate scale). **No currency column** — the price is implicitly in the security's `quote_currency` (AD-5, store native). bigint identity PK; `DATE` effective date, `timestamptz` `created_at`.

- [x] **Task 2 — sqlc: price queries + generate (AC: #1, #2)**
  - [x] Add `db/query/price.sql` (mirror `exchange_rate.sql`):
    - `AddPrice :one` — `INSERT INTO price (security_id, effective_date, price) VALUES ($1,$2,$3) RETURNING id, security_id, effective_date, price, created_at;`
    - `PriceEffectiveAt :one` — `SELECT price FROM price WHERE security_id = $1 AND effective_date <= $2 ORDER BY effective_date DESC, id DESC LIMIT 1;` (the latest price on/before a date — analog of `RateEffectiveAt`).
    - `LatestPrices :many` — newest effective price per security in **one** query for the holdings read: `SELECT DISTINCT ON (security_id) security_id, effective_date, price FROM price WHERE effective_date <= $1 ORDER BY security_id, effective_date DESC, id DESC;` (a bespoke `LatestPricesRow` is **expected and fine** — this is a brand-new query; the column-order hazard only applies to the shared `store.Transaction` full-row queries, which this story does **not** touch).
    - `ListPrices :many` — all rows newest-first **joined to the security symbol** for the management page: `SELECT p.id, p.security_id, s.symbol, p.effective_date, p.price, p.created_at FROM price p JOIN security s ON s.id = p.security_id ORDER BY p.effective_date DESC, p.id DESC;` (bespoke `ListPricesRow` expected/fine — same reasoning).
  - [x] Run `make sqlc` (pinned `sqlc/sqlc:1.27.0` Docker image — **NOT** `go run`). Confirm `price`/`effective_date` generate as `decimal.Decimal`/`time.Time` (the project-wide overrides from 2.2 already cover `numeric → decimal.Decimal`, `date → time.Time`; `created_at` stays `pgtype.Timestamptz` — read `.Time` in the service, as `exchangerate` does). **`transaction` queries are untouched, so `store.Transaction` is unaffected.** Commit the generated `internal/store/price.sql.go` (+ regenerated `models.go`/`querier.go`).

- [x] **Task 3 — `service/price` use-case (AC: #1, #2, #3)**
  - [x] Add `internal/service/price/price.go`, **mirroring `service/exchangerate`** (package doc in the same house style: owner-entered, effective-dated, append-only; one tx per write AD-3; no external feed AD-6):
    - A `Price` struct: `ID int64`, `SecurityID int64`, `Symbol string`, `EffectiveDate time.Time`, `Price decimal.Decimal`, `CreatedAt time.Time`.
    - A `PricePoint` struct for valuation: `Price decimal.Decimal`, `EffectiveDate time.Time` (the latest price + its date, for staleness).
    - `New(pool *pgxpool.Pool) *Service`.
    - `Add(ctx, securityID int64, effective time.Time, price decimal.Decimal) (Price, error)`: validate the security exists via **`store.GetSecurity`** (`pgx.ErrNoRows` → `ErrSecurityNotFound`; store-not-service rule, like the trade validation in 4.2), `price.IsPositive()` (`ErrNonPositivePrice`). Insert in **one** `pool.Begin` tx (AD-3). Append-only — no update path. Resolve `Symbol` from the loaded security for the returned struct (the `AddPrice` RETURNING has no symbol).
    - `PriceAt(ctx, securityID int64, date time.Time) (decimal.Decimal, error)`: latest price effective ≤ `date`; `pgx.ErrNoRows` → **`ErrNoPrice`** (the analog of `exchangerate.ErrNoRate` — never guess/fabricate a price). "Latest for now" = `PriceAt(securityID, today)`. (Provided for symmetry and for 4.4/5.3; `Holdings` uses `LatestPrices` for the one-query path.)
    - `LatestPrices(ctx, asOf time.Time) (map[int64]PricePoint, error)`: one `store.LatestPrices` query → `map[securityID]PricePoint`. The investment-holdings read consumes this so it does NOT issue one query per holding.
    - `List(ctx) ([]Price, error)`: all prices newest-first (via `store.ListPrices`, with symbol), for the management page.
    - Typed errors: `ErrNoPrice`, `ErrSecurityNotFound`, `ErrNonPositivePrice`.
  - [x] DB-gated test `price_test.go` (skips without `DATABASE_URL`/`TEST_DATABASE_URL`): create a security; add prices @ two effective dates (e.g. 2024-01-01 = 10.00, 2024-06-01 = 12.00); `PriceAt(date)` selects the effective-at row (mid-range → 10.00, after → 12.00, before-first → `ErrNoPrice`); `LatestPrices(today)` returns the 12.00 point with its effective date; `Add` on a missing security → `ErrSecurityNotFound`; non-positive price → `ErrNonPositivePrice`; `List` returns rows newest-first with the symbol.

- [x] **Task 4 — `domain.ValueHolding`: the single canonical valuation function (AC: #1, #2)**
  - [x] Add `internal/domain/valuation.go` (or append to `holding.go`): the **one** pure function that values a derived `Holding` at a price (AD-10):
    ```go
    // ValueHolding returns the market value (quantity × price) and unrealized
    // Gain/Loss (market value − cost basis) of a holding, both in the holding's
    // NATIVE currency. Same-currency-only (Epic-4 decision) means the price is
    // already in the holding's currency — there is NO FX here; Display-Currency
    // convert-then-sum aggregation is Story 4.4 (AD-12). Rounding to the money
    // scale happens once, at this display boundary (AD-12).
    func ValueHolding(h Holding, price decimal.Decimal) (marketValue, unrealizedGain money.Money)
    ```
    - `marketValue = money.New(h.Quantity.Mul(price), h.CostBasis.Currency()).Rounded()`.
    - `unrealizedGain = money.New(marketValue.Amount().Sub(h.CostBasis.Amount()), h.CostBasis.Currency())` — compute on raw amounts in the same currency (the cost basis is already at money scale; market value is rounded), so no `Money.Sub` currency-mismatch path is needed.
  - [x] Pure unit test in `internal/domain/valuation_test.go` (no DB): a holding of qty 150 / cost basis 1653.75 at price 16.00 → market value 2400.00, unrealized +746.25; price below avg cost → negative unrealized; a fractional quantity × price rounds **once** to money scale (banker's). (Reuse the `dec` helper already in `balance_test.go` — do NOT redeclare it, per the 4.2 build-error lesson.)

- [x] **Task 5 — Surface market value + unrealized G/L on the investment account-detail (AC: #1, #2)**
  - [x] Extend `service/transaction.HoldingView` (it is the display projection `http` already consumes) with the price-dependent fields: `HasPrice bool`, `Price money.Money` (latest price, native), `PriceDate time.Time` (effective date of that price — staleness), `MarketValue money.Money`, `UnrealizedGain money.Money`. (Signature of `Holdings(...)` is **unchanged** — it already returns `[]HoldingView` — so the `http` `Transactions` interface does not change.)
  - [x] In `service/transaction.Holdings`: after `deriveHoldings`, load latest prices via **`store.LatestPrices` read directly from `store`** (store-not-service — `transaction` must NOT import `service/price`; AD-1, exactly as it reads securities via `store.ListSecurities`, never `service/security`). For each active holding: if a `PricePoint` exists, set `HasPrice=true`, `Price`, `PriceDate`, and call `domain.ValueHolding(h.Holding-equivalent, point.Price)` for `MarketValue`/`UnrealizedGain`; if no price, leave `HasPrice=false` (the page renders "—"). **Note:** `Holdings` currently builds `HoldingView` from `domain.Holding` — pass the `domain.Holding` (qty + `CostBasis money.Money`) into `domain.ValueHolding`. Keep using `store.New(s.pool)` (a read; no tx needed, as today).
  - [x] DB-gated additions to the trade/holdings test (`trade_test.go`): after a buy that establishes a holding, **with no price** → `HasPrice=false`; **after adding a price** → `MarketValue == qty×price`, `UnrealizedGain == marketValue − costBasis`, `PriceDate` is the price's effective date. Confirms AC#1 "re-values affected Holdings" is derived-on-read (no stored revaluation: the same holding gains a market value purely because a price row now exists).

- [x] **Task 6 — `/prices` management page + wiring (AC: #1, #2, #3)**
  - [x] Add a `Prices` interface to `internal/http/router.go` (alongside `ExchangeRates`): `Add(ctx, securityID int64, effective time.Time, price decimal.Decimal) (price.Price, error)`, `List(ctx) ([]price.Price, error)`. Add a `Prices Prices` field to `Deps`; wire `price.New(pool)` in `cmd/server/main.go` (`http → service` import allowed, AD-1; same shape as `ExchangeRates`).
  - [x] Register authenticated routes in the `pr` group (mirror `/exchange-rates`): `pr.Get("/prices", pricesPage(deps))` and `pr.Post("/prices", pricesCreate(deps))`. Add `pricesPage`/`pricesCreate` + a `renderPrices(deps, w, req, errMsg, code)` helper (mirror `renderExchangeRates`/`renderSecurities`): GET lists via `deps.Prices.List` + a security `<select>` (ALL securities, via `deps.Securities.List` — a price applies to any security, **not** filtered by currency). POST parses `security_id` (int), `effective_date` (`time.Parse("2006-01-02", v)` at UTC), and `price` as a **decimal string** via `decimal.NewFromString` (**never** a float, AD-4); calls `Add`; on parse/validation error re-render with the message + 400; on success redirect to `/prices` (303). (`renderPrices` may mirror the existing project-wide `if x, err := …; err == nil` swallow convention — the swallow fix is a tracked project-wide deferral, NOT this story; just be consistent with `renderExchangeRates`.)
  - [x] Add `web.PricesPage(data ShellData, rows []PriceRow, securities []SecurityChoice, errMsg string)` templ in `web/pages.templ` + a `web.PriceRow{Symbol, EffectiveDate, Price string}` view struct in `web/shell.go` (reuse the existing `SecurityChoice{ID, Symbol}` for the form `<select>`). Form: a security `<select>`, an effective-date input (`type=date`), and a price text input. The list shows symbol, effective date, price (newest-first). **Link `/prices` from `/settings`** ("Manage prices →", next to "Manage securities →"); the five-item primary nav (UX-DR1) is **unchanged**. Use `shellData(deps, req.Context(), "settings")` as the active-nav key.
  - [x] Extend `renderInvestmentDetail` (router.go ~999) to map the new `HoldingView` price fields into `web.HoldingRow`, and extend `web.HoldingRow` with `HasPrice bool`, `Price string`, `PriceDate string` (e.g. `"2024-06-01"`, blank when none), `MarketValue string`, `UnrealizedGain string`. In the templ holdings table (`InvestmentAccountDetailPage`) add columns **Price (as of {date})**, **Market value**, **Unrealized G/L**; render **"—"** when `!HasPrice`. (Unrealized G/L uses the same green/positive · red/negative semantics as the rest of the app, UX-DR7 — a `+`/`−` prefix is sufficient here; full token styling is Epic 5.) Keep the existing Quantity / Avg cost / Cost basis / Realized G/L columns.
  - [x] `make generate css` (templ + Tailwind) after the `.templ` edits; commit `web/pages_templ.go` + `web/static/css/app.css`.

- [x] **Task 7 — Tests, verify, docs (AC: all)**
  - [x] `internal/http/router_test.go`: add a stub `Prices` (in-memory `Add`/`List`, reject non-positive price + missing security like the real service) and register it in `testDeps`. Extend the stub `Transactions.Holdings` so a holding carries a market value once a price is present (so the investment-detail test can assert the new columns). Tests: unauth `GET /prices` → 302 `/login`; authed GET shows the form + any rows; authed POST with a valid price redirects (303) and the row appears; an invalid price (bad number / non-positive) re-renders with a message (400) and adds no row; on the investment account-detail page, a holding with a price shows its market value + unrealized G/L, and **without** a price shows "—".
  - [x] `GOTOOLCHAIN=local go build ./... && go vet ./... && go test ./...` green (DB-gated tests skip without a DB). **`make nofloat` stays green** — all price/valuation math is `shopspring/decimal`/`money` in `domain`/`service` (the form parses strings to decimal; no float anywhere, incl. the price round-trip). `gofmt -l` clean on new/edited files.
  - [x] Live smoke (compose db on :5433 + run server, login `owner`/`financas`): open `/prices` (via the Settings link); for a BRL security held in a BRL investment account (set up as in 4.2: e.g. qty 150 / cost basis 1653.75), add a price `16.00` effective today ⇒ it lists; open the investment account ⇒ the holding now shows **Market value 2400.00 BRL**, **Unrealized +746.25 BRL**, **Price 16.00 as of {today}**; add a **later** price `18.00` ⇒ the holding re-values to **2700.00** on reload (most-recent price wins, AC#2) and the older row remains (append-only history, AC#1); a holding with **no** price renders "—"; a non-positive price is rejected. Persistence across reload.
  - [x] Update `README.md` briefly (manual security prices: per-security, effective-dated, append-only, most-recent ≤ today used for valuation, staleness shown, no online/automated fetch; holdings now show market value + unrealized G/L in the account's native currency; Display-Currency Portfolio total / Net Worth come in 4.4).

## Dev Notes

### Scope boundary — what this story does NOT do (read first)

- **NO Display-Currency aggregation / NO Net Worth / NO Portfolio total** — there is **no FX conversion in this story**. Same-currency-only (Epic-4 decision Q1) means a security's price, its account's cash, and its cost basis are all one currency, so per-holding market value and unrealized G/L are computed and shown **in the holding's native currency only**. The canonical Display-Currency `domain` aggregation (convert-then-sum, banker's rounding, AD-12) and the **missing-FX-rate prompt** (Epic-4 decision Q5) are **Story 4.4**. Do not call `money.Convert` or `exchangerate` anywhere here.
- **NO dashboard / KPI cards / value-over-time** — Epic 5. Value-over-time (AD-11, no snapshot table) is Story 5.3.
- **NO edit/delete of a price** — append-only (AD-6), exactly like exchange rates (2.2). A correction is a **new** effective-dated row; the most recent ≤ today wins.
- **NO online / automated / scheduled / real-time price feed** — owner-entered only (AD-6, AC#3). Do not add any HTTP client, fetch job, or background poller.
- **NO change to the average-cost derivation** — Holdings (qty, cost basis, realized G/L) are unchanged from 4.2; this story only *values* them at a price. `domain.DeriveHoldings`/`BasisSold` are untouched.

### Architecture invariants this story must honor

- **AD-6 — owner-entered, effective-dated, append-only, no feed.** `price` is the exact analog of `exchange_rate`: append-only rows; reads select the row effective at (≤) the query date (latest for "now"); no external/online market-data client. A security with no price on/before the date → `ErrNoPrice` (the holding shows "—"), never a fabricated/guessed price. [Source: ARCHITECTURE-SPINE.md#AD-6; 2-2-exchange-rates.md]
- **AD-10 — one canonical home per derived figure.** `domain.ValueHolding` is the **single** function for market value + unrealized G/L. `service/transaction` loads inputs (holdings + latest prices) and calls it; `http` only renders. No re-derivation/re-rounding in `service` or templ. [Source: ARCHITECTURE-SPINE.md#AD-10]
- **AD-2 — derived on read.** Market value / unrealized G/L are **never stored**; "re-values affected Holdings" (AC#1) is automatic because they are computed on read from the price rows — adding a price row changes the next read, nothing is recomputed-and-saved. [Source: #AD-2]
- **AD-3 — one tx per use-case.** `price.Add` wraps its insert in one `pool.Begin` tx. The `Holdings` valuation path is a read (no tx, as today). [Source: #AD-3]
- **AD-4 — decimal, never float.** `price NUMERIC(19,4)` ↔ `decimal.Decimal` end to end (the 2.2 decimal↔NUMERIC plumbing already covers it); parse owner input with `decimal.NewFromString`; round once at the display boundary via `money` (`MoneyScale = 4`, banker's). `make nofloat` must stay green. [Source: #AD-4, #Consistency Conventions]
- **AD-5 — native currency.** A price has no currency column; it is implicitly the security's `quote_currency`. Nothing is converted here. [Source: #AD-5]
- **AD-1 — layering.** New `domain.ValueHolding` (imports only `money`); `service/price` reads/writes via `store` (`store.GetSecurity` for validation — no service→service); `service/transaction.Holdings` reads prices via **`store.LatestPrices`**, NOT `service/price` (store-not-service, the same rule that made 4.2 read securities via `store`); `http` defines the `Prices` interface and renders; `cmd/server` injects. [Source: #AD-1; 4-2-investment-transactions-derived-holdings.md]

### Decided for Epic 4 (apply here) — see memory `financas-epic4-decisions`

[Source: [[financas-epic4-decisions]]; 4-2 Dev Notes]

1. **Same-currency-only (confirmed)** → a holding's price, cash, and cost basis share one currency; **no FX in 4.3**. (The price-form security `<select>` is NOT currency-filtered — a price applies to any security; the *trade* form is the one filtered by currency, in 4.2.)
2–4. (Fees / oversell / zero-crossing — 4.2; not touched here.)
5. **Net-Worth-with-missing-FX-rate → partial total + warning** is **Story 4.4** (it is the cross-currency aggregation). 4.3's "no price" case is the simpler per-holding analog: show **"—"** for that holding's market value / unrealized G/L; do not block the page.

### Previous-story intelligence (2.2 + 4.1 + 4.2) — load-bearing

[Source: 2-2-exchange-rates.md; 4-1-manage-securities.md; 4-2-investment-transactions-derived-holdings.md; [[financas-epic1-progress]]; [[financas-epic4-decisions]]]

- **`exchange_rate` (2.2) is the precise template** for `price`: schema (`00003`), `db/query/exchange_rate.sql` (`AddExchangeRate`/`RateEffectiveAt`/`ListExchangeRates`), `service/exchangerate` (`Add`/`RateAt`+`ErrNoRate`/`List`, one tx, `toRate` mapper), and the `/exchange-rates` page linked from `/settings`. Copy this structure; rename rate→price, the directional from/to pair → a single `security_id`. [Source: internal/service/exchangerate/exchangerate.go, db/query/exchange_rate.sql]
- **decimal↔NUMERIC plumbing is already global (2.2):** `pgx-shopspring-decimal` registered per-connection in `store.NewPool`, and `sqlc.yaml` overrides `numeric → decimal.Decimal` + `date → time.Time`. `price`/`effective_date` will generate correctly with **no** new plumbing; `created_at` stays `pgtype.Timestamptz` (read `.Time`). [Source: 2-2 Dev Agent Record]
- **`store.GetSecurity`/`ListSecurities` exist (4.1)** — use them for price validation and the holdings price-join in memory; never `service/security`. [Source: 4-1; internal/service/security]
- **`HoldingView` already exists and explicitly reserves market value / unrealized gain for "4.3/4.4"** (see its doc comment) — this story fills exactly those fields. `Holdings` already loads the derived `domain.Holding` and the security meta map; add the latest-prices map alongside. [Source: internal/service/transaction/transaction.go:456–503]
- **`renderInvestmentDetail` (router.go ~999)** already builds `web.HoldingRow` per active holding and handles the `ErrOversold` banner — extend its `HoldingRow` mapping with the new price columns; do not restructure it. [Source: internal/http/router.go:999–1063]
- **`money` API:** `money.New(decimal, currency)`, `.Amount()`, `.Currency()`, `.Rounded()` (banker's to `MoneyScale=4`), `.String()` (`"2400.0000 BRL"`), `MoneyScale`. `money.Convert` exists but is **NOT used here** (no FX in 4.3). [Source: internal/money/money.go, convert.go]
- **sqlc:** new queries only (`price.sql`); `transaction` full-row queries are **untouched**, so the 3.4/3.6/4.2 "append the column to every full-row SELECT/RETURNING of `transaction`" column-order hazard does **not** apply. Bespoke `*Row` types for the new `LatestPrices`/`ListPrices` joins are expected and fine. [Source: 4-2 Dev Notes]
- **Project-wide deferrals (do NOT fix here):** the `if x, err := …; err == nil` swallow in render helpers and raw `err.Error()` echo to the client are tracked project-wide deferrals (from 4.1/4.2 reviews — `deferred-work.md`). Make `renderPrices` *consistent* with `renderExchangeRates`/`renderSecurities`; don't start the project-wide fix in this story. [Source: _bmad-output/implementation-artifacts/deferred-work.md]
- **Environment:** build/test `GOTOOLCHAIN=local`; `make sqlc` via the pinned Docker image (not `go run`); local DB host **5433** (`docker compose up -d db`), DB at migration version 10 → `make migrate` applies `00011` (→ 11); DB-gated tests skip without `DATABASE_URL`/`TEST_DATABASE_URL`; dev login `owner`/`financas`; `make generate css` after `.templ`/CSS; `make nofloat` must stay green. **Commit + push to `main` when done** (one commit per story, trunk-based — owner's standing instruction). `baseline_commit` is the real HEAD `8f4cbb1`.

### Project Structure Notes

New: `db/migrations/00011_prices.sql`; `db/query/price.sql` → regenerated `internal/store/price.sql.go` (+ `models.go`/`querier.go`); `internal/service/price/price.go` (+ `price_test.go`); `internal/domain/valuation.go` (+ `valuation_test.go`); `web.PricesPage` (regenerated `web/pages_templ.go`) + `web.PriceRow` in `web/shell.go`.
Modified: `internal/service/transaction/transaction.go` (`HoldingView` + price fields; `Holdings` loads `store.LatestPrices` and calls `domain.ValueHolding`) + `trade_test.go`; `internal/http/router.go` (`Prices` iface + `Deps` field, `/prices` GET/POST + `pricesPage`/`pricesCreate`/`renderPrices`, `renderInvestmentDetail` HoldingRow price mapping) + `router_test.go` (stub `Prices`, extended `Holdings` stub); `cmd/server/main.go` (wire `price.New(pool)`); `web/pages.templ` (`PricesPage` + Settings link + holdings-table price columns) + rebuilt `app.css`; `web/shell.go` (`PriceRow`, `HoldingRow` price fields); `README.md`. No structural variance — `service/price` is a new sibling package following the established onion layout; `/prices` is reference-data management linked from `/settings` like `/exchange-rates`.

### Testing standards

- `domain` (pure unit, no DB): `ValueHolding` — market value = qty×price, unrealized = market − cost basis (positive and negative), fractional qty×price rounds once to money scale (banker's).
- `service/price` (DB-gated): effective-at selection (mid/after/before-first → `ErrNoPrice`), `LatestPrices` latest-per-security with date, `Add` validation (`ErrSecurityNotFound`, `ErrNonPositivePrice`), `List` newest-first with symbol.
- `service/transaction` (DB-gated): a holding has no market value without a price (`HasPrice=false`); after a price is added it shows market value + unrealized G/L derived on read with the correct `PriceDate`.
- `http` (stub-backed): `/prices` auth-gating + list/add happy/sad paths; investment-detail holdings render price/market-value/unrealized columns, "—" when no price.
- `go test ./...` green with **no** DB (DB-gated tests skip); `go vet` + `make nofloat` clean.

### References

- [Source: epics.md#Story 4.3] — acceptance criteria; [Source: epics.md FR-9] — owner enters/updates prices manually, effective-dated; most recent used for valuation; staleness visible; no online/automated/real-time feed
- [Source: ARCHITECTURE-SPINE.md#AD-6] — owner-entered, effective-dated, append-only, reads select effective ≤ date, no feed; [#AD-10] — one canonical derived-figure home (`ValueHolding`); [#AD-2] — derived on read; [#AD-3 / #AD-4 / #AD-5 / #AD-1]; [#Consistency Conventions] — `NUMERIC(19,4)` Price scale, DATE/timestamptz, bigint identity
- [Source: 2-2-exchange-rates.md] — the exact append-only/effective-dated template (schema, query, service, page, decimal↔NUMERIC plumbing)
- [Source: 4-1-manage-securities.md] — `store.GetSecurity`/`ListSecurities`, `security.quote_currency`
- [Source: 4-2-investment-transactions-derived-holdings.md] — `HoldingView` (market value/unrealized reserved for 4.3), `Holdings`/`deriveHoldings`/`securityMeta`, `renderInvestmentDetail`, store-not-service rule, same-currency-only
- [Source: memory `financas-epic4-decisions`] — same-currency-only (no FX in 4.3); missing-FX-rate Net Worth is 4.4
- [Source: internal/money/money.go] — `New`/`Amount`/`Currency`/`Rounded`/`String`/`MoneyScale`
- [Source: _bmad-output/implementation-artifacts/deferred-work.md] — project-wide render-swallow / err-echo deferrals (do not fix here)

## Dev Agent Record

### Agent Model Used

claude-opus-4-8[1m] (Opus 4.8, 1M context)

### Debug Log References

- `make migrate` applied `00011_prices.sql` (goose → version 11). `make sqlc` (pinned Docker image) generated `store.Price`, `LatestPricesRow`, `ListPricesRow`, and the four query methods — **`internal/store/transaction.sql.go` was NOT regenerated** (the `transaction` full-row queries are untouched, so `store.Transaction` is unaffected; the 3.4/3.6/4.2 column-order hazard did not apply). `price`/`effective_date` generated as `decimal.Decimal`/`time.Time` via the existing 2.2 overrides; `created_at` stayed `pgtype.Timestamptz` (read `.Time` in the service).
- The templ holdings table needed a per-row gain/loss colour flag — `strings.HasPrefix` is not importable in `web/pages.templ` without an import block, so the loss flag is computed in the router (`HoldingRow.UnrealizedNegative = h.UnrealizedGain.Amount().IsNegative()`) and the templ uses `templ.KV` over that bool (matches how the register colours rows).
- `gofmt -w` applied to `transaction.go` + `web/shell.go` (struct-field alignment after adding the new fields). `internal/domain/balance_test.go` still shows under `gofmt -l` — pre-existing drift from 4.2, left scoped out (`make` does not gate gofmt).
- build / vet / `go test ./...` green with and without a DB; `make nofloat` green (all price/valuation math is `shopspring/decimal`/`money`).
- Live HTTP smoke (server :8097 + db :5433, owner/financas): created a BRL security + BRL investment account; **Buy 150 @ 11.025 → cost basis 1653.75**; before any price the holding's Price / Market value / Unrealized cells render **"—"**; **add price 16.00 @ 2026-06-29 → Market value 2400.00 BRL, Unrealized +746.25 BRL, "as of 2026-06-29"** (derived on read — nothing recomputed-and-stored); appending an earlier-dated price leaves the most-recent-effective (16.00) in force (latest-≤-today selection correct) and **both rows stay listed** (append-only history); a non-positive price POST → **400**.

### Completion Notes List

All three acceptance criteria verified (pure domain unit + live DB + live HTTP + stub-backed http):
- **AC1 — append-only, effective-dated, re-values holdings:** `price` (`00011`) is the per-security analog of `exchange_rate`; `service/price.Add` only inserts (one tx, AD-3). "Re-values affected holdings" is automatic — market value / unrealized G/L are **derived on read** (`TestHoldingValuation` proves a holding gains a market value purely because a price row was appended; nothing is stored/recomputed — AD-2).
- **AC2 — most-recent price + staleness visible:** `LatestPrices` (`DISTINCT ON (security_id) … effective_date DESC`) and `PriceEffectiveAt` select the latest row ≤ the date; the holdings table shows the price used and its effective date ("as of {date}").
- **AC3 — no online/automated fetch:** owner-entered only; a missing price returns `ErrNoPrice` (never a guessed price) and the holding shows "—". No HTTP/fetch client anywhere.

Decisions / variances (intentional, documented):
- **One canonical `domain.ValueHolding`** (AD-10) for market value + unrealized G/L, in the holding's **native currency** — same-currency-only means no FX here. The Display-Currency Portfolio total / Net Worth / missing-FX-rate prompt remain **Story 4.4**.
- **`service/transaction.Holdings` reads prices via `store.LatestPrices`** (store-not-service, AD-1) — it does NOT import `service/price`, exactly as 4.2 reads securities via `store`. `HoldingView` gained `HasPrice`/`Price`/`PriceDate`/`MarketValue`/`UnrealizedGain` (no interface-signature change).
- **`/prices` page** linked from `/settings` (like `/exchange-rates` / `/securities`); the five-item primary nav is unchanged. The price-form security `<select>` lists **all** securities (a price applies to any security — not currency-filtered, unlike the trade form).
- The loss-colour flag is computed in the router (`UnrealizedNegative`) rather than in the templ (no `strings` import in `pages.templ`).

### File List

New:
- `db/migrations/00011_prices.sql`, `db/query/price.sql`
- `internal/store/price.sql.go` (sqlc-generated; `models.go`/`querier.go` regenerated)
- `internal/service/price/price.go`, `internal/service/price/price_test.go`
- `internal/domain/valuation.go`, `internal/domain/valuation_test.go`

Modified:
- `internal/service/transaction/transaction.go` (`HoldingView` + price fields; `Holdings` loads `store.LatestPrices` and calls `domain.ValueHolding`) + `internal/service/transaction/trade_test.go` (`TestHoldingValuation`)
- `internal/http/router.go` (`Prices` iface + `Deps` field, `/prices` GET/POST routes + `pricesForm`/`pricesSubmit`/`renderPrices`, `renderInvestmentDetail` HoldingRow price mapping, `service/price` import) + `internal/http/router_test.go` (stub `Prices`, extended `stubHolding`/`Holdings` stub, `TestPricesRequiresAuth`/`TestPricesAddAndList`/`TestHoldingValuationColumns`)
- `cmd/server/main.go` (wire `price.New(pool)`)
- `web/pages.templ` (`PricesPage` + Settings "Manage prices →" link + holdings-table Price/Market value/Unrealized G/L columns) + regenerated `web/pages_templ.go`; `web/shell.go` (`PriceRow`, `HoldingRow` price fields); rebuilt `web/static/css/app.css`
- `README.md` (security prices section)

## Change Log

| Date | Change |
| --- | --- |
| 2026-06-29 | Story 4.3 drafted (create-story): `price` entity (`00011`, append-only effective-dated per security, analog of `exchange_rate`); `db/query/price.sql` + `service/price` (`Add`/`PriceAt`/`LatestPrices`/`List`, `ErrNoPrice`); single canonical `domain.ValueHolding` (market value + unrealized G/L, native currency, AD-10); `Holdings` extended to value on read via `store.LatestPrices`; `/prices` management page linked from Settings; investment account-detail gains Price/Market value/Unrealized G/L columns (staleness date shown, "—" when no price). Scope: native-currency per-holding valuation only — Display-Currency Portfolio total / Net Worth / missing-FX prompt are 4.4. Status → ready-for-dev. |
| 2026-06-29 | Story 4.3 implemented (dev-story): `00011` `price` table; `db/query/price.sql` + `service/price` (Add/PriceAt/LatestPrices/List, ErrNoPrice/ErrSecurityNotFound/ErrNonPositivePrice); single shared `domain.ValueHolding` (market value + unrealized G/L, native currency); `service/transaction.Holdings` now values on read via `store.LatestPrices` (store-not-service); `/prices` page linked from Settings + investment account-detail Price/Market value/Unrealized G/L columns ("—" when no price, "as of {date}" staleness). All 3 ACs verified (domain unit + live DB + live HTTP incl. derived-on-read re-valuation, append-only history, non-positive→400). build/vet/test/nofloat green. Status → review. |
