---
stepsCompleted: ["step-01", "step-02", "step-03", "step-04"]
inputDocuments:
  - "_bmad-output/planning-artifacts/prds/prd-Financas-2026-06-28/prd.md"
  - "_bmad-output/planning-artifacts/prds/prd-Financas-2026-06-28/addendum.md"
  - "_bmad-output/planning-artifacts/architecture/architecture-Financas-2026-06-28/ARCHITECTURE-SPINE.md"
  - "UX visual reference (owner-supplied dashboard mockup, cached image)"
---

# Financas - Epic Breakdown

## Overview

This document provides the complete epic and story breakdown for Financas, decomposing the requirements from the PRD and Architecture spine into implementable stories. Financas is a single-user, browser-based, investment-focused personal finance manager (Go + PostgreSQL, layered "onion", Docker→Azure). No formal UX design contract exists, but the owner supplied a visual reference (a modern finance dashboard) captured as UX Design Requirements below; the PRD's Aesthetic & Tone section adds light guidance (modern, clean, responsive; Net Worth / portfolio value is the visual hero; charts legible; plain calm tone).

## Requirements Inventory

### Functional Requirements

FR-1: Manage accounts — create, rename, archive an Account with name, type (cash | credit | investment), and base Currency. Investment Accounts carry a cash balance; credit Accounts track a balance owed; archived Accounts are excluded from current Net Worth but retained for history.
FR-2: Manage currencies & exchange rates — choose a Display Currency; enter/maintain effective-dated Exchange Rates (USD↔BRL); convert aggregated figures to the Display Currency; prompt when a needed rate is missing (no inversion, no auto-fetch).
FR-3: Manage securities — add a Security with symbol, name, type (stock | ETF | fund | other), and quote Currency; prevent duplicate symbols.
FR-4: Derive holdings from transactions — compute each Holding's quantity and Cost Basis (average-cost) from its Account's investment Transactions; Holdings are read-only; zero-quantity Holdings hidden from active portfolio but retained.
FR-5: Record investment transactions — add/edit/delete Buy, Sell, Dividend (security, quantity, price, date, account, optional fees). Buy debits the investment Account's cash and adds to Cost Basis (incl. fees); Sell credits cash, reduces basis proportionally, records realized Gain/Loss; cash Dividend credits cash (no qty/basis change); oversell rejected.
FR-6: Record cash & credit transactions and transfers — add/edit/delete Income, Expense, Transfer with amount, date, account, optional Category. Expense on credit increases balance owed; Transfer moves value between the owner's own Accounts without double-counting (cross-currency records both legs).
FR-7: Manage categories & classify transactions — create/edit income- or expense-type Categories; assign one to each Income/Expense Transaction; filter and sum by Category; guarded delete when in use.
FR-8: Browse & filter the register — view Transactions per Account or across all, newest-first, filter by type, Security, or Category without page reload.
FR-9: Update security prices — owner enters/updates each Security's Price manually, effective-dated; most recent Price used for Valuation; staleness visible; no online/automated/real-time feed.
FR-10: Value the portfolio & net worth — compute per-Holding Valuation and Gain/Loss, Portfolio total, and Net Worth (assets − credit owed) in the Display Currency; surface cumulative realized Gain/Loss.
FR-11: Portfolio dashboard — on login, show total Portfolio value, Net Worth, period change, total Gain/Loss, and a Holdings breakdown in the Display Currency; period change shows "—" until a prior sample exists; Holding rows link to detail.
FR-12: Allocation & performance views — show Portfolio allocation (by Security and/or Account) summing to 100%, and a value-over-time chart derived from effective-dated Price/Exchange Rate history.
FR-13: Import transactions from a file — import cash/credit Transactions into a chosen Account from a tab-delimited `date / description / value` file (date `dd/mm/yy` or `dd/mm/yyyy`; Brazilian number format; value sign → Income/Expense; value in the Account's Currency); preview before commit; failed rows reported without aborting the batch; idempotent re-import.
FR-14: Authenticate the owner — sign in with credentials; session persists until logout/expiry; unauthenticated requests rejected; credentials stored hashed (argon2id); inactivity timeout.
FR-15: Export & restore data — export all authored data to a re-importable file (Accounts, Securities, Transactions, Categories, Prices, Exchange Rates); restore a fresh instance reproducing the same balances, Holdings, and Net Worth.

### NonFunctional Requirements

NFR-1 (Security): authenticated access only; served over HTTPS; credentials hashed with argon2id; financial data private to the single owner.
NFR-2 (Reliability/Durability): financial data persists reliably in PostgreSQL with the export/restore path (FR-15) so a host loss doesn't lose history.
NFR-3 (Performance): dashboard and register feel instant at a single user's data volume (years of personal transactions).
NFR-4 (Accessibility): reasonable defaults — keyboard-usable forms, legible contrast; no formal WCAG target.
NFR-5 (Correctness): exact decimal arithmetic for money and quantities end-to-end (NUMERIC + shopspring/decimal); floating-point money forbidden, including JSON and rendering.

### Additional Requirements

**Greenfield scaffold (drives Epic 1, Story 1):**
- Initialize Go module with the layered "onion" structure: `cmd/server/`, `internal/{domain,money,service,store,http}`, `web/` (templ + Tailwind + static), `db/migrations/` (goose), `db/query/` (sqlc).
- `Dockerfile` (single image: server + embedded templ/static assets) and `docker-compose.yml` (app + PostgreSQL) for local dev mirroring prod.
- Wire goose migrations, sqlc codegen, templ generation, Tailwind build, HTMX, and chi router with auth + CSRF middleware.

**Stack (pinned; verified current June 2026):** Go 1.26.4, PostgreSQL 18.4, chi v5, pgx v5, sqlc, goose, templ, HTMX 2.x, Tailwind 4.x, shopspring/decimal.

**Architectural invariants every story must honor (from the spine's 12 ADs):**
- AD-1 layered dependency direction; AD-2 transaction ledger = single source of truth, all figures (Holdings, balances, Valuation, Net Worth, realized gain) derived on read; AD-3 single mutation path, one DB transaction per use-case; AD-4 decimal money never float; AD-5 store native currency, convert only at read; AD-6 owner-entered effective-dated Prices/Rates, no external feed, rates directional (no inversion); AD-7 single-owner auth (argon2id, no tenant column); AD-8 single container → Azure Container Apps + Azure DB for PostgreSQL; AD-9 a Transfer is one two-account row; AD-10 one canonical `domain` function per derived figure (`http` does no financial math); AD-11 value-over-time derived from effective-dated history (no snapshot table); AD-12 banker's rounding, convert-then-sum, allocation reconciled to 100%, cumulative realized gain converted at each Sell's effective rate.

**Conventions:** bigint identity PKs; `NUMERIC(19,4)` money/price, `(28,10)` quantity, `(18,8)` rate; ISO-4217 currency codes; `DATE` for calendar/effective dates, `timestamptz` UTC for instants; import dedup key `(account_id, date, description, value)` + per-row hash + two-digit-year pivot (00–69→2000s, 70–99→1900s); income-type Category only on Income, expense-type only on Expense; amounts stored as non-negative magnitudes with direction from `Transaction.type`; export authored-state-only with identity-insert restore; Go 1.25+ `http.CrossOriginProtection` for CSRF.

**Deployment:** single Docker image to Azure Container Apps, backed by Azure Database for PostgreSQL Flexible Server; config/secrets via environment only.

### UX Design Requirements

No formal `bmad-ux` contract exists, but the owner provided a **visual reference** (a modern finance dashboard — light theme, rounded cards, large hero numbers, pastel accents with one bold call-out; cached at `~/.claude/image-cache/.../1.png`). These UX-DRs derive from it and the PRD's Aesthetic & Tone, and govern the UI-facing stories. They honor AD-10 (the UI renders; it performs no financial math).

UX-DR1: **Dashboard-first shell** — a personalized greeting header ("Welcome back, {owner}") and top horizontal navigation (Dashboard · Investments · Transactions · Accounts · Analytics). Light theme, responsive (desktop + mobile web).
UX-DR2: **KPI summary card row** — a row of summary cards at the top of the dashboard, each with an icon chip, a large bold number in the Display Currency, and a small period-change delta (▲/▼ %, green up / red down). For Financas: Net Worth, Portfolio Value, Total Gain/Loss, Cash. Realizes FR-11.
UX-DR3: **Primary trend chart** — a prominent area/line chart of portfolio value / Net Worth over time, with a range toggle (e.g. monthly). Realizes FR-12 (value-over-time).
UX-DR4: **Allocation / breakdown visual** — a bar/donut breakdown card (allocation by Security and/or Account; optionally spending by Category). Allocation reconciled to 100% per AD-12. Realizes FR-12 / FR-7.
UX-DR5: **Transaction history list** — recent Transactions with a per-row icon, description, date, signed colored amount (green income / red expense), and a Category/type badge; links into the full register. Realizes FR-8.
UX-DR6: **Insight call-out card** — one bold accent-colored card surfacing a single derived insight (e.g. "Your portfolio is up X% this month"), computed in `domain` and only rendered here.
UX-DR7: **Visual system / design tokens** — rounded-corner cards (~16px) with soft shadows, generous whitespace, a defined type scale (large bold numerals as hero), and a semantic palette: green = gain/positive, red = loss/negative, plus neutral + one bold accent. Tailwind tokens; consistent across all screens.
UX-DR8: **Money & state formatting** — amounts always show currency and sign with consistent decimal formatting (decimal values from the backend, never reformatted into floats); empty/loading/error states for cards and lists (e.g. period change shows "—" until a prior sample exists, per FR-11).

### FR Coverage Map

FR-1: Epic 2 — manage cash/credit/investment accounts
FR-2: Epic 2 — display currency + effective-dated FX rates
FR-3: Epic 4 — manage securities
FR-4: Epic 4 — derive holdings (average-cost)
FR-5: Epic 4 — investment transactions (Buy/Sell/Dividend + cash flow)
FR-6: Epic 3 — cash/credit transactions & transfers
FR-7: Epic 3 — categories & classification
FR-8: Epic 3 — browse & filter the register
FR-9: Epic 4 — manual security prices
FR-10: Epic 4 — portfolio valuation & net worth
FR-11: Epic 5 — portfolio dashboard
FR-12: Epic 5 — allocation & value-over-time
FR-13: Epic 3 — file import
FR-14: Epic 1 — authentication
FR-15: Epic 6 — export & restore

## Epic List

### Epic 1: Foundation & Secure Access
Stand up the running, deployable app and let the owner securely sign in. Greenfield scaffold (Go onion structure, Docker + docker-compose, goose migrations, sqlc, templ/Tailwind/HTMX, chi + CSRF, the shared `money`/decimal package), single-user authentication with sessions, the dashboard-style app shell (greeting header + top nav per UX-DR1), and an Azure-deployable image. After this epic the owner can log into an empty but real, secure, deployed Financas.
**FRs covered:** FR-14 (+ scaffold, NFR-1 security, NFR-5 decimal foundation, UX-DR1, AD-1/3/4/7/8, Azure deploy)

### Epic 2: Accounts & Currencies
Let the owner set up their financial structure: create and manage cash, credit, and investment Accounts (each with a base Currency), choose a Display Currency, and enter effective-dated USD↔BRL Exchange Rates. After this epic the owner has all their accounts and currency scaffolding in place.
**FRs covered:** FR-1, FR-2 (AD-5, AD-6, AD-12 conventions)

### Epic 3: Transactions, Categories & Import
Full cash/credit bookkeeping: record Income, Expense, and Transfer (including cross-currency two-leg and credit-card payments), classify with income/expense Categories, browse and filter the register, and import transactions from tab-delimited statement files. After this epic the owner can capture and review all everyday money movement, and account balances are correct. Establishes the shared Transaction model.
**FRs covered:** FR-6, FR-7, FR-8, FR-13 (AD-2, AD-3, AD-9, import conventions)

### Epic 4: Investment Tracking
The core: define Securities, let Holdings derive from investment Transactions (average-cost), record Buy/Sell/Dividend with their cash-flow effects, enter prices manually, and compute portfolio Valuation, Gain/Loss, and Net Worth in the Display Currency. After this epic the owner knows what their investments are worth and their true net worth. Extends the Transaction model from Epic 3.
**FRs covered:** FR-3, FR-4, FR-5, FR-9, FR-10 (AD-2, AD-4, AD-5, AD-6, AD-10, AD-12)

### Epic 5: Dashboard & Performance Insights
The hero experience, built to the visual reference: the dashboard with KPI summary cards (Net Worth, Portfolio Value, Gain/Loss, Cash), period change, a value-over-time trend chart, allocation breakdown, an insight call-out, and the full design-token system. After this epic the owner opens Financas and sees their whole financial picture at a glance, the way they want it to look.
**FRs covered:** FR-11, FR-12 (UX-DR2–8, AD-10, AD-11, AD-12, NFR-3, NFR-4)

### Epic 6: Data Safety (Export & Restore)
Let the owner export all authored data to a re-importable file and restore a fresh instance that reproduces the same balances, Holdings, and Net Worth. After this epic the owner can back up and recover everything.
**FRs covered:** FR-15 (NFR-2, AD-2, export/restore conventions)

---

## Epic 1: Foundation & Secure Access

Stand up a running, secure, deployable Financas the owner can log into. Establishes the layered scaffold, the decimal-money foundation, single-owner auth, the app shell, and Azure deployment.

### Story 1.1: Project scaffold & layered structure

As the builder,
I want the Go project scaffolded in the layered "onion" structure with local tooling,
So that every later story has a consistent place to live and a one-command local environment.

**Acceptance Criteria:**

**Given** an empty repository
**When** the scaffold is created
**Then** the Go module exists with packages `cmd/server`, `internal/{domain,money,service,store,http}`, `web/`, `db/migrations`, `db/query` (AD-1)
**And** a chi server starts and serves a `/healthz` route returning 200
**And** `docker-compose up` starts the app plus a PostgreSQL 18 container, and `Dockerfile` builds a single image
**And** goose, sqlc, templ, and Tailwind are wired with a documented build command.

### Story 1.2: Config & database foundation with decimal money

As the builder,
I want environment-driven config, a pgx connection pool with migrations, and the shared `Money` type,
So that data persists and all monetary math is exact from the first feature.

**Acceptance Criteria:**

**Given** the scaffold from Story 1.1
**When** the app boots with env config (DB URL, session secret)
**Then** it connects via a pgx pool and runs pending goose migrations on startup
**And** the `money` package exposes a `Money` type (decimal amount + ISO-4217 currency) and a `Convert(amount, rate)` function using banker's rounding (AD-4, AD-12)
**And** no floating-point type is used for any monetary or quantity value anywhere (NFR-5).

### Story 1.3: Single-owner authentication

As the owner,
I want to sign in with my credentials and have unauthenticated access blocked,
So that my financial data is private to me.

**Acceptance Criteria:**

**Given** a configured owner credential (hashed with argon2id)
**When** I submit correct credentials on the login page
**Then** a session cookie is set and I reach the authenticated area (FR-14, AD-7)
**And** any request to a non-login route without a valid session is rejected/redirected to login
**And** state-changing requests are protected by Go 1.25+ `http.CrossOriginProtection` (NFR-1)
**And** the session ends on logout and after an inactivity timeout.

### Story 1.4: Authenticated app shell & navigation

As the owner,
I want a clean app shell with a greeting and top navigation,
So that I can move between the app's areas in the look I want.

**Acceptance Criteria:**

**Given** I am authenticated
**When** any page renders
**Then** a responsive shell shows a greeting header ("Welcome back, {owner}") and top nav: Dashboard · Investments · Transactions · Accounts · Analytics (UX-DR1)
**And** base design tokens (rounded cards, soft shadows, type scale, semantic palette) are defined in Tailwind and applied to the shell (UX-DR7)
**And** the shell is usable on desktop and mobile-width viewports.

### Story 1.5: Azure deployment

As the builder,
I want the image deployed to Azure over managed Postgres,
So that I can reach Financas from any device.

**Acceptance Criteria:**

**Given** the single Docker image
**When** it is deployed to Azure Container Apps with Azure Database for PostgreSQL Flexible Server (AD-8)
**Then** the app is reachable over HTTPS and connects to the managed database
**And** all config/secrets are supplied via environment (no secrets in the image)
**And** migrations run successfully against the managed database on deploy.

## Epic 2: Accounts & Currencies

Let the owner define their currencies, exchange rates, and accounts — the structure everything else hangs on.

### Story 2.1: Currencies & display currency

As the owner,
I want to use USD and BRL and choose which currency totals are shown in,
So that my aggregated figures appear in the currency I think in.

**Acceptance Criteria:**

**Given** I am authenticated
**When** I set my Display Currency
**Then** the choice persists and is used for all aggregated views (FR-2)
**And** USD and BRL are available as currencies with ISO-4217 codes
**And** native amounts are never overwritten by the display choice (AD-5).

### Story 2.2: Exchange rates

As the owner,
I want to enter effective-dated USD↔BRL exchange rates,
So that cross-currency totals convert correctly over time.

**Acceptance Criteria:**

**Given** a Display Currency is set
**When** I enter an Exchange Rate with an effective date
**Then** it is stored as a directional, append-only row at `NUMERIC(18,8)` scale (AD-6)
**And** conversions select the rate effective at (≤) the relevant date, latest for "now"
**And** when a needed currency pair has no effective rate, the system prompts me rather than inverting or guessing.

### Story 2.3: Create & manage accounts

As the owner,
I want to create, rename, and archive cash, credit, and investment accounts,
So that my money is organized the way I hold it.

**Acceptance Criteria:**

**Given** I am authenticated
**When** I create an Account with a name, type (cash | credit | investment), and base Currency
**Then** it appears in the account list and is selectable for transactions (FR-1)
**And** an investment Account exposes a cash balance; a credit Account tracks a balance owed
**And** archiving an Account preserves its history but excludes it from default views and from current Net Worth.

## Epic 3: Transactions, Categories & Import

Full cash/credit bookkeeping and statement import; establishes the shared Transaction model.

### Story 3.1: Record cash income & expenses

As the owner,
I want to record income and expense transactions on my cash accounts,
So that my balances reflect reality.

**Acceptance Criteria:**

**Given** a cash Account exists
**When** I add an Income or Expense (amount, date, account, optional description) through a service use-case
**Then** a Transaction row is created and the account balance updates within one DB transaction (FR-6, AD-2, AD-3)
**And** amounts are stored as non-negative magnitudes with direction derived from `Transaction.type`
**And** I can edit and delete the transaction, and the balance re-derives accordingly.

### Story 3.2: Credit-card expenses & balance owed

As the owner,
I want to record expenses on a credit account,
So that I track what I owe.

**Acceptance Criteria:**

**Given** a credit Account exists
**When** I record an Expense on it
**Then** the account's balance owed increases by that amount (FR-6)
**And** the credit balance is displayed as a liability
**And** it is excluded from assets but reduces Net Worth (per AD conventions).

### Story 3.3: Transfers between accounts

As the owner,
I want to transfer money between my own accounts, including to pay my credit card and across currencies,
So that balances stay correct without double-counting.

**Acceptance Criteria:**

**Given** two of my Accounts exist
**When** I record a Transfer
**Then** it is stored as one Transaction row carrying `from_account_id`, `to_account_id`, `from_amount`, `to_amount` (AD-9)
**And** balance derivation debits the source and credits the destination from that single row (a credit destination reduces balance owed)
**And** a same-currency transfer has `from_amount == to_amount`; a cross-currency transfer records both legs
**And** no amount is double-counted in totals (FR-6).

### Story 3.4: Categories & classification

As the owner,
I want to classify income and expenses with categories,
So that I understand where money goes.

**Acceptance Criteria:**

**Given** I am authenticated
**When** I create a Category (income-type or expense-type) and assign it to an Income/Expense Transaction
**Then** an income-type Category attaches only to Income and an expense-type only to Expense (FR-7)
**And** I can filter and sum Transactions by Category
**And** a Category in use cannot be deleted without reassigning or confirming its Transactions.

### Story 3.5: Browse & filter the register

As the owner,
I want to browse and filter my transactions,
So that I can review my money movements quickly.

**Acceptance Criteria:**

**Given** transactions exist
**When** I open the register for an Account or across all Accounts
**Then** transactions list newest-first (FR-8)
**And** I can filter by type, Security, or Category, and results update without a full page reload (HTMX)
**And** each row shows date, description, signed colored amount, and a type/Category badge (UX-DR5).

### Story 3.6: Import transactions from a file

As the owner,
I want to import a tab-delimited statement file into an account,
So that I don't retype history.

**Acceptance Criteria:**

**Given** a target Account and a `date<tab>description<tab>value` file
**When** I import it
**Then** dates parse as `dd/mm/yy` or `dd/mm/yyyy` (two-digit-year pivot 00–69→2000s, 70–99→1900s) and values parse in Brazilian format (comma decimal) (FR-13)
**And** a negative value becomes an Expense and a positive value an Income, in the Account's Currency
**And** I preview parsed rows before committing, and an unparseable row reports its reason without aborting the batch
**And** re-importing the same file does not duplicate rows (dedup key `(account_id, date, description, value)` + stored row hash).

## Epic 4: Investment Tracking

The core: securities, derived holdings, investment transactions, manual prices, valuation, and net worth.

### Story 4.1: Manage securities

As the owner,
I want to define the securities I own,
So that I can record trades against them.

**Acceptance Criteria:**

**Given** I am authenticated
**When** I add a Security (symbol, name, type, quote Currency)
**Then** it is available for Buy/Sell/Dividend transactions and Holdings (FR-3)
**And** a duplicate symbol within my security list is prevented at entry.

### Story 4.2: Investment transactions & derived holdings

As the owner,
I want to record buys, sells, and dividends and see my holdings update,
So that my positions and cost basis stay accurate.

**Acceptance Criteria:**

**Given** a Security and an investment Account exist
**When** I record a Buy
**Then** the Holding's quantity and Cost Basis increase by `quantity×price + fees` and the account's cash balance decreases by the same (FR-5, AD-2)
**And** a Sell credits cash by `quantity×price − fees`, reduces Cost Basis proportionally, and records realized Gain/Loss using a single shared `basis_sold` domain function (AD-10), rejecting an oversell
**And** a cash Dividend credits the account's cash and leaves quantity and Cost Basis unchanged
**And** Holdings are read-only and derived on read (average-cost), never edited directly (FR-4).

### Story 4.3: Manual security prices

As the owner,
I want to enter prices for my securities,
So that valuations reflect what I believe they're worth.

**Acceptance Criteria:**

**Given** a Security exists
**When** I enter a Price with an effective date
**Then** it is stored as an append-only, effective-dated row and re-values affected Holdings (FR-9, AD-6)
**And** the most recent Price is used for current Valuation and its date/staleness is visible
**And** there is no online or automated price fetch anywhere.

### Story 4.4: Portfolio valuation & net worth

As the owner,
I want to see what my portfolio is worth and my net worth,
So that I know where I stand.

**Acceptance Criteria:**

**Given** holdings, prices, balances, and exchange rates exist
**When** valuation runs
**Then** each Holding shows quantity, current Price, Valuation, Cost Basis, and unrealized Gain/Loss; cumulative realized Gain/Loss is shown (FR-10)
**And** the Portfolio total and Net Worth (all cash + holdings − credit owed, excluding archived accounts) are computed in the Display Currency by a single canonical domain function (AD-10)
**And** conversion is convert-then-sum with banker's rounding (AD-12).

## Epic 5: Dashboard & Performance Insights

The hero experience built to the owner's visual reference.

### Story 5.1: Design-token system & component primitives

As the owner,
I want a consistent visual system,
So that the app looks clean and modern everywhere.

**Acceptance Criteria:**

**Given** the app shell from Epic 1
**When** the design system is implemented
**Then** Tailwind tokens define rounded cards (~16px), soft shadows, a type scale, and a semantic palette (green=gain, red=loss, neutral, one bold accent) (UX-DR7)
**And** reusable card, badge, and large-number components exist and render decimal money with currency and sign (UX-DR8)
**And** components meet reasonable accessibility defaults — keyboard-usable, legible contrast (NFR-4).

### Story 5.2: Dashboard KPI cards

As the owner,
I want a row of summary cards when I open the app,
So that I see my key numbers at a glance.

**Acceptance Criteria:**

**Given** I am authenticated with data
**When** I land on the Dashboard
**Then** KPI cards show Net Worth, Portfolio Value, Total Gain/Loss, and Cash in the Display Currency with an icon chip and a period-change delta (▲/▼ %) (FR-11, UX-DR2)
**And** the dashboard renders with no manual navigation after login
**And** period change shows "—" until a prior sample exists.

### Story 5.3: Value-over-time trend chart

As the owner,
I want a trend chart of my portfolio value,
So that I see how I'm doing over time.

**Acceptance Criteria:**

**Given** price and exchange-rate history exists
**When** the trend chart renders
**Then** it plots Display-Currency value sampled at each date an input changed, derived from history with no snapshot table (FR-12, AD-11)
**And** a range toggle (e.g. monthly) changes the window
**And** values are never retroactively recomputed at today's rate.

### Story 5.4: Allocation breakdown

As the owner,
I want to see how my portfolio is allocated,
So that I understand my mix.

**Acceptance Criteria:**

**Given** holdings exist
**When** the allocation view renders
**Then** it breaks down invested value by Security and/or Account (FR-12, UX-DR4)
**And** the percentages are computed from unrounded converted values and reconcile to exactly 100% (AD-12).

### Story 5.5: Transaction history widget & insight call-out

As the owner,
I want recent transactions and a highlight insight on the dashboard,
So that the dashboard feels alive and useful.

**Acceptance Criteria:**

**Given** transactions and valuations exist
**When** the dashboard renders
**Then** a recent-transactions widget lists rows with icon, description, date, signed colored amount, and a badge, linking to the full register (UX-DR5)
**And** a bold accent insight card surfaces one derived insight (e.g. "Your portfolio is up X% this month"), computed in `domain` and only rendered here (UX-DR6, AD-10).

## Epic 6: Data Safety (Export & Restore)

Back up and recover everything.

### Story 6.1: Export authored data

As the owner,
I want to export all my data to a file,
So that I have a backup I control.

**Acceptance Criteria:**

**Given** I am authenticated with data
**When** I export
**Then** the file contains only authored state — Accounts, Securities, Transactions, Categories, Prices, Exchange Rates (FR-15, AD-2)
**And** derived data (Holdings, balances, valuations) is not included
**And** the export downloads as a single re-importable file.

### Story 6.2: Restore from export

As the owner,
I want to restore a fresh instance from an export,
So that I can recover after a host loss.

**Acceptance Criteria:**

**Given** a fresh/empty instance and a valid export file
**When** I restore it
**Then** authored rows are inserted preserving primary keys (identity insert, no FK remap) (export/restore conventions)
**And** balances, Holdings, and Net Worth re-derive to match the source instance (FR-15, NFR-2)
**And** a malformed or partial file is rejected with a clear reason and leaves the instance unchanged.
