---
baseline_commit: 2782354
---

# Story 5.2: Dashboard KPI cards

Status: review

<!-- Note: Validation is optional. Run validate-create-story for quality check before dev-story. -->

## Story

As the owner,
I want a row of summary cards when I open the app,
so that I see my key numbers at a glance.

## Acceptance Criteria

From `epics.md` → Epic 5 → Story 5.2 (realizes **FR-11**, **UX-DR2**; honors **AD-1**, **AD-10**, **AD-11**, **AD-12**, **NFR-4**, **NFR-5**). **Given** I am authenticated with data, **When** I land on the Dashboard, **Then**:

1. **KPI cards show Net Worth, Portfolio Value, Total Gain/Loss, and Cash in the Display Currency**, each with an **icon chip**, a **large bold number**, and a **period-change delta (▲/▼ %, green up / red down)** (FR-11, UX-DR2).
2. **The dashboard renders with no manual navigation after login** — `/` is the post-login landing page (Epic 1 already redirects `loginSubmit` → `/`); this story replaces the `DashboardPage` placeholder with the real KPI row so the numbers appear immediately.
3. **Period change shows "—" until a prior sample exists** — and also when the prior-sample baseline is zero or non-positive (a % change is undefined), so the card never shows a bogus or ∞ delta (UX-DR8 empty-state rule).
4. Money renders **pre-formatted with currency and sign**, and **gain/loss is conveyed by sign as well as colour** (NFR-4) — i.e. the cards compose the **5.1 `Card` / `Amount`** primitives, and the delta arrow (▲/▼) carries direction in addition to colour.
5. Partial-total honesty is preserved (AD-12 / Q5): if a held currency has **no rate** to the Display Currency, or a holding has **no price**, the affected figure is a **partial total** and the dashboard surfaces the same **missing-rate / unpriced notice** the `/investments` page already shows — never a blank page, never a guessed/inverted figure. A portfolio-load failure (e.g. an oversold ledger) shows a **graceful banner**, mirroring `investmentsPage`.

### Locked design decisions (read before implementing)

- **D1 — "Total Gain/Loss" = total UNREALIZED gain/loss on current holdings, in the Display Currency.** Computed by the **same convert-then-sum / round-once** path as Portfolio Value (AD-12): `TotalGain = round(Σ convert(unrealizedGain_native))`. **Realized G/L is NOT summed here** — it stays per-native-currency with no FX, exactly as the 4.4 owner decision pinned ([[financas-epic4-decisions]]); it continues to live on `/investments`. The card's sign/colour come from `Positive`/`Negative` flags the handler computes from the rounded figure (the 4.3/4.4 convention), rendered via `Amount` so the sign carries direction (NFR-4). *(Recommended default — confirmed against the Epic-4 no-FX-on-realized invariant; flag for veto at dev-story if the owner wants realized folded in.)*
- **D2 — "Cash" KPI = Σ converted cash assets in the Display Currency** (cash + investment-account cash balances; credit balances are liabilities, not cash). This is a sub-aggregate `domain.NetWorth` already computes internally (`cashConv`) — expose it, don't recompute.
- **D3 — Period-change baseline = the full portfolio value computed at the PRIOR SAMPLE DATE (AD-11).** The "prior sample date" = the **second-most-recent distinct `Price`/`ExchangeRate` `effective_date` on or before today** (AD-11 names Price/ExchangeRate as the sampled inputs). The most recent such date is the sample the *current* value already reflects, so the baseline is the one *before* it; **"—" when fewer than two distinct samples ≤ today exist** (a single sample has nothing prior to compare against — the day-one state). *(Note: an earlier draft said "most recent strictly before today"; that degenerates to the current sample whenever the latest price/rate is in the past, yielding a misleading 0% — corrected during review to second-most-recent.)* The baseline is a **full as-of-that-date valuation** — positions (holdings/cash from the ledger up to that date), prices effective ≤ that date, and rates effective ≤ that date — never today's positions revalued, and **never retroactively recomputed at today's rate** (AD-6/AD-11). **Each value card shows its own % delta**; `delta% = round((now − then) / then × 100, 1 dp)` in **decimal** (AD-4, no float — NFR-5), with `▲` when positive, `▼` when negative, no arrow when flat (0.0%), and **"—"** when there is no prior sample or `then ≤ 0`. This single-prior-date **as-of valuation seam is the foundation 5.3** (value-over-time chart) reuses across all sample dates — build it cleanly here, walk it there.

## Tasks / Subtasks

