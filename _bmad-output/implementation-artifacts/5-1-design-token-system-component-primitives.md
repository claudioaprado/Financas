---
baseline_commit: c669327
---

# Story 5.1: Design-token system & component primitives

Status: review

<!-- Note: Validation is optional. Run validate-create-story for quality check before dev-story. -->

## Story

As the owner,
I want a consistent visual system,
so that the app looks clean and modern everywhere.

## Acceptance Criteria

From `epics.md` → Epic 5 → Story 5.1 (realizes UX-DR7, UX-DR8, NFR-4). **Given** the app shell from Epic 1, **When** the design system is implemented, **Then**:

1. Tailwind tokens define **rounded cards (~16px)**, **soft shadows**, a **type scale**, and a **semantic palette** (green = gain, red = loss, neutral, one bold accent) (UX-DR7).
2. Reusable **card**, **badge**, and **large-number** components exist and **render decimal money with currency and sign** (UX-DR8).
3. Components meet **reasonable accessibility defaults** — keyboard-usable, legible contrast (NFR-4).

> **Scope (this story):** the **shared visual vocabulary** the rest of Epic 5 builds on. The palette + shape tokens **already exist** in `web/static/css/input.css` (`@theme`: `--color-{surface,ink,muted,accent,gain,loss}`, `--radius-card`, `--shadow-card` — added with the Epic-1 shell). This story (a) **completes the token system** by adding the missing **type scale** (UX-DR7's one not-yet-expressed token group), and (b) builds the **reusable templ component primitives** — a **Card**, a **Badge**, and a **large-number / money** component — that encode those tokens once so every page renders consistently (UX-DR8), each with **accessibility defaults** (semantic markup, contrast, and gain/loss never signalled by colour alone — the **sign carries the meaning too**, NFR-4). The components are exercised by **render unit tests**. **NOT in this story:** the **Dashboard KPI cards** (5.2 — consumes these primitives), the **value-over-time chart** (5.3), the **allocation breakdown** (5.4), the **transaction-history widget + insight card** (5.5). No Go `domain`/`service` change, no DB, no new dependency, no new route. This is a **web-layer-only** story (templ + CSS tokens); it does **no financial math** (AD-1) — money arrives pre-formatted from the handler, exactly as in 4.3/4.4.

## Tasks / Subtasks

- [x] **Task 1 — Complete the design-token system: add the type scale (AC: #1)**
  - [x] In `web/static/css/input.css`, **extend the existing `@theme` block** (do NOT remove or restyle the palette/shape tokens already there — 4.x pages depend on `bg-surface`, `text-ink`, `text-muted`, `text-accent`, `text-gain`, `text-loss`, `rounded-card`, `shadow-card`). Add the **type scale** UX-DR7 calls for. Tailwind v4 already ships a default `text-*` size scale (the pages use `text-sm`/`text-2xl`/`text-3xl`/`text-4xl`); this story makes the scale **semantic and intentional** rather than ad-hoc:
    - Define semantic text-size tokens for the roles the dashboard needs — e.g. `--text-hero` (the Net Worth / portfolio hero number), `--text-stat` (a KPI card's number), `--text-label` (the muted caption above a number) — each with a sensible size/line-height, OR (if you prefer not to add custom size tokens) **document the chosen scale** in a comment and have the component primitives (Task 2) encode it. Either way the **primitives** are the single home for "what size is a hero number" — pages must not re-pick sizes ad-hoc.
    - Keep it minimal and token-driven; no colour or radius changes. The `@theme` comment already says *"Story 5.1 extends it with card/badge/large-number component primitives"* — keep that promise.
  - [x] `make css` (rebuild `web/static/css/app.css`) and **commit the rebuilt `app.css`** (it is a committed artifact). Confirm the existing pages still render unchanged (no token was renamed/removed).

- [x] **Task 2 — Reusable component primitives: Card, Badge, large-number/money (AC: #2, #3)**
  - [x] Add a new templ file `web/components.templ` (package `web`) housing the shared primitives, with a house-style package/templ doc comment citing UX-DR7/UX-DR8/NFR-4 and AD-1 (web renders only; money is pre-formatted upstream). Define:
    - **`templ Card(class string)`** — wraps `{ children... }` in the canonical card surface (`rounded-card bg-white p-6 shadow-card`), appending the optional `class` for variants (e.g. an accent insight card in 5.5, a tighter KPI card in 5.2). Use `templ.KV`/class-list composition so callers can extend without restyling. This is the single home for "what a card looks like" — replaces the `<section class="rounded-card bg-white p-6 shadow-card">` literal repeated across pages.
    - **`templ Badge(label string, variant BadgeVariant)`** — a small pill (`rounded … px-2 py-0.5 text-xs font-medium …`) with semantic variants. Add a `BadgeVariant` type (string) in `web/shell.go` with values `BadgeNeutral`, `BadgeGain`, `BadgeLoss`, `BadgeAccent`; map each to the token colour (`text-gain` / `text-loss` / `text-accent` / muted-neutral) via `templ.KV`. Used by the register/holdings type chips and (5.5) the insight badge.
    - **`templ Amount(m MoneyText, size AmountSize)`** — the **large-number / money** primitive (UX-DR8's headline requirement). It renders **pre-formatted** money with its **currency and sign** and the gain/loss colour. Add to `web/shell.go`:
      - `type MoneyText struct { Display string; Positive bool; Negative bool }` — `Display` is the already-formatted string (e.g. `"1234.5000 BRL"` from `money.Money.String()`); `Positive`/`Negative` are the colour/sign flags the **handler** computes from the rounded amount (the established 4.3/4.4 `UnrealizedPositive`/`UnrealizedNegative` convention — **the web layer does no math, AD-1**).
      - `type AmountSize string` with `AmountHero`, `AmountStat`, `AmountInline` mapping to the Task-1 type scale.
      - **Accessibility (NFR-4) — gain/loss must NOT be colour-only.** When `Positive`/`Negative`, the component prepends a textual sign (`+` / `−`, or a `▲`/`▼` glyph) **and** an accessible label (e.g. wrap the sign in a visually-meaningful element or include screen-reader text like `<span class="sr-only">gain </span>`) so the direction is conveyed without relying on colour. (If you add an `.sr-only` utility, define it in `input.css`.) Document this decision in the templ comment — it's a deliberate NFR-4 affordance, not decoration.
  - [x] **Prove they compose without regressions (light, surgical):** refactor **one or two** existing usages to the new primitives as a demonstration — recommended: the **`InvestmentsPage` Net Worth hero** (use `Amount(... , AmountHero)`) and a card or two via `@Card(...)`. Keep this minimal and behaviour-preserving (same rendered numbers, same links, same colours); broad page rewrites belong to the consuming stories. Do **not** touch `domain`/`service` or handler math — only swap the markup, passing the same pre-formatted strings/flags. Re-run the affected handler/render tests so nothing regresses (`internal/http/router_test.go`, `web/shell_test.go`).

- [x] **Task 3 — Tests, verify, docs (AC: all)**
  - [x] Add `web/components_test.go` using the existing `renderToString` helper pattern (`web/shell_test.go`): assert each primitive renders the right tokens and behaviour —
    - `Card("max-w-md")` wraps children and emits `rounded-card`, `shadow-card`, and the extra class.
    - `Badge` renders the label and the correct colour token per variant (`text-gain` for `BadgeGain`, `text-loss` for `BadgeLoss`, etc.).
    - `Amount(MoneyText{Display:"1234.5000 BRL", Positive:true}, AmountHero)` renders the currency string, the **gain sign** (`+`/`▲`) **and** the gain colour, and the hero size; a `Negative` amount renders the loss sign + loss colour; a neutral amount renders neither sign nor colour. Assert the **non-colour sign** is present (the NFR-4 guard).
  - [x] `GOTOOLCHAIN=local go build ./... && go vet ./... && go test ./...` green (the web package render tests run without a DB). `make nofloat` **stays green** (this story adds no math anywhere). `gofmt -l` clean. Run `make generate` after any `.templ` edit and **commit the regenerated `*_templ.go`** + the rebuilt `app.css`.
  - [x] **Live smoke** (compose db :5433 + run, owner/financas): open the **Dashboard** and **`/investments`** — confirm the refactored hero/cards render identically to before (same numbers, colours, links) and that a gain shows a sign as well as green, a loss a sign as well as red (resize the window to confirm the responsive shell/cards still behave). No visual regression on the other pages (palette/shape tokens unchanged).
  - [x] Update `README.md` (the **App shell & design tokens** section): note that `web/components.templ` provides the shared **Card / Badge / Amount** primitives over the `@theme` token system (palette, ~16px `rounded-card`, `shadow-card`, semantic type scale), that money is rendered **pre-formatted** with currency + sign and gain/loss is conveyed by **sign as well as colour** (NFR-4), and that Epic 5's dashboard widgets (5.2–5.5) compose these primitives rather than re-styling ad-hoc.

## Dev Notes

### Scope boundary — what this story does NOT do (read first)

- **NO dashboard / KPI cards / chart / allocation / widgets** — those are **5.2 (KPI cards), 5.3 (trend chart), 5.4 (allocation), 5.5 (history widget + insight)**. They **consume** the primitives this story creates. Do not build the dashboard hero here; 5.2 rebuilds `DashboardPage` (today a `ComingSoon`-style placeholder) using `Card` + `Amount`.
- **NO Go `domain`/`service`/`store` change, NO DB, NO new route, NO new dependency.** This is a **web-layer-only** story (templ components + CSS `@theme` tokens). The money the components render is already formatted by the handlers (AD-1) — the components receive strings + boolean flags, never `money.Money` or `decimal.Decimal`, and do **no** arithmetic.
- **NO palette/shape retheme.** The existing `@theme` colours, `--radius-card`, and `--shadow-card` are load-bearing for every 1.x–4.x page; only **add** the type scale, do not rename or restyle. A renamed token silently breaks live pages (Tailwind utilities are generated from the token names).
- **NO broad page refactor.** Refactor at most one or two usages to *demonstrate* the primitives; wholesale adoption happens naturally as 5.2–5.5 build the dashboard. Keep regressions at zero.

### What already exists (reuse, don't rebuild)

[Source: web/static/css/input.css; web/shell.templ; web/pages.templ; [[financas-epic1-progress]]; 4-3-manual-security-prices.md; 4-4-portfolio-valuation-net-worth.md]

- **The `@theme` token system is already in `web/static/css/input.css`** (Tailwind v4, CSS-first — there is **no `tailwind.config.js`**): semantic palette (`--color-surface/ink/muted/accent/gain/loss`, oklch), `--radius-card: 1rem` (~16px), `--shadow-card`. Its comment already names this story: *"Story 5.1 extends it with card/badge/large-number component primitives."* The **type scale is the one UX-DR7 token group not yet present** — that's the CSS half of this story.
- **The card surface is already an established literal** repeated across pages: `<section class="rounded-card bg-white p-6 shadow-card">` (Dashboard, Investments, Settings, Accounts, …). `Card` consolidates it.
- **The gain/loss colour convention is established (4.3/4.4):** `text-gain` (green) / `text-loss` (red) / neutral, driven by handler-computed `Positive`/`Negative` bools (`UnrealizedPositive`/`UnrealizedNegative`, `RealizedChip.Positive/Negative`) — **never** `strings` logic in templ, **never** math in templ. `Amount`/`Badge` formalize this. Note the existing chips/rows convey gain/loss by **colour only** today; this story's `Amount` adds the **non-colour sign** for NFR-4 (a genuine improvement, applied to the hero it refactors; the broader rollout rides along with 5.2–5.5).
- **templ component idioms to mirror:** `templ X(args) { … }` with `{ children... }` for wrappers (see `Shell`), class composition with `templ.KV("class", cond)` (see `navLink`), and **typed view structs in `web/shell.go`** carrying pre-formatted strings + bools (see `RealizedChip`, `PortfolioHoldingRow`, `HoldingRow`). Add `BadgeVariant`, `AmountSize`, `MoneyText` there.
- **Render-test harness exists:** `web/shell_test.go`'s `renderToString(t, component)` renders a `templ.Component` to a string for assertions — no DB, no server. Use it for `components_test.go`.
- **Build/codegen:** `.templ` → committed `*_templ.go` via `make generate` (`go tool templ generate`); CSS → committed `web/static/css/app.css` via `make css` (`npm run build:css`, Tailwind v4 `--minify`). Both artifacts are committed. [Source: Makefile]

### Architecture invariants this story must honor

- **AD-1 — layering / web renders only.** The web layer performs **no business logic, no SQL, no financial math**; it translates pre-computed values into HTML. The new primitives take **already-formatted** money strings + boolean flags (computed by the handler from the rounded `money.Money`, exactly as 4.3/4.4 do) — they must not import `money`/`decimal` for arithmetic or format numbers themselves. [Source: ARCHITECTURE-SPINE.md#AD-1; internal/http/router.go renderInvestmentDetail / investmentsPage]
- **AD-10 — one canonical home.** Just as `domain` is the single home for a financial figure, these primitives are the single home for *how a card/badge/number looks*. Pages must compose them, not re-pick `rounded-card …` / `text-4xl` ad-hoc. (5.2–5.5 depend on this to stay visually consistent.) [Source: #AD-10]
- **No new float anywhere (NFR-5 / `make nofloat`).** Trivially satisfied — this story adds no Go math. Keep it that way. [Source: Makefile `nofloat`]

### Design intent (from the PRD — apply here)

[Source: prd.md §"Aesthetic & Tone", §"Cross-Cutting NFRs"]

- **"Modern UI is a stated reason this product exists — a requirement, not a nice-to-have."** Feel: **clean, fast, uncluttered**; the anti-reference is a dense, dated desktop UI. The **Net Worth / portfolio number and gain/loss are the visual hero** — which is exactly why the **`Amount` large-number primitive** matters most: it's the component the hero number and KPI cards are built from. Tone of any text: plain, calm, neutral — no gamification.
- **Accessibility = reasonable defaults (NFR-4):** keyboard-usable, legible contrast; **no formal WCAG target** for this personal build — so aim for sensible, not exhaustive. Concretely: semantic elements, focus-visible defaults preserved, contrast from the oklch palette, and **gain/loss not conveyed by colour alone** (the sign carries it) — that last one is the highest-value, lowest-cost a11y affordance for a finance app and is cheap to bake into `Amount`.

### Previous-story intelligence (Epic 1 + 4.3/4.4) — load-bearing

[Source: [[financas-epic1-progress]]; [[financas-epic4-decisions]]; 4-4-portfolio-valuation-net-worth.md]

- **Epic 1 shipped the shell + the initial `@theme` tokens** and the five-item nav; the shell (`web/shell.templ`) applies `bg-surface text-ink` and uses `rounded-card`/`shadow-card`. Don't disturb the shell's structure or nav (4.x and the auth/active-nav tests assert on it).
- **4.4 (just merged, HEAD `c669327`)** added `InvestmentsView`/`RealizedChip`/`PortfolioHoldingRow` to `web/shell.go` and `InvestmentsPage` to `web/pages.templ`, with the `Positive`/`Negative` colour-flag pattern and pre-formatted money strings — the **exact pattern** `Amount`/`Badge` should generalize. The Investments hero (`<p class="text-4xl font-bold">{ v.NetWorth }</p>`) is the recommended demonstration target for `Amount(..., AmountHero)`.
- **House testing style:** table-of-`want`-substrings render assertions (`web/shell_test.go`, `internal/http/router_test.go`). Mirror it.
- **Environment:** build/test `GOTOOLCHAIN=local`; `make generate` after `.templ`, `make css` after CSS, commit both generated artifacts; `make nofloat` stays green; `gofmt -l` clean. Local DB host **5433** (`docker compose up -d db`) only needed for the live smoke (this story's unit tests need no DB). Dev login `owner`/`financas`. **Commit + push to `main` when done** (trunk-based, one commit per story — owner's standing instruction). `baseline_commit` is HEAD `c669327`.

### Project Structure Notes

New: `web/components.templ` (+ regenerated `web/components_templ.go`) and `web/components_test.go`. Modified: `web/static/css/input.css` (type-scale tokens) + rebuilt `web/static/css/app.css`; `web/shell.go` (`BadgeVariant`, `AmountSize`, `MoneyText` types + variant→token mapping helpers if needed); a light demonstration refactor in `web/pages.templ` (+ rebuilt `web/pages_templ.go`) — recommended the `InvestmentsPage` hero and a card or two; `README.md` (App shell & design tokens). No Go `domain`/`service`/`store`/`cmd` change, no migration, no `db/query` change, no sqlc regen, no new module dependency.

### Testing standards

- **web (pure render, no DB):** `components_test.go` via `renderToString` — Card emits card tokens + passthrough class + children; Badge renders label + correct colour token per variant; `Amount` renders currency string + **non-colour sign** + correct gain/loss colour + size, for positive / negative / neutral. Keep existing `web/shell_test.go` and `internal/http/router_test.go` green (the demonstration refactor must not change asserted substrings — if it does, update them to the equivalent primitive output and explain in the commit).
- `go test ./...` green with **no DB** (web tests are DB-free; DB-gated service tests skip). `go vet`, `gofmt -l`, and `make nofloat` clean. Visual no-regression confirmed by the live smoke.

### References

- [Source: epics.md#Story 5.1] — ACs (UX-DR7 tokens incl. type scale; UX-DR8 card/badge/large-number rendering money with currency+sign; NFR-4 accessibility)
- [Source: epics.md#Epic 5] — the dashboard hero this primitives layer feeds (5.2 KPI cards, 5.3 chart, 5.4 allocation, 5.5 history+insight)
- [Source: prd.md §"Aesthetic & Tone", §"Cross-Cutting NFRs"] — modern-UI-as-requirement; hero = Net Worth/gain-loss; accessibility = reasonable defaults
- [Source: ARCHITECTURE-SPINE.md#AD-1] — web renders only, no math; [#AD-10] — one canonical home (here: visual primitives)
- [Source: web/static/css/input.css] — the existing `@theme` token system to extend; [web/shell.templ] — templ idioms (children, `templ.KV`); [web/shell.go] — typed view-struct pattern to follow; [web/shell_test.go] — `renderToString` test harness
- [Source: 4-4-portfolio-valuation-net-worth.md; 4-3-manual-security-prices.md] — the `Positive`/`Negative` colour-flag + pre-formatted-money pattern `Amount`/`Badge` generalize; the Investments hero refactor target
- [Source: Makefile] — `make generate` / `make css` / `make nofloat` and the committed-artifact rule

## Dev Agent Record

### Agent Model Used

claude-opus-4-8[1m] (Opus 4.8, 1M context) — bmad-dev-story workflow.

### Debug Log References

- TDD: wrote `web/components_test.go` first (RED — `Card`/`Badge`/`Amount`/types undefined), then added the types/helpers to `web/shell.go` + `web/components.templ`, `make templ`, GREEN.
- `sr-only` and `tabular-nums` are Tailwind v4 built-in utilities — no custom CSS needed; confirmed both (plus the new `text-hero`/`text-stat`/`text-label`) are emitted into the rebuilt `web/static/css/app.css` after `make css`.

### Completion Notes List

- **Type scale (Task 1):** added `--text-hero` (2.25rem, matching the prior `text-4xl` hero so existing pages don't shift), `--text-stat` (1.875rem), `--text-label` (0.875rem) to the existing `@theme` in `input.css`. Palette/shape tokens untouched (no rename/restyle — 1.x–4.x pages depend on them). Rebuilt + committed `app.css`.
- **Primitives (Task 2):** new `web/components.templ` with `Card(class)` (canonical card surface + passthrough class, `{ children... }`), `Badge(label, BadgeVariant)` (semantic pill), and `Amount(MoneyText, AmountSize)` (pre-formatted money + currency + sign at a type-scale size). Added `BadgeVariant`/`AmountSize`/`MoneyText` types + `badgeClass`/`amountClass` helpers to `web/shell.go`. **AD-1 honored:** components take pre-formatted strings + `Positive`/`Negative` flags — no `money`/`decimal` import, no math. **NFR-4:** gain shows a leading `+`, loss a leading `−`, each paired with a `sr-only` "gain"/"loss" label, so direction never depends on colour alone; neutral shows no sign and no colour.
- **Demonstration refactor (Task 2):** `InvestmentsPage` now wraps its card via `@Card("")` and renders the Net Worth hero via `@Amount(..., AmountHero)` and Portfolio value via `@Amount(..., AmountStat)`, with `text-label` captions — behaviour-preserving (same numbers, links, colours). The realized-G/L chips, holdings table, and warnings are unchanged; no `domain`/`service`/handler change. Broad adoption rides along with 5.2–5.5.
- **Tests (Task 3):** `web/components_test.go` (via the existing `renderToString` harness) asserts Card tokens + passthrough class; Badge label + colour per variant; and `Amount` for gain (value + `text-gain` + `text-hero` + `+` sign + `gain` sr-text, not loss-coloured), loss (`text-loss` + `text-stat` + `−` + `loss`, not gain-coloured), and neutral (value, no colour, no sign).
- **Verification:** `go build ./...`, `go vet ./...`, `go test -count=1 ./...` (incl. DB-gated, all green), `make nofloat` green (this story adds no Go math), `gofmt -l` clean on the touched `.go` files. Regenerated `web/components_templ.go` + `web/pages_templ.go` and rebuilt `web/static/css/app.css` committed. Existing `web/shell_test.go` and `internal/http/router_test.go` pass unchanged (the demo refactor preserved all asserted substrings).
- **Live smoke (freshly-built binary, owner/financas, Display = BRL with seeded multi-currency data + USD→BRL rate):** `/investments` renders the Net Worth hero via `text-hero`, Portfolio value via `text-stat`, captions via `text-label`, the Card surface (`rounded-card bg-white p-6 shadow-card`), and the holdings (PETR4/VOO) — no visual regression; Dashboard + shell nav intact, both pages HTTP 200. (Re-seeded the dev DB for the smoke; display currency reset to USD afterward.)

### File List

- `web/components.templ` (new)
- `web/components_templ.go` (generated, new)
- `web/components_test.go` (new)
- `web/shell.go` (modified — `BadgeVariant`/`AmountSize`/`MoneyText` types + `badgeClass`/`amountClass` helpers)
- `web/static/css/input.css` (modified — type-scale tokens)
- `web/static/css/app.css` (rebuilt)
- `web/pages.templ` (modified — `InvestmentsPage` demo refactor to `Card`/`Amount`)
- `web/pages_templ.go` (regenerated)
- `README.md` (modified — App shell & design tokens section)

## Change Log

| Date | Change |
| --- | --- |
| 2026-06-29 | Story 5.1 implemented (dev-story): added the semantic **type scale** (`text-hero`/`text-stat`/`text-label`) to the existing `@theme` (palette/shape untouched); new `web/components.templ` with **Card / Badge / Amount** primitives over the tokens — `Amount` renders pre-formatted money + currency + sign at a type-scale size, with gain/loss conveyed by **sign + sr-only label as well as colour** (NFR-4) and no math in the web layer (AD-1); types in `web/shell.go`. Light demo refactor of `InvestmentsPage` (Card + Amount), behaviour-preserving. Render unit tests via `renderToString`. Build/vet/test/nofloat/gofmt green; live smoke confirmed no visual regression. Status → review. |
| 2026-06-29 | Story 5.1 drafted (create-story): complete the design-token system by adding the **type scale** to the existing `@theme` (palette/shape already shipped in Epic 1), and build reusable **Card / Badge / Amount (large-number)** templ primitives in a new `web/components.templ` that render **pre-formatted** money with **currency + sign** (UX-DR8) over the tokens, with **accessibility defaults** incl. gain/loss conveyed by **sign as well as colour** (NFR-4). Render unit tests via the existing `renderToString` harness; a light demonstration refactor (Investments hero + a card) proves composition without regressions. Web-layer-only — no `domain`/`service`/DB/route/dependency change; `make nofloat` stays green. Stories 5.2–5.5 consume these primitives. Status → ready-for-dev. |
