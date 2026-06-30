---
baseline_commit: 1469f90
---

# Story 5.3: Value-over-time trend chart

Status: review

<!-- Note: Validation is optional. Run validate-create-story for quality check before dev-story. -->

## Story

As the owner,
I want a trend chart of my portfolio value,
so that I see how I'm doing over time.

## Acceptance Criteria

From `epics.md` → Epic 5 → Story 5.3 (realizes **FR-12**, **UX-DR3**; honors **AD-1**, **AD-6**, **AD-10**, **AD-11**, **AD-12**, **NFR-4**, **NFR-5**). **Given** price and exchange-rate history exists, **When** the trend chart renders, **Then**:

1. **It plots the Display-Currency value sampled at each date an input changed, derived from history with NO snapshot table** (FR-12, AD-11). The plotted figure is **Net Worth** (matches the dashboard hero / UX-DR3 headline); each sample point = `portfolioAsOf(date).NetWorth`.
2. **A range toggle (e.g. monthly) changes the window** — 1M / 3M / 1Y / All, via `?range=` links that reload the dashboard (no JS required; mirrors the existing archived-accounts query-param toggle). The active range is visually marked.
3. **Values are NEVER retroactively recomputed at today's rate** — each historical point uses the Price/ExchangeRate **effective on or before that point's date** (AD-6/AD-11). This is already guaranteed by `portfolioAsOf` (5.2): prices via `LatestPrices(asOf)`, rates via `RateEffectiveAt(…,asOf)`, ledger cut at `asOf`.
4. The chart is **server-rendered inline SVG** (an area + line over the series) on the **Dashboard** (`/`), below the 5.2 KPI cards — **no new dependency, no client JS** for the chart (decision below). It is keyboard-reachable and the range controls are real links (NFR-4 reasonable a11y).
5. **Partial-total honesty (AD-12 / Q5)** carries through: a sample date where a held currency has no rate is a **partial** Net Worth (the unrated currency is excluded, same as the KPI/Net-Worth figure) — surfaced (e.g. a small "some points are partial — missing a historical rate" note), never silently wrong, never inverted/guessed.
6. **Graceful empty/sparse state:** with **fewer than two** plottable points (day-one, or no price/rate history yet) the card shows a calm empty state ("Not enough history yet — add prices/rates over time and your trend will appear") instead of a broken/one-point line. A portfolio-load failure shows the same graceful banner as the KPI row (oversold hint), never a blank page.

### Locked design decisions (read before implementing)

- **D1 — Rendering = inline server-rendered SVG (NO new dependency, NO client JS for the chart).** The valuation service computes the series (dates + Display-Currency Net Worth); the **handler** computes the SVG geometry (viewBox coordinates) and passes a ready-to-render `ChartView` to templ, which emits a static `<svg>` (a filled area `<path>` + a `<polyline>`/`<path>` line + min/max value and start/end date labels). The range toggle is `?range=` **links** (server reload), exactly like the existing archived-accounts toggle (`accountsRedirect`) — no client state. *Rationale:* the app is server-rendered templ + Tailwind with embedded static assets; the only JS today is vendored HTMX. A static SVG keeps the single-binary, no-build-step, render-testable ethos and the clean/fast aesthetic; interactive hover/tooltips are an optional later add. **Do NOT add Chart.js or any JS chart lib.**
- **D2 — The series plots Net Worth** in the Display Currency (not Portfolio Value): it mirrors the dashboard's hero number and UX-DR3's "portfolio value / Net Worth over time." `point[D] = portfolioAsOf(ctx, D).NetWorth`.
- **D3 — Sample dates = the distinct `Price`/`ExchangeRate` effective dates ≤ today (AD-11), plus the window boundary and today.** Reuse the exact "sample date" concept from 5.2's `priorSampleDate` (factor it into a shared helper). For a selected range with start `from`, the plotted series = the sample dates in `[from, today]`, **plus a boundary point at `from`** (so a window whose samples all predate it still starts at the correct as-of value), **plus a point at today** (so the line ends at the current value). Each point is valued **as of its own date** via `portfolioAsOf`. Because `portfolioAsOf(D)` includes all trades with `occurred_on ≤ D`, a buy/deposit between two price/rate samples is reflected at the **next** sample point — consistent with AD-11 (the curve is sampled at price/rate change dates; trades are captured cumulatively, never retro-repriced).
- **D4 — Chart geometry lives in the http/web layer, not the financial core.** Scaling values → pixel/viewBox coordinates is **presentation**, so it stays out of `internal/{money,domain,service,store}` (NFR-5 / `make nofloat` is unaffected — the service returns `money.Money`/dates only; the geometry uses integer viewBox units derived via `decimal` then `.IntPart()`, no float in the core). The service does the financial work (per-date Net Worth via the canonical `domain.NetWorth`, AD-10); the handler does the drawing.

