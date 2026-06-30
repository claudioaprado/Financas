---
baseline_commit: 3e2e4f4
---

# Story 5.4: Allocation breakdown

Status: review

<!-- Note: Validation is optional. Run validate-create-story for quality check before dev-story. -->

## Story

As the owner,
I want to see how my portfolio is allocated,
so that I understand my mix.

## Acceptance Criteria

From `epics.md` → Epic 5 → Story 5.4 (realizes **FR-12**, **UX-DR4**; honors **AD-10**, **AD-12**, **AD-5**, **AD-6**, **NFR-4**, **NFR-5**). **Given** holdings exist, **When** the allocation card renders on the dashboard, **Then**:

1. **It breaks down the invested value by Security and/or Account** (FR-12, UX-DR4). The breakdown dimension is chosen by a `?by=security|account` toggle (default `security`); each slice is one security symbol (across accounts) or one investment account, in the **Display Currency**.
2. **"Invested value" means priced holdings only** — the allocation base is the **Portfolio Value** (Σ converted priced-holding market value), the same canonical figure shown on the KPI row / `/investments`. **Cash, investment-account cash balances, and liabilities are excluded** (this is allocation of the *invested* portfolio, not Net Worth). Unpriced holdings contribute nothing (consistent with Portfolio Value) and are surfaced via the existing "no price" notice, never silently dropped.
3. **The percentages are computed from the UNROUNDED converted values and reconcile to EXACTLY 100%** (AD-12). Reconciliation uses the **largest-remainder method** at integer-percent scale (floor each share, then hand the leftover whole-percent units to the largest fractional remainders) — **no float, no naive round-each-then-hope**. Integer slice percents sum to exactly 100 by construction.
4. **The breakdown is rendered as a server-rendered inline SVG donut + a legend** (UX-DR4 "bar/donut visual"), on the **Dashboard** (`/`), below the 5.3 trend chart. **No new dependency, no client JS** (decision D1). The donut arcs use the **reconciled percents** so the ring is whole (sums to a full circle) and matches the legend. It is keyboard-reachable; the `?by=` toggle is real `<a>` links (NFR-4 reasonable a11y); the donut carries `role="img"` + an `aria-label`.
5. **Partial-total honesty (AD-12 / AD-6 / Q5) carries through:** a held currency with no rate to the Display Currency on the valuation date is **excluded** from the allocation (it cannot be converted) and surfaced with the same partial notice used by the KPI row / trend chart — never inverted, guessed, or silently included. The allocation `Total` shown is the canonical **Portfolio Value** (round-once), so it reconciles with the KPI card.
6. **Graceful empty state:** with **no priced invested holdings** (a fresh portfolio, an all-cash portfolio, or every holding unpriced/unrated) the card shows a calm empty state ("No invested holdings to allocate yet — add securities, transactions and prices and your mix will appear here.") instead of a broken/zero donut. A portfolio-load failure degrades to a distinct "couldn't load" message on the card (error ≠ no-data), exactly like the 5.3 trend chart — the rest of the dashboard still renders.

### Locked design decisions (read before implementing)

