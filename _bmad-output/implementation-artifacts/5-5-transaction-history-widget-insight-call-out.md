---
baseline_commit: a5c2954
---

# Story 5.5: Transaction history widget & insight call-out

Status: review

<!-- Note: Validation is optional. Run validate-create-story for quality check before dev-story. -->

## Story

As the owner,
I want recent transactions and a highlight insight on the dashboard,
so that the dashboard feels alive and useful.

## Acceptance Criteria

From `epics.md` → Epic 5 → Story 5.5 (realizes **FR-8**, **FR-11**; **UX-DR5**, **UX-DR6**; honors **AD-2**, **AD-9**, **AD-10**, **AD-12**, **NFR-4**, **NFR-5**). **Given** transactions and valuations exist, **When** the dashboard renders, **Then**:

1. **A recent-transactions widget lists rows with a per-row icon, description, date, signed colored amount, and a Category/type badge, linking to the full register** (UX-DR5, FR-8). It shows the **most recent N (= 5)** transactions across **all** accounts (newest-first), each row: a small type icon (income/expense/transfer/trade), the description, the date, the **signed amount colored green for incoming / red for outgoing** (transfers neutral), and a type (or Category) badge. A "View all" link goes to `/transactions` (the 3.5 register). Amounts are pre-formatted money (decimal, never reformatted to float — UX-DR8).
2. **A bold accent insight call-out card surfaces ONE derived insight, computed in `domain` and only rendered here** (UX-DR6, AD-10). The insight is **"Your net worth is up/down X% this month"** — the percentage change of Net Worth from the **first day of the current month** to now, both in the Display Currency. The percentage is the canonical `domain.PercentChange` figure (the same home the KPI deltas use, AD-10); the `http` layer only frames the sentence and picks the up/down styling. The card uses the **accent** token (a bold call-out, not gain/loss green/red coloring of the card itself), with a ▲/▼ direction cue.
3. **The insight reuses the existing as-of valuation seam** (`portfolioAsOf`, AD-11): Net Worth "now" = `portfolioAsOf(now).NetWorth`; the month-start baseline = `portfolioAsOf(firstOfMonthUTC).NetWorth` (prices/rates effective ≤ that date — never retroactively repriced). No snapshot table, no new query (AD-11).
3. **Partial-total & empty honesty (AD-12 / AD-6 / UX-DR8):** when no comparable month-start baseline exists (a brand-new portfolio, or a non-positive baseline) the insight shows a calm fallback ("Add transactions and prices over the month and your net-worth trend will appear here.") instead of a misleading ±∞ or 0%. If either valuation excluded a currency for lack of a rate, the insight is a partial figure and says so briefly (consistent with the KPI/trend/allocation partial notices) — never silently wrong. The recent-transactions widget shows a calm empty state ("No transactions yet — add one to get started.") when the ledger is empty.
4. **Graceful degradation (matches 5.3/5.4):** a recent-transactions load failure degrades to an empty widget and an insight load failure hides/empties the insight card — the rest of the dashboard (KPIs, trend, allocation) still renders; the page never 500s for these secondary widgets. A11y (NFR-4): the widget rows and the "View all" link are real focusable links; the insight arrow has an accessible label.

### Locked design decisions (read before implementing)

- **D1 — Recent-transactions widget reuses `Transactions.Register`, top-N (NO new query/service method).** `Register(ctx, RegisterFilter{})` already returns the whole ledger **newest-first**, enriched with account/category/security names, with transfers appearing once (AD-9, AD-2). The handler calls it and takes the **first `recentTxLimit = 5`** rows (a presentation slice — not financial math), maps them to the existing `web.RegisterRow` view (already used by `/transactions`), and renders a compact card with a per-row type icon + a "View all" link to `/transactions`. **Do NOT add a new store query, a `LIMIT` query, or a new service method** — owner-scale ledger, the existing read is fine and DRY. (If the ledger is ever huge this can be revisited; document the top-5 cap.)
- **D2 — Insight = month-over-month Net Worth %, computed via the canonical `domain.PercentChange` (AD-10).** Add a service method `valuation.Insight(ctx) (Insight, error)` that values the portfolio at `now` and at the **first of the current month (UTC)** via the existing `portfolioAsOf`, then calls `domain.PercentChange(now.NetWorth, monthStart.NetWorth)` — the **same** canonical %-change home the KPI deltas use (`setDelta`). The `service` returns the figure + direction + a partial flag; the `http` layer frames the sentence ("Your net worth is up/down X% this month") and selects the ▲/▼ + accent styling. **No new domain function** — `PercentChange` is the existing canonical home; do not re-implement a percentage. The financial core stays float-free (PercentChange is decimal, NFR-5).
- **D3 — Month start = first day of the current month at UTC midnight.** Compute it from `time.Now()` normalized the same way as the rest of the as-of machinery: `firstOfMonthUTC = time.Date(y, m, 1, 0,0,0,0, time.UTC)` using the UTC year/month of now (consistent with `dateOnlyUTC` and the 5.3 timezone fix — all as-of date comparisons use UTC calendar dates; the DB session is UTC). This is the baseline passed to `portfolioAsOf`.
- **D4 — The insight is rendered ONLY on the dashboard (AD-10 / UX-DR6).** The derived figure lives in `domain` (`PercentChange`) and the framed call-out appears only here — no other page renders it. The card uses the **accent** token for the call-out emphasis (UX-DR6 "one bold accent card"); the ▲/▼ direction may tint with gain/loss, but the card's bold identity is the accent, not a green/red fill.
- **D5 — Placement & layout.** Both widgets go **below the 5.4 allocation card** (when `!view.ErrMsg`), before the existing Missing/Unpriced notices: the **insight call-out first** (bold accent, prominent), then the **recent-transactions widget**. Reuse the 5.1 `@Card` + `@Badge` + the `kpiIcon`-style chip; no new layout system. Do not disturb the 5.2 KPI row / 5.3 trend / 5.4 allocation.