- [x] **Task 1 — Domain: expose Cash + Total Gain/Loss and a canonical % change (AC: #1, #3; D1, D2, D3)**
  - [x] In `internal/domain/networth.go`, **extend `ValuationInput`** with `Unrealized []money.Money` (per-holding unrealized gain, native — priced holdings only, same source slice shape as `Holdings`). **Extend `Valuation`** with `Cash money.Money` and `TotalGain money.Money` (both Display Currency). In `NetWorth`, fold `in.Unrealized` through the **same `convertSum`** helper and **round once** (banker's, AD-12) → `TotalGain`; return the already-computed `cashConv` rounded once → `Cash`. **Missing semantics unchanged** (a non-zero unrated unrealized amount records its currency in `Missing` like the others). Do not change existing field meanings — `PortfolioValue`/`NetWorth`/`Missing` keep their contracts (4.4 tests must stay green).
  - [x] Add a **canonical percentage-change** home (AD-10) — e.g. `func PercentChange(now, base money.Money) (pct decimal.Decimal, ok bool)` in a small `internal/domain/change.go` (or alongside `networth.go`). Rule: `ok=false` when `base.Amount() ≤ 0` (undefined/▲∞ guard) **or** currencies differ; else `pct = round((now − base) / base × 100, 1 dp)` using **decimal** arithmetic and banker's rounding — **no float anywhere** (NFR-5 / `make nofloat`). Both args are Display-Currency `money.Money`; document the same-currency precondition.
  - [x] Unit tests (`networth_test.go` additions + `change_test.go`): `Cash`/`TotalGain` convert-then-sum-and-round-once for same-currency, multi-currency-with-rate, and missing-rate-partial cases (mirror the existing `TestNetWorth*` table style); `PercentChange` for up, down, flat, zero-base (`ok=false`), negative-base (`ok=false`), and a banker's-rounding boundary. Assert decimals, not floats.

- [x] **Task 2 — Service: as-of-date valuation + the Dashboard read-model (AC: #1, #3, #5; D1, D2, D3)**
  - [x] In `internal/service/valuation/valuation.go`, **refactor `Portfolio(ctx)` to an as-of-date core** `portfolioAsOf(ctx, asOf time.Time) (Portfolio, error)`; keep the public `Portfolio(ctx) (Portfolio, error)` as `s.portfolioAsOf(ctx, time.Now())` (behaviour-preserving — 4.4 tests and the `/investments` page must be unchanged). Thread `asOf` through the three time-dependent reads:
    - `latestPrices(ctx, q, asOf)` — pass `asOf` to `q.LatestPrices` (the query already filters `effective_date <= $1`).
    - `buildRates(ctx, q, display, asOf, groups...)` — pass `asOf` as `RateEffectiveAt`'s `EffectiveDate` (already `effective_date <= $3`) instead of `time.Now()`.
    - **Ledger filter:** when building `allLegs` / `eventsDesc`, **skip rows with `occurred_on` after `asOf`** so balances and holdings are derived as of that date (in-memory filter on the already-loaded `ListTransactions` rows — **no new query**). `occurred_on` is a date; compare date-only (truncate `asOf` to its date, treat the bound as inclusive `≤`). For `asOf = now` this includes everything → identical to today's behaviour.
  - [x] Extend the `Portfolio` struct with `Cash money.Money` and `TotalGain money.Money` (Display Currency); collect each priced holding's `UnrealizedGain` (native) into an `unrealized []money.Money` slice and pass it as `domain.ValuationInput{… Unrealized: unrealized}`. Populate `Cash`/`TotalGain` from the returned `domain.Valuation`.
  - [x] Add the **Dashboard read-model** + method (the single place that orchestrates the comparison so the handler does **no math**, AD-1/AD-10):
    ```go
    type KPI struct {
        Value     money.Money     // Display Currency
        Positive  bool            // gain/loss colour+sign (TotalGain only)
        Negative  bool
        DeltaPct  decimal.Decimal // period change %, valid when HasDelta
        DeltaUp   bool            // pct > 0
        DeltaDown bool            // pct < 0
        HasDelta  bool            // false → render "—"
    }
    type Dashboard struct {
        NetWorth, Portfolio, Cash, GainLoss KPI
        Display   money.Currency
        Missing   []money.Currency // partial-total notice (from the CURRENT valuation)
        Unpriced  []string
        PriorDate time.Time        // zero when no prior sample
    }
    func (s *Service) Dashboard(ctx context.Context) (Dashboard, error)
    ```
    `Dashboard`: compute `cur := portfolioAsOf(ctx, now)`; find the **prior sample date** = max `EffectiveDate` strictly before today across `q.ListPrices` + `q.ListExchangeRates` (in Go — **no new SQL/sqlc/migration**; owner-entered volume is tiny); if found, `base := portfolioAsOf(ctx, prior)` and set each KPI's delta via `domain.PercentChange(curFigure, baseFigure)` (HasDelta = ok); if no prior sample, all `HasDelta=false`. `GainLoss.Positive/Negative` from the **current** `TotalGain` sign. Propagate `cur.Missing`/`cur.Unpriced`/`cur.Display`. **Reuse `ErrOversold`** — surface load failures unchanged.
  - [x] Service tests (`valuation_test.go`, DB-gated, skip without `TEST_DATABASE_URL`/`DATABASE_URL`): assert `portfolioAsOf` with an `asOf` in the past excludes later trades/prices/rates (e.g. a buy and a price added "today" don't appear in last-month's baseline); assert `Dashboard` returns the four figures, the per-card deltas for a two-sample fixture, `HasDelta=false` when only one sample date exists, and that `Portfolio(ctx)` output is unchanged vs the 4.4 expectations. Keep the existing 4.4 valuation tests green.

- [x] **Task 3 — HTTP handler + Valuation interface (AC: #1, #2, #5)**
  - [x] Extend the consumer-side `Valuation` interface in `internal/http/router.go` with `Dashboard(ctx context.Context) (valuation.Dashboard, error)` (keep `Portfolio`).
  - [x] Replace the dashboard route (currently `pr.Get("/", renderPage(deps, "dashboard", … web.DashboardPage(d)))`) with a real `dashboardPage(deps)` handler: call `deps.Valuation.Dashboard(ctx)`, build a `web.DashboardView`, render `web.DashboardPage(shellData(deps, ctx, "dashboard"), view)`. **No financial math in the handler** — it only `.String()`-formats `money.Money`, formats `DeltaPct` (e.g. `pct.StringFixed(1)+"%"`), and copies the boolean flags into the view (AD-1). On error mirror `investmentsPage`: 500 + a graceful banner (`ErrOversold` → the same "a sell exceeds the quantity held…" hint), never a blank page. Build the `Missing`/`Unpriced` notice strings exactly as `investmentsPage` does (join codes/symbols).
  - [x] Handler tests (`internal/http/router_test.go`): with a stub `Valuation` returning a known `Dashboard`, assert the dashboard HTML at `/` contains the four figures, the Display Currency, the per-card deltas (▲/▼ + percent) and a `—` for a no-prior-sample KPI, the missing-rate/unpriced notice when set, and the graceful banner on error. Mirror the existing register/investments handler-test style (stub deps, `want`-substring table). Keep all existing router tests green (the `/` route now needs the stub `Valuation` to implement `Dashboard`).

- [x] **Task 4 — Web: rebuild DashboardPage with the KPI row (AC: #1, #2, #4)**
  - [x] Add view types to `web/shell.go`: `DeltaText struct { Display string; Up, Down, None bool }` (None → render "—") and `KPICardView struct { Label string; Icon …; Amount MoneyText; Delta DeltaText }`, plus `DashboardView struct { Cards []KPICardView; MissingCodes, UnpricedSymbols, ErrMsg string }`. Reuse `MoneyText` for each card's value (currency+sign pre-formatted). Pick a small fixed icon per KPI (an inline SVG or a token-styled glyph in an accent chip — keep it token-driven; the icon is decorative, `aria-hidden`).
  - [x] Rebuild `web/pages.templ` `DashboardPage(data ShellData, view DashboardView)`: a responsive **grid of four KPI cards** (e.g. `grid gap-4 sm:grid-cols-2 lg:grid-cols-4`), each composing **`@Card("…")`** with: the **icon chip** (rounded accent chip), the muted **`text-label`** caption, the **`@Amount(card.Amount, AmountStat)`** number, and the **delta** sub-element. Add a small **delta renderer** (a `templ deltaBadge(d DeltaText)` or inline) showing `▲`/`▼` + percent with `text-gain`/`text-loss`, an `sr-only` "up"/"down" label (NFR-4: arrow + text, not colour alone), and **"—"** when `None`. Render the missing-rate/unpriced notice (reuse the `/investments` markup idiom) and the `ErrMsg` banner branch. **Compose primitives — do not re-pick `rounded-card`/`text-*` ad-hoc** (AD-10).
  - [x] If any new size/utility is needed it must come from the **existing** type scale / Tailwind built-ins — **no palette/shape/token rename** (1.x–4.x pages depend on them). If a genuinely new utility is unavoidable, add it to `web/static/css/input.css` and rebuild `app.css`; prefer reuse (`text-stat`/`text-label`, `text-gain`/`text-loss`, `rounded-card`, built-in `sr-only`/`tabular-nums`) so **no CSS change** is the default outcome.
  - [x] Render tests (`web/pages_test.go` or extend the web render tests via `renderToString`): `DashboardPage` renders four cards with their labels, the `Amount` figures (currency + gain/loss sign on the gain card), each delta (▲/▼ + percent, gain/loss colour, sr-only direction), a `—` for a `None` delta, and the notice/banner branches. DB-free.

- [x] **Task 5 — Wire, verify, docs (AC: all)**
  - [x] `make generate` after the `.templ` edit (commit regenerated `web/pages_templ.go`). **No `make sqlc`** (no new query) and **no `make css`** unless a CSS token was actually added. `GOTOOLCHAIN=local go build ./... && go vet ./... && go test -count=1 ./...` green (web + handler tests DB-free; the new service test is DB-gated and skips without a DB — run it with the local DB once: `docker compose up -d db`, `TEST_DATABASE_URL=…:5433…`). **`make nofloat` stays green** (all new math is `decimal`). `gofmt -l` clean on touched `.go` files.
  - [x] **Live smoke** (compose db :5433 + freshly-built binary, owner/financas, with seeded multi-currency data + at least two distinct price/rate effective dates so a prior sample exists): log in → land on `/` with **no manual nav** → confirm the four cards (Net Worth, Portfolio Value, Total Gain/Loss, Cash) in the Display Currency with icon chips, the period deltas (▲/▼ %), a gain showing a sign as well as green; reset the DB to one sample date (or a fresh load) → confirm deltas show **"—"**; resize to confirm the responsive grid. Confirm `/investments` is **unchanged** (the `portfolioAsOf` refactor preserved its output) and no visual regression elsewhere.
  - [x] Update `README.md` (the **App shell & design tokens** or a new **Dashboard** note): the Dashboard composes the 5.1 `Card`/`Amount` primitives into a four-KPI row (Net Worth, Portfolio Value, Total Gain/Loss = unrealized, Cash) in the Display Currency with a period-change delta; the baseline is the as-of prior-sample-date valuation derived from effective-dated Price/Rate history (no snapshot table, AD-11); deltas show "—" until a prior sample exists; all aggregation math lives in `domain` (AD-10), the web layer renders pre-formatted figures (AD-1), no float (NFR-5).
  - [x] **Commit + push to `main`** (trunk-based, one commit per story — owner's standing instruction). `baseline_commit` is HEAD `2782354`.

## Dev Notes

### Scope boundary — what this story does NOT do (read first)

- **NO trend chart, NO allocation breakdown, NO history widget / insight card** — those are **5.3 (value-over-time chart), 5.4 (allocation), 5.5 (history + insight)**. This story builds **only the KPI card row** and the single-prior-date delta. The multi-point sampled series belongs to 5.3 and **reuses** the `portfolioAsOf` seam this story creates — do not build the full series here.
- **NO snapshot table, NO migration, NO new DB schema** (AD-11). The baseline is **derived** from the existing effective-dated `price`/`exchange_rate` history; the prior sample date is found in Go from the existing `ListPrices`/`ListExchangeRates` rows. **No new SQL / no `make sqlc`** (avoids the Docker-gated sqlc step).
- **NO palette/shape/token rename, NO new dependency, NO charting lib.** Compose the existing 5.1 primitives + `@theme` tokens. A renamed token silently breaks live pages (Tailwind utilities are generated from token names). The charting-library pick is deferred to 5.3.
- **NO realized-G/L FX folding** (D1) — realized stays per-native-currency on `/investments` (4.4 owner decision). **NO change to `Portfolio()`'s public output** — the `portfolioAsOf` refactor must be behaviour-preserving so `/investments` and the 4.4 tests are untouched.
- **NO float anywhere** (NFR-5 / `make nofloat`) — the new percentage-change figure is `decimal`, rounded banker's at the boundary (AD-4/AD-12).

### What already exists (reuse, don't rebuild)

[Source: internal/service/valuation/valuation.go; internal/domain/networth.go; web/components.templ; web/shell.go; internal/http/router.go; 4-4-portfolio-valuation-net-worth.md; [[financas-epic4-decisions]]]

- **`domain.NetWorth(display, ValuationInput, rates) Valuation`** is the canonical Display-Currency aggregation (AD-10): convert-then-sum at full precision, **round once** per figure (AD-12), `Missing` lists non-zero unrated currencies. It already computes `cashConv` internally — D2's `Cash` is one rounded return away; D1's `TotalGain` is the same fold over a new `Unrealized` input. **Reuse this function — do not write a parallel aggregator.**
- **`valuation.Service.Portfolio(ctx)`** already assembles native cash/liabilities/holdings + per-holding `UnrealizedGain`, builds the exact native→Display rates, and calls `domain.NetWorth`. Its three time-dependent reads are the only things to parameterize for as-of-date: `latestPrices` (`q.LatestPrices(asOf)` — **already** `effective_date ≤ $1`), `buildRates` (`q.RateEffectiveAt(…, asOf)` — **already** `effective_date ≤ $3`), and the **in-memory ledger filter** by `occurred_on ≤ asOf` (the rows are already loaded via `q.ListTransactions`). This is why the baseline needs **no new query** — only a date threaded through.
- **`q.ListPrices` / `q.ListExchangeRates`** both return rows carrying `EffectiveDate time.Time` — scan them in Go for `max(EffectiveDate) < today` to get the prior sample date. Tiny owner-entered volume; no pagination concern.
- **5.1 primitives (`web/components.templ`):** `@Card(class)` (canonical card surface + passthrough class), `@Amount(MoneyText, AmountSize)` (pre-formatted money + currency + **sign + sr-only label** at a type-scale size — gain/loss not colour-only, NFR-4), `@Badge`. `AmountStat` is the KPI-card figure size; `text-label` the muted caption; `text-gain`/`text-loss` the semantic colours. **Compose these — do not restyle** (AD-10). [Source: web/components.templ; web/shell.go `amountClass`/`MoneyText`]
- **The `Positive`/`Negative` colour-flag + pre-formatted-money convention (4.3/4.4):** the handler computes the flags from the rounded `money.Money`; the web layer renders, never computes (AD-1). `Amount` already encodes it. Apply the same for the `GainLoss` card and (new) the delta arrow.
- **`investmentsPage` (router.go:1152)** is the template to mirror for the new `dashboardPage`: load read-model → build view → render shell-wrapped page; on error, 500 + graceful banner with the `ErrOversold` hint; build `Missing`/`Unpriced` notice strings by joining. **`loginSubmit` already redirects to `/`** (router.go:1328) — AC #2's "no manual navigation after login" is satisfied; this story just makes `/` render the real cards.
- **Render/handler test harnesses:** `web/shell_test.go`'s `renderToString` (DB-free component render) for the dashboard templ; `internal/http/router_test.go`'s stub-deps + `want`-substring tables for the handler; `internal/service/valuation/valuation_test.go`'s DB-gated style for `portfolioAsOf`/`Dashboard`. Mirror each.
- **Build/codegen:** `.templ` → committed `*_templ.go` via `make generate` (templ + sqlc; **only templ runs here**). CSS only if a token is added (`make css`). `make nofloat` must stay green. [Source: Makefile]

### Architecture invariants this story must honor

- **AD-1 — layering / web renders only; service reads through the store.** The web layer takes pre-formatted strings + bool flags and does no arithmetic; the handler does no financial math (delta % is computed in `domain.PercentChange`, called by the service). The valuation service reads exclusively through `store` (no service→service). [Source: ARCHITECTURE-SPINE.md#AD-1]
- **AD-10 — one canonical home per derived figure.** `Cash`, `TotalGain`, and the period-change `%` are derived figures → they live in `domain` (extend `NetWorth`'s `Valuation`; add `PercentChange`). Pages compose the 5.1 visual primitives, not ad-hoc styles. [Source: #AD-10]
- **AD-11 — value-over-time derived from history, no snapshot table.** The period-change baseline = the portfolio value computed at the **prior sample date** (max Price/Rate effective date < today), derived from the effective-dated history; **shows "—" until a prior sample exists**; never retroactively recomputed at today's rate. This single-date seam is what **5.3 generalizes** to the full sampled series. [Source: ARCHITECTURE-SPINE.md#AD-11]
- **AD-12 — banker's rounding, convert-then-sum, round once.** `Cash`/`TotalGain` follow the existing convert-then-sum-round-once; the delta % rounds once (banker's, 1 dp). [Source: #AD-12]
- **AD-6 — owner-entered effective-dated prices/rates, no inversion.** As-of reads use the then-current row (`effective_date ≤ asOf`); a missing pair stays absent → `Missing` (partial total), never inverted/guessed — unchanged from 4.4. [Source: #AD-6; [[financas-epic4-decisions]]]
- **NFR-5 — no float in the financial core (`make nofloat`).** All new math is `decimal`/`money`. [Source: Makefile `nofloat`]
- **NFR-4 — accessibility = reasonable defaults.** Gain/loss (and the delta direction) conveyed by **sign/arrow + sr-only text** as well as colour; semantic markup; legible contrast from the oklch palette. [Source: prd.md §"Cross-Cutting NFRs"]

### Design intent (from the PRD/UX — apply here)

[Source: epics.md UX-DR2; prd.md §"Aesthetic & Tone", FR-11]

- **UX-DR2 — KPI summary card row:** a row of summary cards at the top of the dashboard, each with an **icon chip**, a **large bold number** in the Display Currency, and a **small period-change delta (▲/▼ %, green up / red down)**. For Financas: **Net Worth, Portfolio Value, Total Gain/Loss, Cash**. This is the realization of FR-11's "on login, show total Portfolio value, Net Worth, period change, total Gain/Loss."
- **Feel: clean, fast, uncluttered;** the Net Worth / portfolio number and gain/loss are the **visual hero** — which is exactly why the cards compose `Amount` (the large-number primitive 5.1 built for this). Tone: plain, calm, neutral — no gamification. The delta is **small** secondary information; the big number leads.
- **UX-DR8 — empty/loading/error states:** "period change shows '—' until a prior sample exists" is an explicit empty-state requirement, not an excuse to skip the delta — compute it when a prior sample exists, show "—" only when it genuinely doesn't (or the baseline is ≤ 0).

### Previous-story intelligence (5.1 + 4.4/4.3) — load-bearing

[Source: 5-1-design-token-system-component-primitives.md; 4-4-portfolio-valuation-net-worth.md; [[financas-epic4-decisions]]; [[financas-epic1-progress]]]

- **5.1 (HEAD `2782354`)** shipped `Card`/`Badge`/`Amount` over the `@theme` tokens and the `text-hero`/`text-stat`/`text-label` type scale, refactored the `/investments` hero to `@Amount(…, AmountHero)`/`@Card`. **This story is the first real consumer** of those primitives — compose them; if a primitive feels missing (e.g. a delta element), prefer a **dashboard-local** templ helper over restyling `Amount`. `Amount` already does the sign+sr-only NFR-4 affordance — reuse its pattern for the delta arrow.
- **4.4 (`c669327`)** added the `valuation.Service`, `domain.NetWorth`, the `Portfolio` read-model, and the `Positive`/`Negative` colour-flag pattern. The **owner decision** that **realized G/L is per-native-currency with no FX** ([[financas-epic4-decisions]]) is why D1 defines Total Gain/Loss as **unrealized only** (the FX-consistent figure). Don't reopen that — it was resolved 2026-06-29.
- **Missing-rate / unpriced partial-total honesty (Q5/AD-12)** is a pinned Epic-4 invariant: the dashboard must show the same notices and never block or guess. Reuse `cur.Missing`/`cur.Unpriced` from the current valuation.
- **House style:** typed view structs in `web/shell.go` carrying pre-formatted strings + bools; table-of-`want`-substring render/handler tests; `GOTOOLCHAIN=local`; commit regenerated `*_templ.go`; `make nofloat` green; `gofmt -l` clean; local DB host **5433** (`docker compose up -d db`) for the DB-gated service test + live smoke; dev login `owner`/`financas`; **one commit per story, push to `main`**.

### Project Structure Notes

- **Modified — domain:** `internal/domain/networth.go` (`ValuationInput.Unrealized`; `Valuation.Cash`/`.TotalGain`); new `internal/domain/change.go` (`PercentChange`) + `change_test.go`; `internal/domain/networth_test.go` (Cash/TotalGain cases).
- **Modified — service:** `internal/service/valuation/valuation.go` (`portfolioAsOf` refactor; `Portfolio.Cash`/`.TotalGain`; `KPI`/`Dashboard` types + `Dashboard(ctx)` method; prior-sample-date lookup); `internal/service/valuation/valuation_test.go`.
- **Modified — http:** `internal/http/router.go` (`Valuation` interface `+Dashboard`; new `dashboardPage` handler replacing the `/` placeholder route); `internal/http/router_test.go` (dashboard handler tests + stub `Valuation.Dashboard`).
- **Modified — web:** `web/shell.go` (`DeltaText`, `KPICardView`, `DashboardView`); `web/pages.templ` (`DashboardPage` rebuilt to take `DashboardView` + KPI grid + delta renderer) → regenerated `web/pages_templ.go`; web render test (`web/pages_test.go` or extend existing). CSS only if a token is added (`web/static/css/input.css` + rebuilt `app.css`).
- **Modified — docs:** `README.md`.
- **NOT touched:** no `db/migrations`, no `db/query`, no `internal/store` regen (no `make sqlc`), no `cmd` change beyond what compiles, no new module dependency.

### Testing standards

- **domain (pure, no DB):** table tests for `Cash`/`TotalGain` (convert-then-sum-round-once; missing-rate partials) and `PercentChange` (up/down/flat/zero-base/negative-base/banker's boundary) — assert `decimal`, no float.
- **service (DB-gated, skips without `TEST_DATABASE_URL`/`DATABASE_URL`):** `portfolioAsOf` excludes post-`asOf` trades/prices/rates; `Dashboard` returns four figures + per-card deltas, `HasDelta=false` with a single sample date; `Portfolio(ctx)` unchanged vs 4.4.
- **http (DB-free, stub deps):** `/` dashboard HTML contains the four figures, Display Currency, per-card ▲/▼ deltas, a `—` for no-prior-sample, the missing-rate/unpriced notice, and the graceful error banner.
- **web (pure render):** `DashboardPage` renders four cards (labels, `Amount` figures with gain/loss sign, deltas with arrow+sr-only direction, `—` for `None`, notice/banner branches).
- `go test ./...` green (web/http DB-free; DB-gated service test skips without DB). `go vet`, `gofmt -l`, `make nofloat` clean. Visual no-regression (incl. `/investments` unchanged) confirmed by the live smoke.

### References

- [Source: epics.md#Story 5.2] — ACs (Net Worth / Portfolio Value / Total Gain/Loss / Cash in Display Currency; icon chip; period delta ▲/▼ %; "—" until a prior sample); FR-11, UX-DR2
- [Source: epics.md UX-DR2, UX-DR8] — KPI card row anatomy; empty-state "—" rule
- [Source: epics.md FR-11; prd.md §4.5 FR-11] — portfolio dashboard on login; period change "—" until a prior snapshot
- [Source: ARCHITECTURE-SPINE.md#AD-11] — value-over-time derived from history, **no snapshot table**; period-change baseline = portfolio value at the prior sample date; never recomputed at today's rate
- [Source: ARCHITECTURE-SPINE.md#AD-1, #AD-10, #AD-12, #AD-6] — web renders only / service reads through store; one canonical domain home; convert-then-sum round-once; no rate inversion
- [Source: internal/domain/networth.go] — the aggregation to extend (Cash/TotalGain); [internal/service/valuation/valuation.go] — the service to parameterize by `asOf` + the Dashboard read-model; [internal/http/router.go investmentsPage / loginSubmit] — handler + post-login-redirect patterns to mirror
- [Source: web/components.templ; web/shell.go] — the 5.1 `Card`/`Amount` primitives + `MoneyText`/`AmountStat`/`text-label` to compose
- [Source: 5-1-design-token-system-component-primitives.md; 4-4-portfolio-valuation-net-worth.md; [[financas-epic4-decisions]]] — primitives + the `Positive`/`Negative` convention; the no-FX-on-realized decision (D1)
- [Source: Makefile] — `make generate` (templ) / `make nofloat`; committed-artifact rule; no `make sqlc`/`make css` needed

## Dev Agent Record

### Agent Model Used

claude-opus-4-8[1m] (Opus 4.8, 1M context) — bmad-dev-story workflow.

### Debug Log References

- TDD: wrote `internal/domain/change_test.go` + `networth_test.go` additions first (RED — `PercentChange`/`Unrealized`/`Cash`/`TotalGain` undefined), then implemented; GREEN. Service tests (`TestDashboardAsOfAndDeltas`, `TestDashboardNoPriorSample`) and handler/web render tests added alongside their implementations.
- Confirmed `LatestPrices`/`RateEffectiveAt` already filter `effective_date ≤ $`, so as-of-date pricing/rates needed only a threaded `asOf`; the ledger as-of cut is an in-memory `OccurredOn.After(asOf)` skip — **no new query, no `make sqlc`, no migration**. Prior sample date is scanned in Go from `ListPrices`/`ListExchangeRates`.
- `make generate` re-ran sqlc (Docker) but produced no `internal/store` diff (no query change), as expected.
- **Review fix (period-change semantics):** independent review caught that the first cut of `priorSampleDate` used "most recent sample strictly before today", which collapses to the *current* sample whenever the latest price/rate is in the past → a misleading 0% delta instead of "—". Corrected to the **second-most-recent distinct sample ≤ today** (the sample before the one the current value reflects), with "—" when <2 distinct samples exist. Added `TestDashboardPriorSampleWhenLatestIsPast` (two past samples, none today) which fails the old logic and passes the fix.

### Completion Notes List

- **Domain (Task 1):** extended `domain.ValuationInput` with `Unrealized` and `domain.Valuation` with `Cash`/`TotalGain` — folded through the existing `convertSum` (convert-then-sum, round-once, AD-12), Missing semantics unchanged. New `domain.PercentChange(now, base) (decimal, ok)` (AD-10): signed %, banker's-rounded to 1 dp, `ok=false` for non-positive base or currency mismatch (→ "—"); decimal only (NFR-5).
- **Service (Task 2):** refactored `Portfolio(ctx)` → `portfolioAsOf(ctx, asOf)` (public `Portfolio` = `portfolioAsOf(now)`, behaviour-preserving — 4.4 test green, `/investments` unchanged); threaded `asOf` into prices/rates/ledger. Added `Cash`/`TotalGain` to `Portfolio`. New `KPI`/`Dashboard` read-model + `Dashboard(ctx)` orchestrating current vs prior-sample baseline (D3) and computing per-card deltas via `domain.PercentChange`; `priorSampleDate` scans price/rate history in Go (AD-11, no snapshot table).
- **HTTP (Task 3):** `Valuation` interface `+Dashboard`; new `dashboardPage` handler replacing the `/` placeholder route — formats money + copies flags only (AD-1), graceful banner on load failure (oversold hint), partial-total notices mirrored from `/investments`. Delta rendered as magnitude % (the ▲/▼ arrow carries direction).
- **Web (Task 4):** `DeltaText`/`KPICardView`/`DashboardView` view types; rebuilt `DashboardPage(data, view)` into a responsive four-card grid composing `@Card` + `@Amount` (AmountStat) + an icon chip + a `deltaBadge` (▲/▼ + sr-only up/down, or "—"); no token rename. Rebuilt `app.css` for the new utilities.
- **Verification (Task 5):** `go build`, `go vet`, `go test -count=1 ./...` all green (incl. DB-gated valuation/store/service tests against local PG :5433); `make nofloat` green; `gofmt -l` clean. **Live smoke** (freshly-built binary, owner/financas, dev DB, Display = USD): `/` renders 200 with no manual nav after login — four KPI cards (Net Worth, Portfolio Value, Total Gain/Loss `+50.0000 USD` with `text-gain` sign+colour, Cash) with icon chips, the responsive grid, and per-card deltas; the dev data's latest sample is 2026-06-01 (= today's effective sample) so deltas correctly read **0.0%** (no change since last sample), and the missing-rate/unpriced **partial-total notices render**. The non-zero ▲/▼ path is covered by `TestDashboardAsOfAndDeltas` (up 2.0% / up 10.0% / flat / gain "—").
- **Decision D1 flagged for review:** Total Gain/Loss = total **unrealized** G/L only (FX-consistent; realized stays per-currency on `/investments`, per [[financas-epic4-decisions]]). Veto here if realized should be folded in.

### Post-review fixes (independent Opus review → CHANGES REQUESTED, all addressed)

- **[HIGH] Double minus sign on the gain/loss card for a loss** (`internal/http/router.go` `kpiCard` × `web/components.templ` `Amount`): the handler passed a *signed* money string into `Amount`, which also prepends a `−`, so a loss rendered `−-100.0000 BRL`. Fixed: added `money.Money.Abs()` and `kpiCard` now passes the **magnitude** when a gain/loss flag is set (unflagged value cards keep their signed string, so a negative Net Worth still shows its own `−`). Regression test `TestDashboardLossCardSingleMinus` (asserts no double-sign patterns + single `−` + `text-loss`).
- **[MEDIUM] As-of ledger cut compared `OccurredOn` against raw `time.Now()`** instead of the date-truncated `asOf` the spec mandated (timezone-fragile; dropped future-dated rows from current `Portfolio()`). Fixed: the cut is now `dateOnlyUTC(r.OccurredOn).After(dateOnlyUTC(asOf))` — calendar-based and timezone-stable, matching the `effective_date <= asOf::date` used for prices/rates.
- **[HIGH, found independently] Period-change baseline degenerated to 0%** when the latest sample was in the past — `priorSampleDate` now returns the second-most-recent distinct sample ≤ today (see Debug Log; `TestDashboardPriorSampleWhenLatestIsPast`).
- **[LOW]** added the missing loss-case render coverage (above). The two NITs (Dashboard re-reads the ledger for the baseline; minor) were left as-is — acceptable for this single-owner data volume.

### File List

- `internal/money/money.go` (modified — `Abs()` helper for magnitude rendering)
- `internal/domain/change.go` (new — `PercentChange`)
- `internal/domain/change_test.go` (new)
- `internal/domain/networth.go` (modified — `ValuationInput.Unrealized`; `Valuation.Cash`/`.TotalGain`)
- `internal/domain/networth_test.go` (modified — Cash/TotalGain cases)
- `internal/service/valuation/valuation.go` (modified — `portfolioAsOf` refactor; `Portfolio.Cash`/`.TotalGain`; `KPI`/`Dashboard` + `Dashboard(ctx)`; `priorSampleDate`/`dateOnlyUTC`; `latestPrices`/`buildRates` take `asOf`)
- `internal/service/valuation/valuation_test.go` (modified — Dashboard + as-of tests)
- `internal/http/router.go` (modified — `Valuation` interface `+Dashboard`; `dashboardPage` handler + `kpiCard`/`deltaText`; `/` route rewired)
- `internal/http/router_test.go` (modified — `stubValuation.Dashboard`, `cannedDashboard`, dashboard handler tests)
- `web/shell.go` (modified — `DeltaText`/`KPICardView`/`DashboardView`)
- `web/pages.templ` (modified — `DashboardPage` rebuilt + `kpiCard`/`deltaBadge`/`kpiIcon`)
- `web/pages_templ.go` (regenerated)
- `web/shell_test.go` (modified — DashboardPage render tests updated to new signature + KPI/error tests)
- `web/static/css/app.css` (rebuilt — new dashboard utilities)
- `README.md` (modified — Dashboard note in App shell & design tokens)

## Change Log

| Date | Change |
| --- | --- |
| 2026-06-29 | Story 5.2 review fixes (independent Opus review, CHANGES REQUESTED → resolved): fixed the **double minus sign** on the gain/loss card (added `money.Money.Abs()`; `kpiCard` renders the magnitude when a sign flag is set) and the **date-truncated as-of ledger cut** (timezone-stable, matches the price/rate `::date` bound); plus the independently-found **prior-sample-degenerates-to-0%** fix (second-most-recent distinct sample ≤ today). Added loss-case + past-latest-sample regression tests. Full suite + nofloat + vet + gofmt green. |
| 2026-06-29 | Story 5.2 implemented (dev-story): rebuilt the placeholder `DashboardPage` into the **KPI card row** — Net Worth, Portfolio Value, Total Gain/Loss (unrealized), Cash in the Display Currency, each an icon chip + `@Amount` + a **period-change delta (▲/▼ %)** with the "—" empty state. Domain gained `Cash`/`TotalGain` on `Valuation` and a canonical decimal `PercentChange` (AD-10/NFR-5). The valuation service was refactored to an as-of-date core `portfolioAsOf` (behaviour-preserving `Portfolio`) + a `Dashboard` read-model computing the baseline at the **prior sample date** from effective-dated history (AD-11, no snapshot table, no new query/sqlc/migration). New `dashboardPage` handler (`/`); web view types + `deltaBadge`/`kpiIcon`; `app.css` rebuilt. Build/vet/test (incl. DB-gated)/nofloat/gofmt green; live smoke confirmed the cards render with real figures + sign/colour and the partial-total notices. Status → review. |
| 2026-06-29 | Story 5.2 drafted (create-story): rebuild the placeholder `DashboardPage` into the **KPI card row** (Net Worth, Portfolio Value, Total Gain/Loss, Cash) in the Display Currency with icon chips and a **period-change delta (▲/▼ %)**, composing the 5.1 `Card`/`Amount` primitives. Locked decisions: **D1** Total Gain/Loss = total **unrealized** G/L (FX-consistent; realized stays per-currency per the 4.4 decision); **D2** Cash = Σ converted cash assets; **D3** period baseline = full **as-of prior-sample-date** valuation (max Price/Rate effective date < today), per-card % delta, "—" until a prior sample exists (AD-11). Plan: extend `domain.NetWorth`'s `Valuation` (+`Cash`/`TotalGain`) and add `domain.PercentChange` (decimal, AD-4/NFR-5); refactor `valuation.Portfolio` → `portfolioAsOf(asOf)` (prices/rates already date-capable; in-memory ledger filter — **no new query/sqlc/migration**) + a `Dashboard` read-model; new `dashboardPage` handler (`/`); rebuilt `DashboardPage(DashboardView)` with a four-card grid + delta renderer (arrow+sr-only, NFR-4). The `portfolioAsOf` seam is the foundation 5.3's value-over-time chart reuses. Status → ready-for-dev. |