- **D1 — Rendering = inline server-rendered SVG donut + HTML legend (NO new dependency, NO client JS).** The `domain` layer computes the allocation (slices + reconciled percents + converted values); the **handler** computes the donut geometry (per-slice `stroke-dasharray`/`stroke-dashoffset` on a ring) and passes a ready-to-render `AllocationView` to templ, which emits a static `<svg>` of concentric/overlaid `<circle>` arcs + an HTML legend (colour swatch, key, percent, Display-Currency value). The dimension toggle is `?by=` **links** (server reload), exactly like the 5.3 `?range=` toggle and the archived-accounts toggle — no client state. *Rationale:* matches the server-rendered templ + Tailwind, single-binary, no-build-step, render-testable ethos that 5.3 established. **Do NOT add Chart.js / any JS chart lib / a `<canvas>` / an init `<script>`.**
- **D2 — Donut via `stroke-dasharray` (NO trigonometry).** Each slice is a `<circle r=R>` sharing the same centre, drawn as an arc by setting `stroke-dasharray="arc gap"` where `arc = percent/100 × C`, `C = 2πR` (the circumference), and `stroke-dashoffset = −(cumulativePriorPercent/100 × C)`, with the whole ring rotated `-90°` (transform) so it starts at 12 o'clock and runs clockwise. **π is the only irrational constant** (a documented `decimal` literal in the http layer); no `math.Sin/Cos`. *Rationale:* the classic dependency-free SVG-donut technique; keeps the geometry simple and avoids polar-arc path math + large-arc flags.
- **D3 — Allocation is a canonical `domain` function (AD-10).** AD-10 explicitly names **allocation** as a derived figure that must have exactly one pure home in `domain`. Add `domain.Allocate(display, items, rates) Allocation`: it does **convert-then-sum** per item (AD-12), groups by key, computes percents from the **unrounded** converted group totals, and reconciles them to exactly 100 via **largest-remainder**. The `service` feeds it authored inputs; the `http` layer does **no** financial math — only donut geometry + formatting. **No float in the financial core (NFR-5)** — `Allocate` is pure `decimal`.
- **D4 — The allocation base reconciles to Portfolio Value.** Slices use the **same rates and the same convert-then-sum/round-once policy** as `domain.NetWorth`, valued **as of now** (today's effective prices/rates — this is the *current* mix, not a historical one; AD-11's "no retro-reprice" concerns the time series, not today's snapshot). So `Σ unrounded slice values == unrounded Portfolio Value`, and the card's `Total` is the canonical `PortfolioValue` (round-once). Per-slice **displayed** values are each `Rounded()` and MAY differ from `Total` by a sub-unit rounding residual — that is expected; the **percents** (not the rounded value labels) are what reconcile to exactly 100% (AC #3). Document this in the view.
- **D5 — Legibility cap → "Other".** To keep the donut + legend readable, show at most **`allocTopN = 8`** named slices (sorted by converted value descending, then key ascending for determinism); aggregate the remaining tail into a single **"Other"** slice. "Other" is reconciled into the largest-remainder pass like any slice, so the displayed percents still sum to exactly 100. With ≤ 8 groups there is no "Other". Slices are deterministically ordered so the donut, legend, and percent reconciliation are stable across renders.

## Tasks / Subtasks

- [x] **Task 1 — Domain: the canonical allocation function (AC: #1, #2, #3, #5; D3, D5)**
  - [x] Add `internal/domain/allocation.go` — the single canonical home for allocation (AD-10). Types + function:
    ```go
    // AllocItem is one priced holding's contribution to the invested-value
    // allocation: a grouping key (label) and its native market value. Callers
    // (service) supply only priced holdings; unpriced ones are not invested value.
    type AllocItem struct {
        Key   string      // grouping label: security symbol OR account name
        Value money.Money // native market value (qty×price)
    }
    // AllocSlice is one group's share of invested value: the converted
    // Display-Currency value (rounded once for display) and the reconciled
    // integer percent. The Percent values across all slices sum to EXACTLY 100.
    type AllocSlice struct {
        Key     string
        Value   money.Money     // Display Currency, round-once
        Percent int             // reconciled; Σ Percent == 100 (largest-remainder)
    }
    // Allocation is the invested-value breakdown in the Display Currency. Total is
    // the round-once converted invested value (== domain.NetWorth's PortfolioValue
    // for the same inputs/rates). Missing lists native currencies excluded for lack
    // of a rate (partial total, AD-6). Slices are sorted by value desc, then key.
    type Allocation struct {
        Slices  []AllocSlice
        Total   money.Money
        Missing []money.Currency
    }
    func Allocate(display money.Currency, items []AllocItem, rates map[money.Currency]decimal.Decimal) Allocation
    ```
  - [x] Implementation (pure `decimal`, no float — NFR-5): for each item, convert native→Display at **full precision** via `money.Convert` when `cur == display` (as-is) or a rate exists; a non-zero item in an unrated currency is **skipped** and its currency recorded in `Missing` (mirror `domain.NetWorth`'s `convertSum`/`hasRate` exactly — same dedup+sort-by-code). Accumulate the **unrounded** converted value per key in a map; track first-seen order or just sort at the end. `totalUnrounded = Σ group unrounded`. If `totalUnrounded` is zero (or no items) → return `Allocation{Total: money.New(0, display).Rounded()}` with no slices. Build slices: `Value = money.New(groupUnrounded, display).Rounded()`; sort by `groupUnrounded` desc then `Key` asc. **Largest-remainder reconciliation to 100**: `share_i = groupUnrounded_i / totalUnrounded × 100` (full precision); `floor_i = share_i.Floor()`; `assigned = Σ floor_i`; `remaining = 100 − assigned`; sort indices by fractional remainder `(share_i − floor_i)` desc (tie-break: larger `groupUnrounded`, then earlier sort index) and give `+1` to the first `remaining` of them. Set `Percent_i = int(floor_i) + bump`. `Total = money.New(totalUnrounded, display).Rounded()`. **Assert by construction** `Σ Percent == 100`.
  - [x] **Apply the D5 "Other" cap in `domain` OR `service`** — lock it in `domain.Allocate` is cleanest (keeps reconciliation in one place): after grouping+sorting, if `len(groups) > allocTopN` (pass `allocTopN` as a parameter, or define a sensible package const and a variant — prefer a parameter `topN int`, `0 = no cap`), fold groups `[topN:]` into one `{Key:"Other", value:Σtail}` **before** the largest-remainder pass, so percents reconcile over the capped set. (If you keep `Allocate` cap-free and apply the cap in the service, you must re-run largest-remainder after folding — do NOT just sum already-rounded percents, that breaks the 100% guarantee. Keeping it inside `Allocate(topN)` avoids this footgun.)
  - [x] Tests (`internal/domain/allocation_test.go`, **pure, DB-free**): (a) two same-currency holdings 30/70 → percents 30/70 sum 100, values converted; (b) a **largest-remainder** case that naive rounding would break, e.g. three equal thirds (33,33,34 summing to 100, never 33/33/33=99) and a 1/3·1/3·1/3-style fractional set — assert `Σ Percent == 100` exactly and the remainder went to the largest fractional remainders deterministically; (c) **multi-currency convert-then-sum**: holdings in USD + BRL with a USD→Display rate, BRL→Display rate → percents from unrounded converted values, `Total` equals the round-once sum (matches what `NetWorth.PortfolioValue` would give for the same holdings/rates); (d) a holding in an **unrated** currency → excluded, currency in `Missing`, percents reconcile over the remaining slices; (e) **empty / all-zero** → no slices, `Total` zero; (f) **Other cap**: `topN=2` with 5 groups → 2 named + "Other", `Σ Percent == 100`. Use `decimal`/`money.New` fixtures; no DB.

- [x] **Task 2 — Service: feed the domain function from the portfolio (AC: #1, #2, #5; D3, D4)**
  - [x] In `internal/service/valuation/valuation.go`, add the read-model + method:
    ```go
    // AllocationGroup is one allocation slice formatted for the read model: the
    // grouping key, the reconciled integer percent, and the Display-Currency value.
    type AllocationGroup struct {
        Key     string
        Percent int
        Value   money.Money // Display Currency
    }
    // Allocation is the invested-value breakdown (Story 5.4, FR-12/UX-DR4): the
    // current portfolio's priced holdings grouped by Security or Account, converted
    // to the Display Currency and reconciled to exactly 100% (AD-12). Total is the
    // canonical Portfolio Value; Missing/Unpriced carry the same partial notices as
    // Portfolio. By is the active dimension ("security" | "account").
    type Allocation struct {
        Groups   []AllocationGroup
        Total    money.Money
        Display  money.Currency
        By       string
        Missing  []money.Currency
        Unpriced []string
    }
    // Allocation derives the invested-value breakdown as of now for the given
    // dimension ("security" default, or "account"). It reuses the portfolio
    // valuation (priced holdings, native market value) and the same native→Display
    // rates as Net Worth, then delegates the grouping + 100%-reconciliation to the
    // canonical domain.Allocate (AD-10). ErrOversold propagates like Portfolio.
    func (s *Service) Allocation(ctx context.Context, by string) (Allocation, error)
    ```
  - [x] Implementation (reuse, do **not** re-derive valuations): `p, err := s.Portfolio(ctx)` gives the priced `Holdings` (native `Valuation`, `Currency`, `Symbol`, `AccountName`, `HasPrice`), `Display`, `PortfolioValue`, `Missing`, `Unpriced`. Build `[]domain.AllocItem` from holdings where `HasPrice` — `Key = h.Symbol` when `by=="account"? h.AccountName : h.Symbol`; `Value = h.Valuation` (native market value). Build the rate map for the items' currencies by reusing `s.buildRates(ctx, store.New(s.pool), display, dateOnlyUTC(time.Now()), nativeValues)` (the same effective-date rates Net Worth used — this is what makes `Total` reconcile to `PortfolioValue`, D4). Call `domain.Allocate(display, items, rates /*, allocTopN*/)`. Map `domain.Allocation` → service `Allocation` (Groups, `Total`, `Display`, `By: normalized`, `Missing: p.Missing` — **prefer the portfolio's Missing**, which is the authoritative partial set; `Unpriced: p.Unpriced`). Normalize `by` to `security`/`account` (default `security`) — define an exported helper or normalize in the handler; keep one source of truth.
  - [x] **Note on the extra rate read:** reusing `Portfolio()` then calling `buildRates` again issues the rate lookups a second time (owner-scale data, read-only — negligible, same pattern as `Dashboard` calling `portfolioAsOf` twice and `ValueSeries` calling it N times). Do **not** refactor `portfolioAsOf` to expose its internal rates unless it stays trivially clean; the second `buildRates` call is the locked, lowest-risk approach.
  - [x] Tests (`valuation_test.go`, **DB-gated**, reuse `isolatedDB`/`dateOnlyUTC`): a fixture with two priced holdings in two accounts/securities (same currency) asserts `Allocation(ctx,"security")` groups by symbol with reconciled percents summing 100 and `Total == PortfolioValue`; `Allocation(ctx,"account")` groups by account; a multi-currency fixture with one currency **unrated** asserts that holding is excluded and its currency is in `Missing`; an all-cash / no-priced-holdings DB yields no groups; `ErrOversold` propagates. Skip without `TEST_DATABASE_URL`/`DATABASE_URL`.

- [x] **Task 3 — HTTP: dimension selection + donut geometry (AC: #1, #3, #4, #5, #6; D1, D2, D5)**
  - [x] Extend the `Valuation` interface in `internal/http/router.go` with `Allocation(ctx context.Context, by string) (valuation.Allocation, error)`.
  - [x] In `dashboardPage`, after the chart block: read `?by=` (`allocBy(req.URL.Query().Get("by"))` → `security`|`account`, default `security`), call `deps.Valuation.Allocation(ctx, by)`, and build `view.Allocation = buildAllocation(alloc, by, rng)` (pass the active `rng` so the `?by=` links **preserve** the current `?range=`). On error, degrade to an empty allocation card with the distinct "couldn't load" copy (mirror the chart's `sErr` handling) — the rest of the dashboard still renders.
  - [x] `buildAllocation` (presentation geometry, http layer — **no float in the core**, this is the http package; π is a documented local `decimal` constant): with no groups → `AllocationView{HasData:false, Empty:"…"}`. Otherwise, for the donut ring of radius `R` (e.g. `R=70`, viewBox `0 0 200 200`, centre `100,100`, stroke width ~`32`): `C = 2πR` (decimal); for each slice in order compute `arc = percent/100 × C`, `dashArray = fmt("%s %s", arc, C−arc)`, `dashOffset = −(cumulativePrior/100 × C)` (format decimals to ~3dp — SVG accepts decimal lengths), and a colour-token index `i mod len(palette)`. Emit per-slice `AllocSliceView{DashArray, DashOffset string; ColorClass string; Key string; Percent int; Value string}` plus the legend fields. Include `Display`, the active `By`, the `Bys` toggle list (`{Key,Label,Active,Href}` where `Href = "/?range="+rng+"&by="+key`), `Total` (Display-Currency string), and `Partial` (len(Missing)>0) + `MissingCodes`. Keep it dependency-free; π is the only constant. Define `allocBy`, the palette token list, and the geometry consts (`allocR`, `allocStroke`, viewBox) next to `chartRange`/`buildChart`.
  - [x] **Preserve `?by=` on the chart range links too (cheap correctness):** update `buildChart`'s `Href` to `"/?range="+key+"&by="+activeBy` so switching the range keeps the chosen dimension (pass the active `by` into `buildChart`, or post-process the hrefs in the handler). Keep all 5.3 chart tests green — adjust the expected hrefs in `router_test.go` accordingly. (If this turns out to ripple more than trivially, fall back to range-only chart hrefs and document that switching range resets `by` to default — but prefer preserving both.)
  - [x] Handler tests (`router_test.go`): extend `stubValuation` with `Allocation` (a `cannedAllocation`); `/` and `/?by=account` render the donut `<svg>` with one `<circle>` arc per slice, the legend rows (key + percent + value), the two `?by=` links with the active one marked and **carrying the active `range`**, the `Total`, and the partial note when a currency is missing; a no-groups stub → the empty-state copy, no donut `<svg>`. Keep existing dashboard/chart/`/` tests green (the stub `Valuation` now needs `Allocation`; the chart hrefs may now include `&by=security`).

- [x] **Task 4 — Web: render the allocation card on the dashboard (AC: #1, #4, #6; D1, D2)**
  - [x] Add view types to `web/shell.go`: `AllocSliceView{DashArray, DashOffset, ColorClass, Key string; Percent int; Value string}`, `AllocBy{Key, Label, Href string; Active bool}`, and `AllocationView{HasData bool; Slices []AllocSliceView; Total, Display string; By string; Bys []AllocBy; Partial bool; MissingCodes string; Empty string}`. Add `Allocation AllocationView` to `DashboardView`.
  - [x] In `web/pages.templ`, add an `allocationCard(a AllocationView)` component and render it inside an `@Card("mt-4")` **below `@trendChart`** in `DashboardPage` (when `!view.ErrMsg`), before the Missing/Unpriced notices. Header: "Allocation" + `(Display)` + the `?by=` toggle (`<a>` links styled exactly like the trend range toggle — `templ.KV("bg-accent/10 text-accent font-medium", Active)` + `aria-current`). Body: either the empty-state paragraph (`!HasData`) or a flex row of the donut `<svg viewBox="0 0 200 200" role="img" aria-label="Portfolio allocation">` (a faint full-ring background `<circle>` + one arc `<circle>` per slice with `stroke-dasharray`/`stroke-dashoffset`/`ColorClass`, `fill="none"`, `transform="rotate(-90 100 100)"`, plus a centre `<text>` showing `Total`) and an HTML legend `<ul>` (each row: a colour swatch `<span>` with the slice `ColorClass`, the `Key`, the `Percent`%, and the `Value`). Add the partial note (`Partial` → reuse the "totals exclude …" copy with `MissingCodes`). Compose the 5.1 `@Card`. Use the **5.1 design tokens**; add a small **categorical palette** of allocation colours as utilities (see Task 5) — do **not** reuse gain/loss (reserved). No token rename.
  - [x] Render tests (`web/shell_test.go` / `pages_test.go`, via `renderToString`, **DB-free**): a populated `AllocationView` renders the donut `<svg>`, one `<circle>` arc per slice, the legend rows (key/percent/value), the `Total`, two `?by=` links (active marked), and the partial note; an empty `AllocationView` renders the empty-state copy and no donut `<svg>`.

- [x] **Task 5 — Wire, verify, docs (AC: all)**
  - [x] **Categorical palette:** add a small set (≈6–8) of allocation slice colours to `web/static/css/input.css` as `stroke-*`/`bg-*`/`fill-*` utilities (for the donut arcs + legend swatches), within the 5.1 token aesthetic (distinct, calm hues; NOT gain/loss green/red). `make css` to rebuild + commit `web/static/css/app.css`. (The 5.3 `fill-accent`/`stroke-accent` utilities already exist — extend, don't rename.)
  - [x] `make generate` after the `.templ` edit (commit regenerated `web/pages_templ.go`). **No `make sqlc`** (no new query — allocation reuses `Portfolio()`/`buildRates`/existing reads). `GOTOOLCHAIN=local go build ./... && go vet ./... && go test -count=1 ./...` green (web/http DB-free; DB-gated valuation test runs against local PG :5433). **`make nofloat` stays green** (`domain.Allocate` is pure `decimal`; donut geometry/π lives in the http layer, outside the `internal/{money,domain,service,store}` scope). `gofmt -l` clean.
  - [x] **Live smoke** (compose db :5433 + freshly-built binary, owner/financas, seeded multi-currency data: ≥2 securities priced across ≥2 accounts so a donut is drawable): log in → `/` shows the KPI row + trend chart **and** the allocation donut + legend below it; the legend percents **sum to exactly 100**; the `Total` matches the Portfolio Value KPI; click `?by=account` and confirm the breakdown regroups and the active link is marked **and the `range` is preserved**; confirm the empty state on a fresh/all-cash DB; confirm a missing-rate currency is excluded with the partial note; confirm `/investments` and the rest are unchanged. Capture the rendered donut `<svg>` to confirm sane arcs.
  - [x] Update `README.md` (Dashboard note): the dashboard now includes a **server-rendered SVG allocation donut** of invested value (Portfolio Value) broken down **by Security or Account** via a `?by=` toggle, with percentages computed from **unrounded** converted values and **reconciled to exactly 100%** (largest-remainder, AD-12); missing-rate currencies are excluded with a partial notice; no new dependency / no client JS.
  - [x] **Commit + push to `main`** (trunk-based, one commit per story). `baseline_commit` is HEAD `3e2e4f4`.

## Dev Notes

### Scope boundary — what this story does NOT do (read first)

- **NO transaction-history widget / insight call-out (5.5).** This story is **only** the allocation breakdown card on the dashboard. 5.5 follows.
- **NO spending-by-Category breakdown.** UX-DR4 mentions it as *optional*; this story does **invested-value** allocation (holdings) only, per the 5.4 AC ("breaks down invested value by Security and/or Account"). Category spend is out of scope.
- **NO Net-Worth / cash / liabilities allocation.** The base is **Portfolio Value (invested value)** — cash, investment-cash balances, and credit liabilities are excluded (D-scope, AC #2). This is *not* "what % of Net Worth is cash".
- **NO historical / time-based allocation.** It's the **current** mix (as of now). The donut is not sampled over time (that's the 5.3 trend chart's job).
- **NO new store query, NO migration, NO `make sqlc`.** Allocation is **derived** from the existing `Portfolio()` valuation + a `buildRates` call. No `db/migrations`, no `db/query`, no `internal/store` regen.
- **NO new dependency, NO JS chart library, NO client-side charting** (D1). Inline server-rendered SVG donut + HTML legend only; the `?by=` toggle is plain links. Do not add Chart.js/uPlot/etc., a `<canvas>`, or an init `<script>`.
- **NO float in the financial core** (NFR-5). `domain.Allocate` is pure `decimal`; donut geometry (π, dash arrays) lives in the http/web layer, outside `make nofloat`'s scope.
- **NO gain/loss colour for slices** — allocation is a neutral categorical breakdown; use a dedicated calm categorical palette, NOT the gain/loss green/red (reserved by 5.1).
- **NO change to `Portfolio()`/`Dashboard()`/`ValueSeries()` public behaviour, NO change to the 5.2 KPI cards or the 5.3 trend chart** (other than additively threading the active `by` into the chart range hrefs so the dimension is preserved — keep 5.3's tests green).

### What already exists (reuse, don't rebuild)

[Source: internal/service/valuation/valuation.go; internal/domain/networth.go; internal/http/router.go; web/pages.templ; web/shell.go; 5-3-value-over-time-trend-chart.md; [[financas-epic5-progress]]; [[financas-epic4-decisions]]]

- **`valuation.Service.Portfolio(ctx)` (4.4/5.2)** returns the current per-holding `HoldingValuation` list (native `Valuation`, `Currency`, `Symbol`, `Name`, `AccountID`, `AccountName`, `HasPrice`) **plus** the Display-Currency `PortfolioValue`, `Display`, `Missing`, `Unpriced`. This is the invested-value raw material — group its **priced** holdings; **do not re-derive holdings or valuations**.
- **`Service.buildRates(ctx, q, display, asOf, groups...)`** builds the exact native→Display rate map (effective ≤ `asOf`, never inverted, AD-6) for the currencies present in the supplied money groups, skipping the Display currency. Call it with the holdings' native values (and `asOf = dateOnlyUTC(time.Now())`) to get the **same rates Net Worth used** — so the allocation `Total` reconciles to `PortfolioValue` (D4).
- **`domain.NetWorth`'s `convertSum`/`hasRate` pattern** (`internal/domain/networth.go`) is the template for `Allocate`'s conversion + `Missing` handling: convert at full precision, skip non-zero unrated amounts into a deduped+sorted `Missing`, round once at the boundary. Mirror it for consistency (one rounding policy everywhere, AD-12).
- **`domain.PercentChange` (5.2)** shows the house pattern for a pure `decimal` domain calc returning a reconciled figure — `Allocate` follows the same "pure, decimal, single canonical home" shape (AD-10).
- **`dashboardPage` + `web.DashboardView` + the `?range=` link toggle (5.3)** are the page/handler to extend. The toggle styling (`templ.KV("bg-accent/10 text-accent font-medium", Active)` + `aria-current`), the `buildChart`-style "geometry in the handler" seam, the graceful empty/error degradation, and the partial-/unpriced-notice patterns are all established there — mirror them for `?by=` + `buildAllocation`.
- **The `?range=`/`?archived=` query-param toggle** is the established server-reload toggle pattern; `?by=` joins it on `/`. Build hrefs that carry **both** params so the chart range and the allocation dimension don't clobber each other.
- **`itoa` (web/shell.go)** + the inline-`<svg>` precedent (5.3 `trendChart`) cover SVG rendering. **Static assets are embedded** (`web/embed.go`); the donut is inline markup needing no new asset. The 5.3 `fill-accent`/`stroke-accent` utilities exist — extend the palette, don't rename.
- **Build/codegen:** `.templ` → `make generate` (commit `*_templ.go`); new CSS utilities → `make css` (commit `app.css`); `make nofloat` stays green; `gofmt` clean. [Source: Makefile]

### Architecture invariants this story must honor

- **AD-12 — convert-then-sum, round-once, allocation reconciled to 100%.** Percentages are computed from **unrounded** converted values and reconciled to **exactly 100%** (largest-remainder). Conversion uses `money.Convert` (full precision) + `Rounded()` (banker's half-to-even) once at the boundary, same order as everywhere. [Source: ARCHITECTURE-SPINE.md#AD-12]
- **AD-10 — one canonical home per derived figure.** **Allocation** is explicitly named in AD-10 — it gets exactly one pure `domain` function (`Allocate`). `service` loads inputs + calls it; `http` does **no** financial math, only donut geometry + formatting. [Source: #AD-10]
- **AD-5 — store native, convert only on read.** Holdings stay native (`HoldingValuation.Valuation`); conversion to Display happens only in `Allocate` (the domain projection), using the effective rate. [Source: #AD-5]
- **AD-6 — owner-entered effective-dated rates, no inversion.** A missing native→Display pair → that holding is excluded (partial), never inverted/guessed — inherited from the `buildRates`/`convertSum` path. [Source: #AD-6; [[financas-epic4-decisions]]]
- **NFR-5 — no float in the financial core.** `domain.Allocate` is pure `decimal`; π/dash-array geometry is http-layer. `make nofloat` (scope `internal/{money,domain,service,store}`) stays green. [Source: Makefile `nofloat`]
- **NFR-4 — reasonable a11y.** Donut `<svg>` has `role="img"` + `aria-label`; the legend conveys the data textually (key/percent/value); the `?by=` toggle is real focusable links. [Source: prd.md §"Cross-Cutting NFRs"]
- **AD-1 — web/http render only.** The service returns slices + reconciled percents + `money.Money`; the handler/templ turn them into donut arcs + labels (presentation). [Source: #AD-1]

### Design intent (from the PRD/UX — apply here)

[Source: epics.md UX-DR4 / Story 5.4; prd.md §4.5 FR-12]

- **UX-DR4 — Allocation / breakdown visual:** a **bar/donut breakdown card** — allocation by Security and/or Account — **reconciled to 100% per AD-12**. Decision: **donut + legend** (D1/D2), with a `?by=security|account` toggle (decision: **both dimensions**, default Security). Clean, calm, uncluttered; the donut is the focus, the legend carries the numbers.
- **FR-12 / PRD §4.5:** allocation summing to 100% by Security and/or Account, computed from converted values. **Performance attribution is out of scope** (as for 5.3) — this is a static mix breakdown.
- **Aesthetic:** matches the 5.1 design-token system + 5.2/5.3 dashboard cards; a distinct categorical palette (not gain/loss); legible legend; graceful empty state — the dashboard should feel polished and "at a glance".

### Previous-story intelligence (5.3 + 5.2 / 4.4) — load-bearing

[Source: 5-3-value-over-time-trend-chart.md; 5-2-dashboard-kpi-cards.md; [[financas-epic5-progress]]; [[financas-epic4-decisions]]]

- **5.3 established the exact seam this story mirrors:** geometry computed in the handler (`buildChart`), a pre-formatted view struct (`ChartView`), inline `<svg>`, a `?range=` link toggle styled with `templ.KV`+`aria-current`, graceful empty/error degradation (error copy ≠ no-data copy), and DB-free handler/render tests with a stub `Valuation`. Reuse this shape for `buildAllocation`/`AllocationView`/`?by=`.
- **5.3 review lessons to repeat here:** (a) **error ≠ no-data** — a load failure gets a distinct "couldn't load" message, not the empty-state copy; (b) avoid Go-builtin shadowing (`min`/`max` → `lo`/`hi`); (c) keep the financial core float-free, geometry in http. (d) The `Amount` primitive prepends its own sign/colour — but allocation values are **neutral magnitudes** (no sign/colour), so format with `money.Money.String()` directly, not `Amount`.
- **The latent timezone bug 5.3 fixed** (`portfolioAsOf` normalizes price/rate reads to `dateOnlyUTC(asOf)`) means `Portfolio()`/`buildRates` already agree on one UTC calendar date — reuse `dateOnlyUTC(time.Now())` for the allocation's rate read so it matches.
- **Partial-total honesty is a pinned Epic-4 invariant** ([[financas-epic4-decisions]]): a holding missing a rate is excluded and surfaced, never blocked or guessed. The dashboard already renders the `MissingCodes`/`UnpricedSymbols` notices — the allocation card's partial note should be consistent with them.
- **House style:** typed view structs in `web/shell.go` (pre-formatted strings + ints); table-of-`want`-substring render/handler tests; DB-gated service tests skip without `TEST_DATABASE_URL`/`DATABASE_URL`; `GOTOOLCHAIN=local`; commit regenerated `*_templ.go`; `make nofloat`/`gofmt` green; local DB host **5433**; dev login `owner`/`financas`; **one commit per story, push to `main`**.

### Project Structure Notes

- **NEW — domain:** `internal/domain/allocation.go` (`AllocItem`/`AllocSlice`/`Allocation` + `Allocate` with largest-remainder + Other-cap); `internal/domain/allocation_test.go`.
- **Modified — service:** `internal/service/valuation/valuation.go` (`AllocationGroup`/`Allocation` types + `Allocation(ctx, by)` method reusing `Portfolio`+`buildRates`); `internal/service/valuation/valuation_test.go`.
- **Modified — http:** `internal/http/router.go` (`Valuation` interface `+Allocation`; `dashboardPage` reads `?by=`, builds `AllocationView` via `buildAllocation`; `allocBy`/palette/geometry consts; chart range hrefs additively carry `by`); `internal/http/router_test.go` (stub `Allocation` + `cannedAllocation`; allocation handler tests; adjusted chart-href expectations).
- **Modified — web:** `web/shell.go` (`AllocSliceView`/`AllocBy`/`AllocationView` + `DashboardView.Allocation`); `web/pages.templ` (`allocationCard` component + card in `DashboardPage`) → regenerated `web/pages_templ.go`; `web/shell_test.go` render test. `web/static/css/input.css` (categorical palette utilities) + rebuilt `web/static/css/app.css`.
- **Modified — docs:** `README.md`.
- **NOT touched:** no `db/migrations`, no `db/query`, no `internal/store` regen (no `make sqlc`), no new module dependency, no new JS asset, no `web/embed.go` change.

### Testing standards

- **domain (pure, DB-free):** `Allocate` — percents sum to **exactly 100** (incl. the thirds/largest-remainder case naive rounding breaks); convert-then-sum across multiple currencies; unrated currency excluded → `Missing`; empty/all-zero → no slices; Other-cap folds the tail and still reconciles. This is the load-bearing 100%-reconciliation test (AC #3).
- **service (DB-gated):** `Allocation(ctx,"security")`/`("account")` group correctly, percents sum 100, `Total == PortfolioValue`; missing-rate currency excluded into `Missing`; no-priced-holdings → no groups; `ErrOversold` propagates. Skip without DB.
- **http (DB-free, stub):** `/` and `/?by=account` render the donut `<svg>` (one `<circle>` arc per slice) + legend rows + `Total` + the two `?by=` links (active marked, **carrying the active range**) + partial note; no-groups → empty state, no donut; load error → distinct "couldn't load" copy; existing dashboard/chart tests stay green (stub gains `Allocation`; chart hrefs may include `&by=`).
- **web (pure render):** populated `AllocationView` → donut + arcs + legend + total + toggle; empty → empty-state copy.
- `go test ./...` green (web/http DB-free; DB-gated valuation test skips without DB). `go vet`, `gofmt -l`, `make nofloat` clean. Visual confirmed by the live smoke.

### References

- [Source: epics.md#Story 5.4] — ACs (breaks invested value down by Security and/or Account; percentages from unrounded converted values reconciling to exactly 100%); FR-12, UX-DR4
- [Source: epics.md UX-DR4] — bar/donut allocation breakdown card, reconciled to 100% per AD-12
- [Source: prd.md §4.5 FR-12] — allocation & performance views; allocation summing to 100%; perf-attribution out of scope
- [Source: ARCHITECTURE-SPINE.md#AD-12] — convert-then-sum, banker's round-once, **allocation percentages from unrounded values reconciled to exactly 100%**; [#AD-10 allocation = one canonical `domain` function; #AD-5/#AD-6/#AD-1/NFR-5/NFR-4]
- [Source: internal/service/valuation/valuation.go] — `Portfolio`/`HoldingValuation` (the priced-holdings raw material to group), `buildRates` (the same Net-Worth rates → `Total` reconciles to `PortfolioValue`), `dateOnlyUTC`
- [Source: internal/domain/networth.go] — `convertSum`/`hasRate`/`Missing` pattern to mirror in `Allocate`; [internal/domain/change.go `PercentChange`] — the pure-decimal canonical-figure shape
- [Source: internal/http/router.go `dashboardPage`/`buildChart`/`chartRange`] — the page/geometry/toggle seam to extend; [web/pages.templ `trendChart`; web/shell.go `ChartView`/`DashboardView`] — where the allocation card slots in + the view-struct + toggle-styling precedent
- [Source: 5-3-value-over-time-trend-chart.md; [[financas-epic5-progress]]] — the handler-geometry / inline-SVG / link-toggle / graceful-degradation precedent + review lessons
- [Source: Makefile] — `make generate` / `make css` / `make nofloat`; committed-artifact rule; no `make sqlc`

## Dev Agent Record

### Agent Model Used

claude-opus-4-8[1m] (Opus 4.8, 1M context) — bmad-dev-story workflow.

### Debug Log References

- TDD per layer: `domain.Allocate` tests (largest-remainder reconciliation, convert-then-sum, missing-rate, grouping, empty, Other-cap) → service DB-gated tests (`TestAllocation`/`TestAllocationEmpty`) → http handler tests (donut/legend/toggle/partial/empty + cross-preserve) → web render tests. Each layer written RED, then implemented to GREEN.
- One test-assertion fix: the `?by=`/`?range=` hrefs are HTML-escaped (`&` → `&amp;`) in the rendered attribute; the handler test substrings were adjusted to `&amp;by=…` / un-prefixed `by=…` accordingly. No production change.
- `make generate` (templ) produced no `internal/store` diff (no new query — allocation reuses `Portfolio()`/`buildRates`). `make css` added the `--color-alloc-1..8` theme tokens; the `stroke-alloc-N`/`bg-alloc-N` utilities are built dynamically in Go, so they are safelisted via `@source inline(...)` in `input.css` (verified present in the rebuilt `app.css`).

### Completion Notes List

- **Domain (Task 1):** new `internal/domain/allocation.go` — the single canonical home for allocation (AD-10). `Allocate(display, items, rates, topN)` does convert-then-sum per item at full precision (mirrors `NetWorth`'s `convertSum`/`hasRate`/`Missing`), groups by key, and reconciles integer percents to **exactly 100** via a `largestRemainder` helper (floor shares, hand the leftover whole points to the largest fractional remainders; ties broken by value then index for determinism). Pure `decimal` — `make nofloat` stays green. The D5 "Other" cap folds the tail beyond `topN` before reconciliation.
- **Service (Task 2):** `valuation.Allocation(ctx, by)` + `AllocationGroup`/`Allocation` read-model + `AllocBy` normalizer. Reuses `Portfolio()` for the priced holdings (native MV, symbol/account) and a second `buildRates` pass for the same effective-today rates Net Worth used, so `Total` reconciles to `PortfolioValue` (D4) — asserted in the DB-gated test. **Deviation from the spec (more precise):** the read-model's `Missing` uses `alloc.Missing` (the holdings-specific currencies the allocation actually excluded) rather than `p.Missing` (which also reflects cash/liability currencies) — so the allocation card's partial note is accurate to the breakdown itself.
- **HTTP (Task 3):** `Valuation` interface `+Allocation`; `dashboardPage` reads `?by=`, calls `Allocation`, and `buildAllocation` computes the donut geometry — per-slice `stroke-dasharray`/`stroke-dashoffset` on a ring (`C = 2πR`, π a documented http-layer `decimal` constant, no trig; D2), the colour-token classes, the legend strings, and the range-preserving `?by=` toggle. A `ValueSeries`/`Allocation` error degrades to an empty card with a distinct "couldn't load" message (error ≠ no-data). `buildChart`'s range hrefs additively carry `&by=` so the dimension survives a range switch (5.3 chart tests stay green — `/?range=1m` is still a substring).
- **Web (Task 4):** `AllocSliceView`/`AllocBy`/`AllocationView` + `DashboardView.Allocation`; `allocationCard` templ renders the accent-free categorical donut (`role="img"` + `aria-label`, native `<title>` per slice) beside an HTML legend (colour chip + key + percent + value), a centre Total, the Security/Account toggle (active marked via `aria-current` + `bg-accent/10`), the partial note, and the empty/error state. New `--color-alloc-1..8` tokens; no token rename, no new dependency, no client JS.
- **Verification (Task 5):** `go build`/`vet`/`test -count=1 ./...` green incl. DB-gated valuation tests on local PG :5433; `make nofloat` green; `gofmt` clean. **Live smoke** (freshly-built binary, owner/financas, dev DB seeded with multi-currency priced holdings): `/` renders the KPI row + trend chart + the allocation donut; the legend percents **summed to exactly 100**; the **D5 "Other" cap** kicked in with the dev DB's existing data (8 named slices + Other = 9 arcs); the Total reconciled with the Portfolio Value KPI; `?by=account` regrouped by account and marked the Account link current; the chart range links carried `&by=security`; `/investments` unchanged. Seed rows cleaned up afterward.

### File List

- `internal/domain/allocation.go` (new — `AllocItem`/`AllocSlice`/`Allocation` + `Allocate` with `largestRemainder` + Other-cap; `sortedCurrencies`)
- `internal/domain/allocation_test.go` (new — reconciliation/convert-then-sum/missing/grouping/empty/Other-cap tests)
- `internal/service/valuation/valuation.go` (modified — `allocTopN`; `AllocationGroup`/`Allocation` types; `AllocBy`; `Allocation(ctx, by)` reusing `Portfolio`+`buildRates`)
- `internal/service/valuation/valuation_test.go` (modified — `TestAllocation`, `TestAllocationEmpty`)
- `internal/http/router.go` (modified — `Valuation` interface `+Allocation`; `dashboardPage` `?by=`; `buildAllocation` + donut geometry consts/palette/π; `buildChart` range hrefs carry `&by=`)
- `internal/http/router_test.go` (modified — `stubValuation.Allocation` + `cannedAllocation`; allocation handler tests; chart-href `&by=` assertions)
- `web/shell.go` (modified — `AllocSliceView`/`AllocBy`/`AllocationView` + `DashboardView.Allocation`)
- `web/pages.templ` (modified — `allocationCard` component + card in `DashboardPage`)
- `web/pages_templ.go` (regenerated)
- `web/shell_test.go` (modified — allocation card render tests)
- `web/static/css/input.css` (modified — `--color-alloc-1..8` tokens + `@source inline` safelist)
- `web/static/css/app.css` (rebuilt — alloc palette utilities)
- `README.md` (modified — allocation breakdown note)

## Change Log

| Date | Change |
| --- | --- |
| 2026-06-29 | Story 5.4 implemented (dev-story): added a **server-rendered SVG allocation donut + legend** to the dashboard, breaking down **invested value (Portfolio Value)** by **Security or Account** via a range-preserving `?by=` toggle. New canonical `domain.Allocate` (pure decimal, AD-10) does convert-then-sum + **largest-remainder reconciliation to exactly 100%** (AD-12) with a top-8 + "Other" legibility cap; `valuation.Allocation(ctx, by)` reuses `Portfolio()`/`buildRates` so the Total reconciles to `PortfolioValue` (D4); `buildAllocation` computes the donut geometry via `stroke-dasharray` (π only, no trig — http layer, core stays float-free); `AllocationView` + `allocationCard` templ; `--color-alloc-1..8` palette safelisted in `app.css`. Missing-rate currencies excluded with a partial notice; no priced holdings → graceful empty state; load error → distinct "couldn't load". Build/vet/test (incl. DB-gated)/nofloat/gofmt green; live smoke confirmed the donut (incl. the Other cap), 100%-summing legend, Total reconciliation, and the by-account regroup. No new query/migration/dependency/JS. Status → review. |
| 2026-06-29 | Story 5.4 drafted (create-story): add a **server-rendered SVG allocation donut + legend** to the dashboard, breaking down **invested value (Portfolio Value)** by **Security or Account** via a `?by=` toggle (default Security). Decisions: **D1** inline SVG donut + HTML legend, no JS lib, `?by=` link toggle; **D2** donut via `stroke-dasharray` (no trig, π only); **D3** allocation is a canonical pure-`decimal` `domain.Allocate` (AD-10) doing convert-then-sum + **largest-remainder reconciliation to exactly 100%** (AD-12); **D4** same rates as Net Worth → `Total` reconciles to `PortfolioValue`; **D5** top-8 + "Other" legibility cap. Reuses `Portfolio()`/`buildRates`; cash/liabilities excluded (invested value only); missing-rate currencies excluded with a partial notice; no-priced-holdings → graceful empty state. New `domain.Allocate`; `valuation.Allocation`; `dashboardPage` `?by=` + `buildAllocation`; `AllocationView` + `allocationCard` templ; categorical palette in `app.css`. No new query/migration/dependency/JS. Status → ready-for-dev. |