## Tasks / Subtasks

- [x] **Task 1 — Service: the month-over-month Net Worth insight (AC: #2, #3; D2, D3)**
  - [x] In `internal/service/valuation/valuation.go`, add the read-model + method:
    ```go
    // Insight is the dashboard's single derived call-out (Story 5.5, UX-DR6): the
    // percentage change of Net Worth from the first of the current month to now,
    // in the Display Currency. Pct is the canonical domain.PercentChange figure
    // (signed, 1 dp). HasData is false when no comparable month-start baseline
    // exists (non-positive baseline / different currency) → the card shows a calm
    // fallback. Partial is set when either valuation excluded a currency for lack
    // of a rate (the figure is a partial total, AD-6/AD-12).
    type Insight struct {
        Pct      decimal.Decimal // signed % change this month (1 dp)
        Up       bool
        Down     bool
        HasData  bool
        Partial  bool
        NetWorth money.Money     // current Net Worth (Display Currency)
        Display  money.Currency
    }
    // Insight derives the month-over-month Net Worth change: portfolioAsOf(now)
    // vs portfolioAsOf(first-of-month UTC), reconciled by the canonical
    // domain.PercentChange (AD-10) — the same %-change home the KPI deltas use.
    // It reuses the as-of seam (AD-11, no snapshot table). ErrOversold propagates.
    func (s *Service) Insight(ctx context.Context) (Insight, error)
    ```
  - [x] Implementation (reuse, do not re-derive): `now := time.Now()`; `cur, err := s.portfolioAsOf(ctx, now)`; `monthStart := firstOfMonthUTC(now)`; `base, err := s.portfolioAsOf(ctx, monthStart)`; `pct, ok := domain.PercentChange(cur.NetWorth, base.NetWorth)`. Build `Insight{Pct: pct, Up: ok && pct.IsPositive(), Down: ok && pct.IsNegative(), HasData: ok, Partial: len(cur.Missing) > 0 || len(base.Missing) > 0, NetWorth: cur.NetWorth, Display: cur.Display}`. Propagate `ErrOversold` like `Portfolio`/`Dashboard`. Add an unexported `firstOfMonthUTC(t time.Time) time.Time` helper next to `dateOnlyUTC` (`u := t.UTC(); return time.Date(u.Year(), u.Month(), 1, 0,0,0,0, time.UTC)`).
  - [x] Tests (`valuation_test.go`, DB-gated, reuse `isolatedDB`/`dateOnlyUTC`): a fixture with a cash + a priced holding where Net Worth at month-start < Net Worth now asserts `Insight` returns `HasData` true, `Up` true, and a positive `Pct` (cross-check against `domain.PercentChange` of the two as-of Net Worths); a down case (price drops after month start, with a month-start price) asserts `Down`; a fresh/empty DB (or non-positive month-start baseline) asserts `HasData` false; a missing-rate holding asserts `Partial` true. Use relative dates anchored so the month-start (1st of this month UTC) sits **before** the prices you add — e.g. add the holding/prices on `firstOfMonthUTC(now)` and a later in-month date, so `portfolioAsOf(monthStart)` and `portfolioAsOf(now)` differ. (If "today" is the 1st of the month, month-start == today → `Pct` 0 / `HasData` may be true with 0%; the test should pick dates that don't straddle that edge, or assert tolerantly. Document this calendar edge.)

- [x] **Task 2 — HTTP: build the recent-tx widget + insight on the dashboard (AC: #1, #2, #4; D1, D2, D5)**
  - [x] Extend the `Valuation` interface in `internal/http/router.go` with `Insight(ctx context.Context) (valuation.Insight, error)`.
  - [x] In `dashboardPage`, after the allocation block: (a) **Recent tx** — `regRows, rErr := deps.Transactions.Register(req.Context(), transaction.RegisterFilter{})`; take `recentTxLimit = 5` (`if len(regRows) > recentTxLimit { regRows = regRows[:recentTxLimit] }`); map to `[]web.RegisterRow` exactly as `transactionsRegister` does (reuse `registerAmount`); set `view.Recent`. On `rErr`, leave it empty (the widget renders its empty/none state). (b) **Insight** — `ins, iErr := deps.Valuation.Insight(req.Context())`; build `view.Insight = buildInsight(ins)`; on `iErr`, set an empty `InsightView{}` (the card hides/shows nothing) — the page still renders. Define `recentTxLimit` as a const.
  - [x] `buildInsight(ins valuation.Insight) web.InsightView` (presentation framing — AD-1; the % is the domain figure): when `!ins.HasData` → `InsightView{HasData: false, Empty: "Add transactions and prices over the month and your net-worth trend will appear here."}`. Otherwise frame the sentence from the magnitude: `pctStr := ins.Pct.Abs().String() + "%"`; `verb := "up"` when `Up`, `"down"` when `Down`, else `"flat"`; `Text := "Your net worth is " + verb + " " + pctStr + " this month"` (flat → "Your net worth is flat this month"); set `Up/Down`, `Partial`, and `NetWorth: ins.NetWorth.String()`. Keep money/percent formatting here; no math.
  - [x] Handler tests (`router_test.go`): extend `stubValuation` with `Insight` (+ `insight`/`insightErr` fields). `/` renders the recent-tx widget (the canned rows' descriptions + signed amounts + the "View all" link to `/transactions`) and the insight sentence ("Your net worth is up … this month") with the accent styling + a ▲; an empty `Register` stub → the widget empty state; an `insightErr`/`!HasData` → the insight fallback copy, page still 200 with the KPIs. Keep all existing dashboard tests green (the stub `Valuation` now needs `Insight`). The `stubTransactions.Register` already exists (reused from the register tests) — give it a canned multi-row ledger for the dashboard test.
  - [x] **No new query** (`make sqlc` not run); the widget reuses `Register`. **No change to `transactionsRegister`/`Register` behaviour.**

- [x] **Task 3 — Web: render the widget + insight card (AC: #1, #2, #4; D1, D5)**
  - [x] Add view types to `web/shell.go`: `InsightView{HasData bool; Text, NetWorth string; Up, Down, Partial bool; Empty string}`. Add `Recent []RegisterRow` and `Insight InsightView` to `DashboardView` (reuse the existing `RegisterRow` for the widget rows — it already carries Type/Description/Date/Account/Category/Amount/Incoming/IsTransfer).
  - [x] In `web/pages.templ`: (a) an `insightCallout(i InsightView)` component — a **bold accent** `@Card` (e.g. `bg-accent/10` or an accent left-border; use the accent token, not gain/loss as the card identity) with the `Text`, a ▲/▼ glyph (`templ.KV` on `Up`/`Down`, with an `sr-only` "up"/"down" for a11y), and a small partial note when `Partial`; the empty state shows `Empty`. (b) a `recentTransactions(rows []RegisterRow)` component — an `@Card` titled "Recent activity" with a "View all" `<a href="/transactions">`, then a compact list: each row a small type icon (add a `txTypeIcon(typ string)` templ helper — income/expense/transfer/buy/sell/dividend glyphs, accent/muted chip mirroring `kpiIcon`), the description (+ a Category/type `@Badge`), the date (muted), and the signed amount colored via `templ.KV("text-gain", r.Incoming && !r.IsTransfer)` / `templ.KV("text-loss", !r.Incoming && !r.IsTransfer)` (transfers neutral) — exactly the `TransactionRows` colour rule. Empty list → "No transactions yet — add one to get started." Render both in `DashboardPage` **below `@allocationCard`** (insight first, then recent), before the Missing/Unpriced notices.
  - [x] Render tests (`web/shell_test.go`, via `renderToString`, DB-free): a populated `InsightView` renders the sentence + the ▲ (up) / ▼ (down) + accent classes + partial note; an empty `InsightView` renders the fallback. A populated `recentTransactions` renders the rows (description, signed amount, badge, the type icon) + the "View all" → `/transactions` link, with income green / expense red; an empty slice renders the empty copy.

- [x] **Task 4 — Wire, verify, docs (AC: all)**
  - [x] `make generate` after the `.templ` edit (commit regenerated `web/pages_templ.go`). `make css` **only if** a new utility class was introduced (prefer existing tokens; rebuild + commit `app.css` only if so). **No `make sqlc`** (no new query). `GOTOOLCHAIN=local go build ./... && go vet ./... && go test -count=1 ./...` green (web/http DB-free; DB-gated valuation test runs against local PG :5433). **`make nofloat` stays green** (the insight % is `domain.PercentChange`, decimal; the widget does no math). `gofmt -l` clean.
  - [x] **Live smoke** (compose db :5433 + freshly-built binary, owner/financas, seeded data: a few transactions across accounts spanning before/after the 1st of the month, with prices/rates so Net Worth moved): log in → `/` shows the KPI row + trend + allocation **and** the insight call-out ("Your net worth is up/down X% this month") + the recent-transactions widget (5 newest rows, icons, signed colours, badges, a working "View all" → `/transactions`); confirm the insight direction matches the data; confirm the empty states on a fresh DB; confirm `/transactions` and the rest are unchanged. **Clean up any seed rows afterward** (the base `financas` DB is a shared test fixture — restore `display_currency` to USD and remove rows you added; reset before running the full suite, see lesson below).
  - [x] Update `README.md` (Dashboard note): the dashboard now also shows a **recent-transactions widget** (the 5 newest ledger entries — icon, description, date, signed colored amount, type/category badge — linking to the full register) and a **bold accent insight call-out** ("Your net worth is up/down X% this month") whose percentage is the canonical `domain.PercentChange` of Net Worth from the first of the month to now (derived via `portfolioAsOf`, no snapshot table); both degrade gracefully and never 500 the page.
  - [x] **Commit + push to `main`** (trunk-based, one commit per story). `baseline_commit` is HEAD `a5c2954`. This completes Epic 5.

## Dev Notes

### Scope boundary — what this story does NOT do (read first)

- **This is the LAST story of Epic 5.** It adds two dashboard widgets only: the recent-transactions list (UX-DR5) and the single insight call-out (UX-DR6). No new analytics page, no spending-by-category insight, no multi-insight feed.
- **NO new store query / migration / `make sqlc`.** The recent-tx widget reuses `Transactions.Register` (whole ledger, newest-first) and slices the top 5; the insight reuses `portfolioAsOf` + `domain.PercentChange`. No `db/migrations`, no `db/query`, no `internal/store` regen.
- **NO new derived-figure home.** The insight percentage IS `domain.PercentChange` (the canonical AD-10 home, already used by the KPI deltas). Do NOT add a second percentage function. The `http` layer frames the sentence; the `service` orchestrates the two as-of valuations.
- **NO new dependency, NO client JS, NO HTMX for these widgets.** Both are static server-rendered markup (the register's HTMX filter is not needed on a fixed top-5 list). No chart lib, no `<script>`.
- **NO float in the financial core** (NFR-5). The insight % is decimal via `PercentChange`; the widgets only format pre-computed strings.
- **NO change to the 5.2 KPI row / 5.3 trend / 5.4 allocation**, and **NO change to `Register`/`transactionsRegister`/`Dashboard`/`Portfolio` public behaviour.** The widgets are added below the allocation card.
- **NO gain/loss recoloring of the insight CARD** — the call-out's identity is the **accent** token (UX-DR6); the ▲/▼ may tint gain/loss, but the card is the bold accent, not a green/red panel.

### What already exists (reuse, don't rebuild)

[Source: internal/service/transaction/transaction.go; internal/service/valuation/valuation.go; internal/domain/change.go; internal/http/router.go; web/pages.templ; web/shell.go; web/components.templ; 5-4-allocation-breakdown.md; [[financas-epic5-progress]]]

- **`transaction.Service.Register(ctx, RegisterFilter{})`** returns the whole ledger **newest-first** (`ListTransactions` is `occurred_on DESC, id DESC`), enriched with account/category/security names; a transfer appears once (AD-9), income/sell/dividend are `Incoming`, expense/buy are outgoing, transfers neutral. The `/transactions` handler (`transactionsRegister`) already maps `transaction.RegisterRow` → `web.RegisterRow` via `registerAmount` (signed +/−, neutral transfer legs) — **reuse that exact mapping**; take the top `recentTxLimit`. [Source: internal/service/transaction/transaction.go `Register`/`RegisterRow`; internal/http/router.go `transactionsRegister`/`registerAmount`]
- **`web.RegisterRow` + `TransactionRows` templ** are the row shape + colour rule (`text-gain` incoming / `text-loss` outgoing, transfers neutral). The widget is a compact variant of this — reuse `RegisterRow`; add a per-row `txTypeIcon` (mirror `kpiIcon`'s accent chip) for UX-DR5's "icon". [Source: web/shell.go `RegisterRow`; web/pages.templ `TransactionRows`]
- **`valuation.Service.portfolioAsOf(ctx, asOf)`** is the as-of Net Worth seam (AD-11) — call it at `now` and at the first of the month for the insight, exactly as `Dashboard` calls it at `now` and the prior sample date. **`domain.PercentChange(now, base)`** is the canonical signed-%-change home (1 dp, banker's; ok=false on non-positive base or currency mismatch) — used by `Dashboard.setDelta`; reuse it verbatim. [Source: internal/service/valuation/valuation.go `portfolioAsOf`/`Dashboard`/`setDelta`; internal/domain/change.go `PercentChange`]
- **`dashboardPage` + `web.DashboardView`** is the page/handler to extend (the widgets go below `view.Allocation`). The graceful-degradation pattern (a secondary widget error → empty state, the page still renders; error ≠ no-data) is established by the 5.3 chart and 5.4 allocation — mirror it. [Source: internal/http/router.go `dashboardPage`; web/pages.templ `DashboardPage`]
- **5.1 primitives:** `@Card`, `@Badge` (variants `BadgeGain`/`BadgeLoss`/`BadgeAccent`/`BadgeNeutral`), `kpiIcon`, the `text-accent`/`text-gain`/`text-loss`/`text-muted` tokens. The insight call-out is an accent `@Card`; the badge on each tx row is `@Badge`. **No new CSS likely needed** — reuse tokens; only `make css` if a genuinely new utility is added. [Source: web/components.templ; web/shell.go `badgeClass`]
- **Build/codegen:** `.templ` → `make generate` (commit `*_templ.go`); `make nofloat` stays green; `gofmt` clean. [Source: Makefile]

### Architecture invariants this story must honor

- **AD-10 — one canonical home per derived figure; UI renders, performs no financial math.** The insight % is `domain.PercentChange` (existing home); the recent-tx list is read-only formatting. `http` frames text and slices the top-5; no aggregation/rounding in the view. [Source: ARCHITECTURE-SPINE.md#AD-10]
- **AD-2 — derived on read; AD-9 — a transfer is one row.** The widget reads the ledger via `Register` (no snapshot, no double-count). [Source: #AD-2/#AD-9]
- **AD-11 — value-over-time derived from history, no snapshot.** The month-start Net Worth is `portfolioAsOf(firstOfMonth)` (prices/rates effective ≤ that date), never retro-repriced, never materialized. [Source: #AD-11]
- **AD-12 / AD-6 — convert-then-sum, partial totals surfaced.** Both as-of Net Worths use the existing round-once conversion; if a currency lacked a rate, the insight is flagged partial, never silently wrong or inverted. [Source: #AD-12/#AD-6; [[financas-epic4-decisions]]]
- **NFR-5 — no float in the financial core.** `PercentChange` is decimal; the widgets format strings only. `make nofloat` (scope `internal/{money,domain,service,store}`) stays green. [Source: Makefile `nofloat`]
- **NFR-4 — reasonable a11y.** The widget rows + "View all" are real links; the insight ▲/▼ has an `sr-only` label. [Source: prd.md §"Cross-Cutting NFRs"]

### Design intent (from the PRD/UX — apply here)

[Source: epics.md UX-DR5/UX-DR6; prd.md §"Aesthetic & Tone"]

- **UX-DR5 — Transaction history list:** recent transactions with a per-row icon, description, date, **signed colored amount (green income / red expense)**, and a Category/type badge; links into the full register. Realizes FR-8. Clean, scannable, the colour carries the in/out signal.
- **UX-DR6 — Insight call-out card:** **one bold accent-colored card** surfacing a single derived insight ("Your portfolio is up X% this month"), computed in `domain`, only rendered here. This is the dashboard's personality moment — make it feel alive, not a second KPI tile.
- **Aesthetic:** matches the 5.1 token system + the 5.2–5.4 cards; the insight is the bold accent call-out; the recent list is calm and scannable. Empty/partial states are calm and honest (UX-DR8).

### Previous-story intelligence (5.4 + 5.3/5.2) — load-bearing

[Source: 5-4-allocation-breakdown.md; 5-3-value-over-time-trend-chart.md; [[financas-epic5-progress]]; [[financas-epic4-decisions]]]

- **The handler-builds-view-struct seam** (`buildChart`/`buildAllocation`) is the pattern: add `buildInsight`; keep money/percent formatting in the handler, the figure in `domain`/`service`. Pre-formatted view structs in `web/shell.go`; the templ only emits markup.
- **Graceful degradation lesson (5.3/5.4):** a secondary-widget load error degrades to an empty/fallback state with the page still rendering — and **error ≠ no-data** (use a distinct fallback only where it matters; for the insight, a load error simply hides it, while "no month-start baseline" is the calm "add data" copy).
- **Templ escaping gotchas (5.4 review):** templ HTML-escapes `&`→`&amp;` in attributes and `'`→`&#39;` in text. Assert on escaped substrings in handler/render tests (e.g. avoid matching an apostrophe in copy).
- **As-of UTC convention (5.3 fix):** all as-of date comparisons use UTC calendar dates; `portfolioAsOf` normalizes to `dateOnlyUTC`. Compute the month start in **UTC** (`firstOfMonthUTC`) so it agrees with the ledger/price/rate cuts. The DB session is UTC (AD-8).
- **Shared-test-DB hygiene (5.4 live-smoke lesson):** the `internal/service/settings` tests share the base `financas` DB and assert the **default `display_currency` is USD**; the cross-account service tests also share it. After a live smoke that seeds the base DB, **restore `display_currency` to USD and delete the rows you added** before running the full suite, or `TestDisplayCurrencyLifecycle` (and others) will fail on polluted state. The valuation tests use `isolatedDB` (throwaway `CREATE DATABASE`, templated from `template1`) and are unaffected.
- **House style:** typed view structs in `web/shell.go`; table-of-`want`-substring render/handler tests; DB-gated service tests skip without `TEST_DATABASE_URL`/`DATABASE_URL`; `GOTOOLCHAIN=local`; commit regenerated `*_templ.go`; `make nofloat`/`gofmt` green; local DB host **5433**; dev login `owner`/`financas`; **one commit per story, push to `main`**.

### Project Structure Notes

- **Modified — service:** `internal/service/valuation/valuation.go` (`Insight` type + `Insight(ctx)` method + `firstOfMonthUTC` helper); `internal/service/valuation/valuation_test.go` (`TestInsight`).
- **Modified — http:** `internal/http/router.go` (`Valuation` interface `+Insight`; `dashboardPage` builds `Recent` via `Register` top-5 + `buildInsight`; `recentTxLimit` const); `internal/http/router_test.go` (stub `Insight` + canned insight; widget/insight handler tests; canned `Register` rows for the dashboard).
- **Modified — web:** `web/shell.go` (`InsightView` + `DashboardView.Recent`/`.Insight`); `web/pages.templ` (`insightCallout` + `recentTransactions` + `txTypeIcon` components, rendered in `DashboardPage`) → regenerated `web/pages_templ.go`; `web/shell_test.go` render tests. CSS only if a utility was added.
- **Modified — docs:** `README.md`.
- **NOT touched:** no `db/migrations`, no `db/query`, no `internal/store` regen (no `make sqlc`), no new module dependency, no new JS asset, no `web/embed.go` change, no change to `Register`/`Dashboard`/`Portfolio`/`Allocation`/`ValueSeries` behaviour.

### Testing standards

- **service (DB-gated):** `Insight` — up/down/`HasData`/`Partial` against `portfolioAsOf`-derived month-start vs now Net Worth; cross-checked with `domain.PercentChange`; empty/non-positive baseline → `HasData` false. Skip without DB.
- **http (DB-free, stub):** `/` renders the recent-tx widget (rows + signed amounts + "View all" → `/transactions`) and the insight sentence + ▲/▼ + accent; empty `Register` → widget empty state; `insightErr`/`!HasData` → insight fallback, page still 200 with KPIs; existing dashboard tests stay green (stub gains `Insight`).
- **web (pure render):** `insightCallout` populated (sentence + arrow + accent + partial) and empty (fallback); `recentTransactions` populated (rows, icon, badge, signed colour, "View all" link) and empty (copy).
- `go test ./...` green (web/http DB-free; DB-gated valuation test skips without DB). `go vet`, `gofmt -l`, `make nofloat` clean. Visual confirmed by the live smoke.

### References

- [Source: epics.md#Story 5.5] — ACs (recent-transactions widget: icon/description/date/signed colored amount/badge, links to register, UX-DR5; bold accent insight card computed in domain, only rendered here, UX-DR6, AD-10)
- [Source: epics.md UX-DR5/UX-DR6] — transaction history list + insight call-out
- [Source: prd.md §"Aesthetic & Tone"] — bold accent call-out; clean scannable list
- [Source: ARCHITECTURE-SPINE.md#AD-10] — one canonical derived-figure home; UI renders only; [#AD-2/#AD-9 ledger derived-on-read, transfer = one row; #AD-11 as-of, no snapshot; #AD-12/#AD-6 convert-then-sum/partial; NFR-5/NFR-4]
- [Source: internal/service/transaction/transaction.go `Register`/`RegisterRow`] — the cross-account newest-first ledger to slice top-5; [internal/http/router.go `transactionsRegister`/`registerAmount`] — the exact row mapping to reuse
- [Source: internal/service/valuation/valuation.go `portfolioAsOf`/`Dashboard`/`setDelta`] — the as-of seam + how PercentChange is applied; [internal/domain/change.go `PercentChange`] — the canonical %-change home to reuse
- [Source: web/pages.templ `DashboardPage`/`TransactionRows`; web/shell.go `DashboardView`/`RegisterRow`; web/components.templ `Card`/`Badge`] — where the widgets slot in + the row/badge/card primitives
- [Source: 5-4-allocation-breakdown.md; [[financas-epic5-progress]]] — the buildX view-seam, graceful-degradation, templ-escaping, UTC as-of, and shared-test-DB hygiene lessons
- [Source: Makefile] — `make generate` / `make nofloat`; committed-artifact rule; no `make sqlc`

## Dev Agent Record

### Agent Model Used

claude-opus-4-8[1m] (Opus 4.8, 1M context) — bmad-dev-story workflow.

### Debug Log References

- TDD per layer: service `Insight` tests (up/empty/partial, DB-gated) → http handler tests (insight sentence + ▲ + accent, empty/error fallback, recent-activity rows + "View all", empty ledger) → web render tests (`insightCallout`/`recentTransactions` up/down/empty). Each RED then GREEN.
- One formatting fix: the insight % first rendered as "4" (decimal trims the trailing zero); matched the KPI delta's `StringFixed(1)` so it reads "4.0%" consistently.
- `make generate` (templ) produced no `internal/store` diff (no new query). `make css` regenerated `app.css` to add the new accent-opacity utilities used by the call-out (`bg-accent/5`, `/15`, `border-accent/30`) — they are literal in the templ so Tailwind's source scan picks them up (no safelist needed). `gofmt -w` on `router_test.go`.
- **Live smoke gotcha:** a stale server from the 5.4 smoke still held `:8099`, so the first run served an old binary (no 5.5 widgets); killed it (`lsof -ti tcp:8099 | xargs kill`) and re-ran on `:8100`. Lesson reinforced: free the port before a smoke.

### Completion Notes List

- **Service (Task 1):** `valuation.Insight(ctx)` + `Insight` read-model + `firstOfMonthUTC` helper. Values the portfolio at `now` and at the first of the current month (UTC) via the existing `portfolioAsOf` (AD-11, no snapshot), reconciled by the canonical `domain.PercentChange` (AD-10 — no new percentage function). Returns the figure + Up/Down/HasData + a Partial flag (either valuation excluded a currency) + current NetWorth. `make nofloat` green (decimal only).
- **HTTP (Task 2):** `Valuation` interface `+Insight`; `dashboardPage` builds the insight via `buildInsight` (frames "Your net worth is up/down X.X% this month", `StringFixed(1)` to match the KPI delta; no-baseline → calm fallback) and the recent-activity widget by reusing `Transactions.Register(ctx, {})` and slicing the **top `recentTxLimit = 5`**, mapped exactly like `transactionsRegister` (reusing `registerAmount`). Both degrade gracefully (load error → hidden/empty; the page still renders). No new query/service method.
- **Web (Task 3):** `InsightView` + `DashboardView.Insight`/`.Recent` (reusing `RegisterRow`); `insightCallout` (bold **accent** card — `border-accent/30 bg-accent/5` — with the sentence, a ▲/▼ + `sr-only` label, a NetWorth context line, and a partial note), `recentTransactions` (compact list: `txTypeIcon` chip + description/category badge + date + signed colored amount via the `text-gain`/`text-loss` rule, transfers neutral; "View all" → `/transactions`; empty state), and the `txTypeIcon` glyph helper. Rendered below `@allocationCard` in `DashboardPage`. New accent-opacity utilities → `app.css`; no new dependency/JS.
- **Verification (Task 4):** `go build`/`vet`/`test -count=1 ./...` green incl. DB-gated valuation tests on local PG :5433; `make nofloat` green; `gofmt` clean. **Live smoke** (freshly-built binary on :8100, owner/financas, dev DB seeded with cash + a holding priced 100 at month-start / 120 today + in-month income/expense): `/` rendered the insight call-out ("Your net worth is down 0.2% this month" — direction reflects the dev DB's accumulated data) + the recent-activity widget (newest rows incl. the seeded "June bonus"/"Groceries", "View all" → `/transactions`); `/transactions` unchanged. Seed rows cleaned up and the shared base DB's `display_currency` restored to USD (5.4 lesson); full suite re-run ALL PASS afterward.
- **Independent Opus review: APPROVE WITH NITS (verdict PASS).** Applied **M1** — on an `Insight()` error the bold accent card shell rendered empty; `DashboardPage` now guards `@insightCallout` behind `HasData || Empty != ""`, so an error truly hides the card (the no-data fallback still shows). Applied **L1** — extracted `mapRegisterRows` shared by `/transactions` and the dashboard widget (no more duplicated mapping). Applied **N1** — changed the insight glyph from `✦` to `◆` to avoid colliding with the dividend row icon. Left **N2** (the `TestInsight` skip on the 1st of the month) as documented/acceptable — hardening would need `time.Now` injection. Build/vet/test/nofloat/gofmt re-verified green.
- **This completes Epic 5.**

### File List

- `internal/service/valuation/valuation.go` (modified — `Insight` type + `Insight(ctx)` method + `firstOfMonthUTC` helper)
- `internal/service/valuation/valuation_test.go` (modified — `TestInsight`, `TestInsightEmptyAndPartial`)
- `internal/http/router.go` (modified — `Valuation` interface `+Insight`; `dashboardPage` builds insight + recent-activity top-5; `buildInsight`; `recentTxLimit` const)
- `internal/http/router_test.go` (modified — `stubValuation.Insight` + `cannedInsight`; insight/recent-activity handler tests)
- `web/shell.go` (modified — `InsightView` + `DashboardView.Insight`/`.Recent`)
- `web/pages.templ` (modified — `insightCallout` + `recentTransactions` + `txTypeIcon` components in `DashboardPage`)
- `web/pages_templ.go` (regenerated)
- `web/shell_test.go` (modified — insight + recent-transactions render tests)
- `web/static/css/app.css` (rebuilt — accent-opacity utilities for the call-out)
- `README.md` (modified — insight call-out + recent-activity note)

## Change Log

| Date | Change |
| --- | --- |
| 2026-06-29 | Story 5.5 implemented (dev-story): added the dashboard's **recent-activity widget** (UX-DR5) and **insight call-out** (UX-DR6), completing Epic 5. New `valuation.Insight(ctx)` = month-over-month Net Worth % via `portfolioAsOf(now)` vs `portfolioAsOf(first-of-month UTC)` reconciled by the canonical `domain.PercentChange` (AD-10 — no new percentage); `firstOfMonthUTC` helper. `dashboardPage` frames the insight via `buildInsight` and builds the recent widget by reusing `Transactions.Register` (whole ledger newest-first) sliced to the top 5 — no new query/service method. `InsightView` + `insightCallout`/`recentTransactions`/`txTypeIcon` templ; bold accent call-out + signed-colored compact list linking to `/transactions`. Graceful degradation + partial/empty honesty. Build/vet/test (incl. DB-gated)/nofloat/gofmt green; live smoke confirmed both widgets end-to-end. No new query/migration/dependency/JS. Status → review. |
| 2026-06-29 | Story 5.5 drafted (create-story): add the dashboard's **recent-transactions widget** (UX-DR5) and **insight call-out** (UX-DR6), the final Epic 5 story. Decisions: **D1** the recent-tx widget reuses `Transactions.Register` (whole ledger newest-first) and slices the **top 5** — no new query/service method — mapping to the existing `web.RegisterRow` + a per-row `txTypeIcon`, linking to `/transactions`; **D2** the insight = **month-over-month Net Worth %** via `portfolioAsOf(now)` vs `portfolioAsOf(first-of-month UTC)` reconciled by the canonical `domain.PercentChange` (AD-10 — no new percentage function), framed as "Your net worth is up/down X% this month" by the http layer; **D3** month start = first of the current month at UTC midnight (matches the as-of UTC convention); **D4** insight rendered only here, bold **accent** card (UX-DR6); **D5** both widgets below the 5.4 allocation card (insight first, then recent). Partial-total honesty + calm empty/fallback states; graceful degradation (secondary-widget error never 500s the page). New `valuation.Insight`; `dashboardPage` recent-tx top-5 + `buildInsight`; `InsightView` + `insightCallout`/`recentTransactions` templ. No new query/migration/dependency/JS. Status → ready-for-dev. |