## Tasks / Subtasks

- [x] **Task 1 — Service: the value-over-time series (AC: #1, #3, #5; D2, D3)**
  - [x] In `internal/service/valuation/valuation.go`, **factor the distinct-sample-date scan** currently inside `priorSampleDate` into a shared helper `func (s *Service) sampleDates(ctx context.Context, now time.Time) ([]time.Time, error)` returning the **sorted-ascending, de-duplicated, UTC-date-normalized** `Price` + `ExchangeRate` effective dates that are `≤ today` (reuse `dateOnlyUTC`, `q.ListPrices`, `q.ListExchangeRates`). Re-implement `priorSampleDate` on top of it (its behaviour — second-most-recent ≤ today, ok=false when <2 — must be **unchanged**; keep its tests green).
  - [x] Add the series read-model + method:
    ```go
    // SeriesPoint is one plotted Net Worth value at a historical date, valued
    // as of that date (then-current prices/rates — never today's rate, AD-11).
    type SeriesPoint struct {
        Date    time.Time
        Value   money.Money      // Display Currency Net Worth as of Date
        Partial bool             // a held currency had no rate at Date (partial total)
    }
    // ValueSeries returns the Display-Currency Net Worth series for the window
    // [from, today]. from == zero means "all history" (start at the earliest
    // sample). Points: the sample dates in (from, today], plus a boundary point
    // at from (when from is non-zero and ≤ today), plus today — each valued via
    // portfolioAsOf. Sorted ascending, de-duplicated by date.
    func (s *Service) ValueSeries(ctx context.Context, from time.Time) ([]SeriesPoint, error)
    ```
    Implementation: `now := time.Now()`, `today := dateOnlyUTC(now)`; collect `sampleDates`; build the set of plotted dates = `{d ∈ samples : d.After(fromDay) && !d.After(today)}` ∪ `{today}` ∪ (`from` non-zero ? `{max(fromDay, earliest? )}`… — concretely: include `fromDay` when `!fromDay.After(today)`); for `from == zero` start at the earliest sample (or just `today` if none). De-dup + sort ascending. For each date `D`, `p, err := s.portfolioAsOf(ctx, D)` (reuse — **do not re-derive**); append `SeriesPoint{Date: D, Value: p.NetWorth, Partial: len(p.Missing) > 0}`. Propagate `ErrOversold` like `Portfolio`. The Display currency is the current setting (consistent across points — read once; `portfolioAsOf` already reads it per call, which is fine).
  - [x] Tests (`valuation_test.go`, DB-gated): a fixture with a holding and prices at two past dates (100 then 110) + an FX rate change asserts the series has a point per sample date with the **then-current** value (the older point uses 100, not 110 — proves no retroactive repricing, AD-11); `from` windowing includes a boundary point and excludes older samples; a missing-historical-rate point sets `Partial`; an empty DB yields `< 2` points. Reuse the `isolatedDB` harness and `dateOnlyUTC` for relative dates.

- [x] **Task 2 — HTTP: window selection + SVG geometry (AC: #2, #4, #5, #6; D1, D4)**
  - [x] Extend the `Valuation` interface in `internal/http/router.go` with `ValueSeries(ctx context.Context, from time.Time) ([]valuation.SeriesPoint, error)`.
  - [x] In `dashboardPage`, read `?range=` (`1m`|`3m`|`1y`|`all`, default `all` or `1y` — pick `1y` as a sensible default, document it), map it to a `from` date (`today.AddDate` offsets; `all` → zero `time.Time`), call `deps.Valuation.ValueSeries(ctx, from)`, and build a `web.ChartView` via a `buildChart(points, activeRange)` helper. Add it to `web.DashboardView.Chart`. On `ValueSeries` error, reuse the existing graceful-banner path (the whole dashboard already 500s with a banner on `Dashboard` failure; keep one banner — if `Dashboard` succeeds but `ValueSeries` fails, set an empty chart with a small inline note rather than failing the whole page).
  - [x] `buildChart` (presentation geometry, http layer — **no float in the core**, this is the http package): with `< 2` points set `ChartView{HasData:false, Empty:"…"}`. Otherwise map each point to integer viewBox coords (e.g. viewBox `0 0 1000 300` with padding): `x` spread evenly (or proportional to date) across the width; `y = height − round((value − min)/(max − min) × height)` computed with `decimal` then `.IntPart()` (guard `max == min` → flat mid-line). Emit the `<polyline>` `points` string and an area `<path d>` (line down to the baseline and back). Include `MinLabel`/`MaxLabel` (Display-Currency, via `money.Money.String()`), `StartLabel`/`EndLabel` (dates `YYYY-MM-DD`), `Display`, the active `Range`, the `Ranges` toggle list (`{Key,Label,Active,Href}` — `Href` = `/?range=key`), and `Partial` (any point partial). Keep it integer-clean and dependency-free.
  - [x] Handler tests (`router_test.go`): stub `Valuation.ValueSeries` returning a known multi-point series → `/` (and `/?range=1m`) renders an `<svg>` containing a `polyline`/`path`, the min/max + date labels, the four range links with the active one marked, and the partial note when a point is partial; a `< 2`-point stub → the empty-state copy, no `<svg>` line. Keep all existing dashboard/`/` tests green (the stub `Valuation` now needs `ValueSeries`).

- [x] **Task 3 — Web: render the chart card on the dashboard (AC: #2, #4, #6; D1)**
  - [x] Add view types to `web/shell.go`: `ChartPoint{X,Y int; Date,Value string}`, `ChartRange{Key,Label,Href string; Active bool}`, and `ChartView{HasData bool; Line,Area string; Points []ChartPoint; MinLabel,MaxLabel,StartLabel,EndLabel,Display string; Range string; Ranges []ChartRange; Partial bool; Empty string}`. Add `Chart ChartView` to `DashboardView`.
  - [x] In `web/pages.templ`, render the chart inside an `@Card("")` **below the KPI grid** in `DashboardPage` (when `!view.ErrMsg`): a small header ("Net worth over time" + the Display currency + the range toggle of `<a>` links, the active one styled via `templ.KV` like `navLink`), then either the empty-state paragraph (`!Chart.HasData`) or the `<svg viewBox="0 0 1000 300" role="img" aria-label="Net worth over time">` with the area `<path>` (fill `fill-accent/10` or `text-accent`+opacity), the line `<polyline>`/`<path>` (`stroke` the accent token, `fill="none"`), and min/max + start/end labels (muted `text-label`). Add the partial note (`Chart.Partial`) and keep gain/loss-free styling (this is a neutral value line; use the **accent** token, not gain/loss). Compose the 5.1 `Card`; **no token rename**, **no new CSS** unless a genuinely new utility is required (prefer existing tokens + Tailwind built-ins; rebuild `app.css` only if a class was added).
  - [x] Render tests (`web/shell_test.go` or `web/pages_test.go`, via `renderToString`): a populated `ChartView` renders the `<svg>`, the polyline/area, the labels, four range links (active marked), and the partial note; an empty `ChartView` renders the empty-state copy and no `<svg>` line. DB-free.

- [x] **Task 4 — Wire, verify, docs (AC: all)**
  - [x] `make generate` after the `.templ` edit (commit regenerated `web/pages_templ.go`). `make css` **only if** a new utility class was introduced (rebuild + commit `app.css`); otherwise no CSS change. **No `make sqlc`** (no new query — series reuses `ListPrices`/`ListExchangeRates`/`portfolioAsOf`). `GOTOOLCHAIN=local go build ./... && go vet ./... && go test -count=1 ./...` green (web/http DB-free; DB-gated valuation test runs against local PG :5433). **`make nofloat` stays green** (geometry is http-layer + integer/decimal, no float in the core). `gofmt -l` clean.
  - [x] **Live smoke** (compose db :5433 + freshly-built binary, owner/financas, seeded multi-currency data with ≥2 price/rate effective dates so a line is drawable): log in → `/` shows the KPI row **and** the trend chart below it; click each range link (1M/3M/1Y/All) and confirm the window changes and the active link is marked; confirm a historical point reflects the **then-current** value (resize/inspect); confirm the empty state by pointing at a fresh/one-sample DB; confirm `/investments` and the rest are unchanged. Capture the rendered `<svg>` to confirm a sane path.
  - [x] Update `README.md` (App shell & design tokens / Dashboard note): the dashboard now includes a **server-rendered SVG trend chart** of Net Worth over time, sampled at each Price/Exchange-Rate effective date (derived from history, **no snapshot table**, AD-11), with a `?range=` window toggle; historical points use the **then-current** rate (never retro-repriced, AD-6); no new dependency / no client JS for the chart.
  - [x] **Commit + push to `main`** (trunk-based, one commit per story). `baseline_commit` is HEAD `1469f90`.

## Dev Notes

### Scope boundary — what this story does NOT do (read first)

- **NO allocation breakdown (5.4), NO history widget / insight card (5.5).** This story is **only** the value-over-time trend chart on the dashboard.
- **NO snapshot table, NO migration, NO new store query** (AD-11). The series is **derived** by calling the existing `portfolioAsOf` at each sample date; sample dates come from the existing `ListPrices`/`ListExchangeRates`. No `make sqlc`.
- **NO new dependency, NO JS chart library, NO client-side charting** (D1). Inline server-rendered SVG only; the range toggle is plain `?range=` links. Do not add Chart.js/uPlot/etc., do not add a `<canvas>`, do not add an init `<script>`.
- **NO change to `Portfolio()`/`Dashboard()` public behaviour, NO change to the 5.2 KPI cards.** The `sampleDates` refactor must keep `priorSampleDate` identical (its tests stay green). The chart is **added** below the existing KPI row.
- **NO float in the financial core** (NFR-5). Per-date values are `money.Money` from `domain.NetWorth`; chart geometry (integer viewBox coords) lives in the http/web layer, outside `make nofloat`'s scope.
- **NO gain/loss colour on the trend line** — Net Worth over time is a neutral value series; use the **accent** token, not green/red (those are reserved for gain/loss, 5.1).

### What already exists (reuse, don't rebuild)

[Source: internal/service/valuation/valuation.go; internal/http/router.go; web/pages.templ; web/embed.go; 5-2-dashboard-kpi-cards.md; [[financas-epic5-progress]]]

- **`valuation.Service.portfolioAsOf(ctx, asOf)` (5.2)** is the as-of-date valuation seam this story was built to reuse: it values the whole portfolio (holdings/cash/liabilities) as of any date with prices/rates effective ≤ that date and the ledger cut at that date — **exactly** the AD-11 "value at a historical date, then-current rate" the chart needs. `ValueSeries` calls it once per sample date. **Do not re-derive valuations.**
- **The sample-date concept + `dateOnlyUTC`** already live in `priorSampleDate` (5.2): distinct `Price`+`ExchangeRate` effective dates ≤ today, UTC-date-normalized. Factor the scan into `sampleDates` and share it. `q.ListPrices` / `q.ListExchangeRates` return rows with `EffectiveDate time.Time`.
- **`dashboardPage` + `web.DashboardView` (5.2)** is the page to extend (the chart card goes below `view.Cards`). The graceful-banner + missing/unpriced-notice patterns are established there. `loginSubmit` already lands on `/`.
- **The query-param toggle pattern** is established by the archived-accounts view (`accountsRedirect` / `?archived=`) and the transactions filter — mirror it for `?range=`. The active-state styling mirror is `navLink` (`templ.KV("…", active)`).
- **Static assets are embedded** (`web/embed.go`, `//go:embed all:static`) and served under `/static`; the only JS is vendored `htmx.min.js` loaded in `web/shell.templ`. The SVG chart needs **none** of this — it's inline markup. [Source: web/embed.go; web/shell.templ]
- **5.1 primitives + tokens:** compose `@Card`; use `text-accent`/`bg-accent`, `text-label`, `text-muted`, `rounded-card`. SVG uses `stroke="currentColor"`/token classes. Render-test via `renderToString` (`web/shell_test.go`).
- **Build/codegen:** `.templ` → `make generate` (commit `*_templ.go`); CSS only if a token/utility is added (`make css`); `make nofloat` stays green. [Source: Makefile]

### Architecture invariants this story must honor

- **AD-11 — value-over-time derived from history, NO snapshot table.** The series is sampled at the effective-dated Price/ExchangeRate change dates and valued as-of; never materialized into a snapshot row, never retro-recomputed at today's rate. [Source: ARCHITECTURE-SPINE.md#AD-11]
- **AD-6 — owner-entered effective-dated prices/rates, no inversion.** Historical points use the row effective ≤ that date; a missing pair → that point is partial (excluded), never inverted/guessed — inherited from `portfolioAsOf`/`domain.NetWorth`. [Source: #AD-6; [[financas-epic4-decisions]]]
- **AD-10 — one canonical home per derived figure.** Each point's Net Worth is `domain.NetWorth` (via `portfolioAsOf`); the http layer does no financial math — only chart geometry. [Source: #AD-10]
- **AD-1 — web/http render only.** The service returns dates + `money.Money`; the handler/templ turn them into SVG coordinates and labels (presentation). [Source: #AD-1]
- **AD-12 — convert-then-sum / partial totals.** Per-date Net Worth uses the existing round-once conversion; partial (missing-rate) points are surfaced, not hidden. [Source: #AD-12]
- **NFR-5 — no float in the financial core.** Geometry is http-layer + integer/decimal; `make nofloat` (scope `internal/{money,domain,service,store}`) stays green. [Source: Makefile `nofloat`]
- **NFR-4 — reasonable a11y.** SVG has `role="img"` + `aria-label`; the range toggle is real focusable links; the line uses a neutral accent (not colour-coded gain/loss). [Source: prd.md §"Cross-Cutting NFRs"]

### Design intent (from the PRD/UX — apply here)

[Source: epics.md UX-DR3; prd.md §4.5 FR-12, §"Aesthetic & Tone"]

- **UX-DR3 — Primary trend chart:** a prominent **area/line** chart of portfolio value / Net Worth over time, with a **range toggle (e.g. monthly)**. This is the dashboard's centerpiece visual under the KPI row — clean, fast, uncluttered; the value line is the focus, axes/labels are muted.
- **FR-12 / PRD §4.5:** the chart plots the Display-Currency value **snapshotted at each point using the then-current Price and Exchange Rate — not retroactively recomputed at today's rate.** `[ASSUMPTION: Price/Rate history accrues going forward; no backfill of pre-app history in v1]` — so a brand-new install shows little/no curve until history accrues (the empty/sparse state, AC #6). **Performance attribution (time-weighted vs money-weighted) is explicitly out of scope** — v1 is simple value-over-time only.

### Previous-story intelligence (5.2 + 5.1/4.4) — load-bearing

[Source: 5-2-dashboard-kpi-cards.md; [[financas-epic5-progress]]; [[financas-epic4-decisions]]]

- **5.2 built `portfolioAsOf` precisely so 5.3 could walk it across sample dates** — this is the intended reuse; do not re-implement valuation. The `dateOnlyUTC` date-normalization and the "distinct sample dates ≤ today" logic are already correct and timezone-stable (fixed in 5.2 review) — share them.
- **5.2 review lessons to not repeat:** (a) the `Amount` primitive prepends its own +/− — but the trend line is **neutral** (no sign/colour), so this doesn't apply here; the **axis value labels** use `money.Money.String()` directly (signed only if Net Worth is negative, which is fine on an axis). (b) Period semantics: "samples ≤ today, second-most-recent" — the series uses the **same sample set**; keep the shared helper consistent.
- **Partial-total honesty is a pinned Epic-4 invariant** ([[financas-epic4-decisions]]): a historical point missing a rate is partial — surface it, never block or guess.
- **House style:** typed view structs in `web/shell.go` (pre-formatted strings + ints); table-of-`want`-substring render/handler tests; DB-gated service tests skip without `TEST_DATABASE_URL`/`DATABASE_URL`; `GOTOOLCHAIN=local`; commit regenerated `*_templ.go`; `make nofloat`/`gofmt` green; local DB host **5433**; dev login `owner`/`financas`; **one commit per story, push to `main`**.

### Project Structure Notes

- **Modified — service:** `internal/service/valuation/valuation.go` (`sampleDates` helper + `priorSampleDate` re-impl; `SeriesPoint` type + `ValueSeries`); `internal/service/valuation/valuation_test.go`.
- **Modified — http:** `internal/http/router.go` (`Valuation` interface `+ValueSeries`; `dashboardPage` reads `?range=`, builds `ChartView` via `buildChart`); `internal/http/router_test.go` (stub `ValueSeries`, chart handler tests).
- **Modified — web:** `web/shell.go` (`ChartPoint`/`ChartRange`/`ChartView` + `DashboardView.Chart`); `web/pages.templ` (chart card in `DashboardPage`) → regenerated `web/pages_templ.go`; `web/shell_test.go`/render test. CSS only if a utility was added (`web/static/css/input.css` + rebuilt `app.css`).
- **Modified — docs:** `README.md`.
- **NOT touched:** no `db/migrations`, no `db/query`, no `internal/store` regen (no `make sqlc`), no new module dependency, no new JS asset, no `web/embed.go` change.

### Testing standards

- **service (DB-gated):** `ValueSeries` — point-per-sample-date with **then-current** value (older point uses the old price/rate, proving no retro-reprice — AD-11); `from` windowing (boundary point included, older samples excluded); `Partial` on missing historical rate; `< 2` points on empty/one-sample DB. `priorSampleDate` unchanged (its tests stay green).
- **http (DB-free, stub):** `/` and `/?range=1m` render the `<svg>` (polyline/area + labels), the four range links with the active marked, the partial note when applicable; `< 2` points → empty state, no `<svg>` line; existing dashboard tests stay green.
- **web (pure render):** `ChartView` populated → `<svg>` + line/area + labels + range links; empty → empty-state copy.
- `go test ./...` green (web/http DB-free; DB-gated valuation test skips without DB). `go vet`, `gofmt -l`, `make nofloat` clean. Visual confirmed by the live smoke.

### References

- [Source: epics.md#Story 5.3] — ACs (Display-Currency value sampled at each input-change date, derived from history no snapshot; range toggle; never retro-recomputed at today's rate); FR-12, UX-DR3
- [Source: epics.md UX-DR3] — primary area/line trend chart with range toggle
- [Source: prd.md §4.5 FR-12] — then-current price+rate snapshotting; no backfill; perf-attribution out of scope
- [Source: ARCHITECTURE-SPINE.md#AD-11] — value-over-time derived from effective-dated history, **no snapshot table**, sampled at each input-change date, never retro-recomputed; [#AD-6/#AD-10/#AD-1/#AD-12/NFR-5/NFR-4]
- [Source: internal/service/valuation/valuation.go] — `portfolioAsOf` (the as-of seam to walk), `priorSampleDate`/`dateOnlyUTC` (the sample-date logic to share); [internal/http/router.go `dashboardPage`] — the page to extend, the `?range=` toggle pattern (`accountsRedirect`)
- [Source: web/pages.templ `DashboardPage`; web/shell.go `DashboardView`] — where the chart card slots in; [web/embed.go; web/shell.templ] — embedded static assets / vendored htmx (chart needs neither)
- [Source: 5-2-dashboard-kpi-cards.md; [[financas-epic5-progress]]] — the `portfolioAsOf` seam + sample-date semantics built for this story; review lessons
- [Source: Makefile] — `make generate` / `make nofloat`; committed-artifact rule; no `make sqlc`

## Dev Agent Record

### Agent Model Used

claude-opus-4-8[1m] (Opus 4.8, 1M context) — bmad-dev-story workflow.

### Debug Log References

- TDD: service `ValueSeries` tests + `buildChart` handler tests + `renderToString` chart render tests written alongside each layer; all green.
- **Latent timezone bug found & fixed in the shared as-of seam:** while running the suite near the UTC-midnight boundary (local 21:00 −03 = next-day 00:xx UTC), `TestDashboardAsOfAndDeltas` failed on the **committed HEAD too** (verified via `git stash`) — proving it pre-dated 5.3. Root cause: `portfolioAsOf` passed the **raw** `asOf` (a local-zoned `time.Now()`) to `latestPrices`/`buildRates`, whose SQL casts `$1::date` in the DB session TZ, so a local-evening `now` resolved to the *local* date and excluded a price/rate effective on the UTC date — while the ledger cut already used `dateOnlyUTC`. Fixed by passing `dateOnlyUTC(asOf)` to the price/rate reads too, so the ledger, price, and rate cuts all agree on one UTC calendar date. Deterministic now regardless of wall-clock/timezone.
- `make generate` (templ) produced no `internal/store` diff (no new query — series reuses `ListPrices`/`ListExchangeRates`/`portfolioAsOf`). `make css` added the new `fill-accent`/`stroke-accent` SVG utilities to `app.css`.

### Completion Notes List

- **Independent Opus review: APPROVE** (no Critical/High). Applied the fast-follows before commit: **[Medium]** the chart now shows a distinct "couldn't load your trend" message on a `ValueSeries` error instead of reusing the "not enough history" empty copy (error ≠ no-data); **[Low]** `ValueSeries` no longer adds a pre-history boundary point when the window predates all samples (avoids a flat-zero lead-in on young portfolios with the default 1Y window) — regression-tested; **[Nit]** renamed `min`/`max` → `lo`/`hi` in `buildChart` (avoid shadowing the Go builtins); plus a drive-by `gofmt` of the pre-existing `internal/domain/balance_test.go`. Deferred (documented Low): pinning the pgx pool `timezone=UTC` for belt-and-suspenders `::date` robustness — the dev/Azure DB session is already UTC.

- **Service (Task 1):** factored the distinct-sample-date scan into `sampleDates` (ascending, UTC-date-normalized, ≤ today) and re-implemented `priorSampleDate` on it (behaviour identical — 5.2 delta tests stay green). Added `SeriesPoint` + `ValueSeries(ctx, from)`: the windowed Net Worth series (sample dates in the window + window boundary + today), each valued **as of its own date** via `portfolioAsOf` (then-current prices/rates — AD-11). Partial flag from `len(Missing)>0`. **Also fixed the as-of UTC-date normalization bug above.**
- **HTTP (Task 2):** `Valuation` interface `+ValueSeries`; `dashboardPage` reads `?range=` (`chartRange` default `1y`, `chartFrom` → window start), calls `ValueSeries`, and `buildChart` computes the SVG geometry (integer viewBox coords via `decimal.IntPart`, no float in the core, AD-1/NFR-5): the line `<polyline>` points, filled-area `<path>`, per-point coords (with native `<title>` hover), min/max + start/end labels, the range toggle, and the partial note. `<2` points → empty state. A `ValueSeries` error degrades to an empty chart (the page still renders the KPIs).
- **Web (Task 3):** `ChartPoint`/`ChartRange`/`ChartView` + `DashboardView.Chart`; `trendChart` templ renders the chart card below the KPI grid — accent-coloured `<svg>` (a neutral value line, not gain/loss), range links (active marked via `aria-current` + `bg-accent/10`), labels, partial note, empty state. New `fill-accent`/`stroke-accent` utilities → `app.css` rebuilt. No token rename, no new dependency, no client JS.
- **Verification (Task 4):** `go build`/`vet`/`test -count=1 ./...` green incl. DB-gated valuation tests on local PG :5433; `make nofloat` green; `gofmt` clean. **Live smoke** (freshly-built binary, owner/financas, dev DB): `/` renders the KPI row + the trend `<svg>` (5 data-point circles, accent area+line) with the range toggle (1Y active by default); `/?range=1m` correctly activates 1M; historical points use then-current values. `/investments` unchanged.

### File List

- `internal/service/valuation/valuation.go` (modified — `sampleDates` helper + `priorSampleDate` re-impl; `SeriesPoint` + `ValueSeries`; **as-of UTC-date normalization fix** in `portfolioAsOf`)
- `internal/service/valuation/valuation_test.go` (modified — `TestValueSeries`, `TestValueSeriesPartialAndEmpty`)
- `internal/http/router.go` (modified — `Valuation` interface `+ValueSeries`; `dashboardPage` `?range=`; `chartRange`/`chartFrom`/`buildChart` + viewBox consts; `fmt` import)
- `internal/http/router_test.go` (modified — `stubValuation.ValueSeries` + `cannedSeries`; chart handler tests)
- `web/shell.go` (modified — `ChartPoint`/`ChartRange`/`ChartView` + `DashboardView.Chart`; `itoa` helper)
- `web/pages.templ` (modified — `trendChart` component + chart card in `DashboardPage`)
- `web/pages_templ.go` (regenerated)
- `web/shell_test.go` (modified — chart render tests)
- `web/static/css/app.css` (rebuilt — `fill-accent`/`stroke-accent`)
- `README.md` (modified — trend-chart note)
- `internal/domain/balance_test.go` (modified — drive-by `gofmt`, pre-existing flag noted in review)

## Change Log

| Date | Change |
| --- | --- |
| 2026-06-29 | Story 5.3 implemented (dev-story): added a **server-rendered SVG value-over-time trend chart** of Net Worth to the dashboard, sampled at each Price/Exchange-Rate effective date and valued as-of via the 5.2 `portfolioAsOf` seam (derived from history, **no snapshot table / no new query / no migration**, AD-11; then-current rate, AD-6). `sampleDates` factored out + shared with `priorSampleDate`; new `ValueSeries`/`SeriesPoint`; `?range=` toggle + `buildChart` SVG geometry (integer/decimal, core stays float-free); `ChartView` + `trendChart` templ. **Fixed a latent timezone bug** in the shared as-of seam (`portfolioAsOf` now normalizes the price/rate reads to `dateOnlyUTC(asOf)`, matching the ledger cut — deterministic across the UTC-midnight boundary). Build/vet/test (incl. DB-gated)/nofloat/gofmt green; live smoke confirmed the chart + range toggle. Status → review. |
| 2026-06-29 | Story 5.3 drafted (create-story): add a **server-rendered SVG value-over-time trend chart** of **Net Worth** to the dashboard, sampled at each distinct Price/Exchange-Rate effective date and valued as-of via the 5.2 `portfolioAsOf` seam (derived from history, **no snapshot table / no new query / no migration**, AD-11; never retro-recomputed at today's rate, AD-6). Decisions: **D1** inline SVG, no JS chart lib, `?range=` link toggle; **D2** plot Net Worth; **D3** sample = distinct price/rate dates ≤ today + window boundary + today (share the scan with `priorSampleDate`); **D4** chart geometry in the http/web layer (integer/decimal viewBox coords), financial core stays float-free (NFR-5). New `valuation.ValueSeries` + `SeriesPoint`; `dashboardPage` `?range=` + `buildChart`; `ChartView` in `web/shell.go`; chart card in `DashboardPage`. Partial (missing-rate) points surfaced; <2 points → graceful empty state. Status → ready-for-dev. |
