---
title: Financas
status: final
created: 2026-06-28
updated: 2026-06-28
---

# PRD: Financas
*Working title — confirm.*

## 0. Document Purpose

This PRD defines **Financas**, a browser-based personal finance manager for a single user (the builder), with **investment/portfolio tracking as the core** and full account/transaction bookkeeping — credit cards, categories, transfers, and USD/BRL multi-currency — around it. It is written for the builder-as-PM and for the downstream UX, architecture, and implementation work that follows. Structure: Glossary-anchored vocabulary, features grouped with Functional Requirements (FRs) numbered globally, assumptions tagged inline as `[ASSUMPTION]` and collected in §9. Scope is deliberately lean — a personal/hobby build, not a launch — so depth is spent where it changes what gets built (the portfolio and money-flow model) and kept thin elsewhere. Implementation choices (Go, PostgreSQL, Docker, Azure) live in the companion `addendum.md`, not here. This is the first artifact; no prior UX or architecture docs exist.

## 1. Vision

Financas is a clean, fast, browser-based home for one person's money — reachable from any device without installing anything. It takes the part of Moneydance that matters most to its owner — **knowing what your investments are actually worth and how they're doing** — and rebuilds it as a modern web app instead of a dated desktop client.

The heart of the product is the **portfolio**: every holding across every account, valued at current market prices and expressed in the owner's chosen currency, with gain/loss, allocation, and performance over time visible at a glance. Around that core sits the bookkeeping any honest finance tool needs — cash, credit-card, and investment accounts; a categorized transaction register; transfers between accounts; and USD/BRL handling — so that an accurate net worth falls out of the same ledger that feeds the portfolio view.

It matters because the owner already tracks this somewhere — a desktop app, a spreadsheet — and that somewhere is either tied to one machine or ugly enough to avoid. Financas wins by being the thing you'll actually open: on the couch, on a phone, on a work laptop — and enjoy looking at when you do.

## 2. Target User

### 2.1 Jobs To Be Done

- **See my total portfolio value and net worth right now**, in my chosen currency, so I know where I stand without doing math.
- **Record what I buy, sell, and receive (dividends, contributions)**, so my holdings and cost basis stay accurate.
- **Log everyday income and spending — including credit-card expenses — and classify each by category**, so I understand where money goes.
- **Move money between my own accounts as transfers** (including paying the credit card), so balances stay correct without double-counting.
- **Apply current market prices and my own USD↔BRL rates to valuations**, so my numbers are live, not stale.
- **See how my portfolio is doing over time and how it's allocated**, so I can make decisions and feel in control.
- **Do all of this from any device in a browser**, so the tool fits my life instead of one desk.
- **As the builder, have a project I enjoy using and maintaining** — a clean codebase and UI I'm proud of. *(Valid framing for a personal build.)*

### 2.2 Non-Users (v1)

- Households / couples sharing finances — Financas is **single-user** in v1. `[ASSUMPTION: no shared/household access]`
- Anyone needing tax-filing, professional accounting, or audit-grade double-entry guarantees.
- Active day-traders needing real-time tick data, order execution, or brokerage integration.

### 2.3 Key User Journeys

*Lighter scope (hobby/solo) — single-sentence journeys.*

- **UJ-1. Claudio checks his net worth.** Logged in on his phone, he opens Financas and the dashboard shows total portfolio value, net worth (assets − credit liabilities), today's change, and overall gain/loss — in his display currency, no taps required.
- **UJ-2. Claudio records a purchase.** After buying shares, he adds a Buy transaction (security, quantity, price, fees, date, account); the holding's quantity and cost basis update and the account's cash balance drops by the amount spent.
- **UJ-3. Claudio logs and classifies a credit-card expense.** He records an Expense on his credit-card account and tags it with a Category; the card's balance owed rises and the spend rolls up under that category.
- **UJ-4. Claudio transfers money.** He records a Transfer from checking to pay the credit card; both balances adjust in one operation with no double-counting.
- **UJ-5. Claudio reviews performance.** He opens a holding to see current value, cost basis, unrealized gain/loss, and a value-over-time chart, then glances at portfolio allocation.
- **UJ-6. Claudio updates prices and rates.** He enters the latest price for each holding and the current USD↔BRL rate by hand, and every holding and account re-values in his display currency. All market data is manual — there is no online or real-time feed.

## 3. Glossary

*Downstream work must use these terms exactly — no synonyms elsewhere in the PRD.*

- **Account** — a named container with a **type** (cash | credit | investment), a base **Currency**, and one owner. A **cash** Account holds a money balance; a **credit** Account tracks a balance *owed* (a liability); an **investment** Account holds Holdings **and** a cash balance for uninvested/settled cash. Holds Transactions.
- **Currency** — a unit of money (e.g. USD, BRL). Every Account and Security has one. The owner picks a **Display Currency** for aggregated views.
- **Exchange Rate** — an owner-entered conversion factor between two Currencies, effective from a date. The system retains rates over time; current figures use the latest Rate, historical figures use the Rate effective at their date. Not fetched automatically.
- **Display Currency** — the single Currency in which the dashboard and totals are presented (e.g. BRL). Native Currency is preserved per Account/Security; conversion uses the applicable Exchange Rate.
- **Security** — a tradable instrument identified by a symbol (stock | ETF | fund | other). Has a quote Currency. Referenced by Holdings and investment Transactions.
- **Holding** — the position in one Security within one Account: total quantity and aggregate Cost Basis. Derived from that account's Transactions; never edited directly.
- **Transaction** — a dated event in an Account. Types: **Buy**, **Sell**, **Dividend**, **Transfer**, **Income**, **Expense**. Carries amount(s), an optional free-text **description**, and, for investment types, security/quantity/price/fees. A cross-currency **Transfer** records both a debited and a credited amount.
- **Category** — an owner-defined label classifying Income and Expense Transactions (e.g. "Groceries", "Salary"), used for roll-ups. Each Category is income-type or expense-type.
- **Cost Basis** — total amount paid to acquire a Holding's current quantity (including Buy fees), average-cost method.
- **Price** — the value of one unit of a Security at a point in time, in the security's quote Currency. Entered manually by the owner; never fetched online.
- **Portfolio** — the aggregate of all Holdings across all Accounts, valued at current Prices.
- **Valuation** — a Holding's or the Portfolio's worth = quantity × current Price, convertible to Display Currency via Exchange Rate.
- **Net Worth** — Portfolio Valuation + all cash balances (cash Accounts and investment-Account cash) − credit Account balances owed, in Display Currency. Excludes archived Accounts.
- **Gain/Loss** — Valuation − Cost Basis (unrealized). Realized Gain/Loss is recorded on Sell, in the Security's quote Currency and shown converted to Display Currency.

## 4. Features

*FRs numbered globally for stable downstream reference. Investment features (4.2, 4.4, 4.5) are the core; 4.1, 4.3, 4.6, 4.7 are the supporting bookkeeping and plumbing that keep the picture accurate and safe.*

### 4.1 Accounts & Currencies

**Description:** The owner manages Accounts as containers for holdings and balances, and manages the Currencies and Exchange Rates that express everything in one Display Currency. Investment Accounts hold Securities plus a cash balance; cash Accounts hold a balance; credit Accounts hold a balance owed. Realizes UJ-1, UJ-3, UJ-4.

**Functional Requirements:**

#### FR-1: Manage accounts
The owner can create, rename, and archive an Account with a name, type (cash | credit | investment), and base Currency.

**Consequences (testable):**
- A created Account appears in the account list and is selectable when adding a Transaction.
- A credit Account's balance represents an amount owed and reduces Net Worth; an investment Account exposes a cash balance alongside its Holdings.
- Archiving an Account preserves its Transactions and history but excludes it from default views **and from current Net Worth**.

#### FR-2: Manage currencies and exchange rates
The owner can choose a Display Currency and enter/maintain Exchange Rates (e.g. USD↔BRL), each effective from a date; the system converts Valuations and balances to the Display Currency.

**Consequences (testable):**
- Current aggregated figures (Net Worth, Portfolio total, allocation) use the latest Exchange Rate; historical figures use the Rate effective at their date.
- Native Currency is preserved and shown alongside the converted figure per Account/Security.
- If no Exchange Rate exists for a needed Currency pair, the system prompts for one rather than guessing. `[ASSUMPTION: FX rates are manual; no automated FX feed]`

### 4.2 Securities & Holdings

**Description:** The owner defines the Securities they own and the app derives each Holding (quantity + Cost Basis) from that account's investment Transactions. Holdings are never edited directly — always a computed view of the transaction history, so the ledger stays the single source of truth. Realizes UJ-2, UJ-5.

**Functional Requirements:**

#### FR-3: Manage securities
The owner can add a Security with a symbol, name, type (stock | ETF | fund | other), and quote Currency.

**Consequences (testable):**
- A Security can be referenced by Buy/Sell/Dividend Transactions and by Holdings.
- A duplicate symbol within the owner's security list is prevented at entry.

#### FR-4: Derive holdings from transactions
The system computes each Holding's quantity and Cost Basis from the Buy/Sell/Dividend Transactions of its Security within an Account (average-cost).

**Consequences (testable):**
- A Buy increases quantity and Cost Basis; a Sell decreases quantity and reduces Cost Basis proportionally; a cash Dividend changes neither (see FR-5).
- A Holding with zero quantity is hidden from the active portfolio but retained for history.
- Holdings are read-only in the UI; correcting a position means editing its Transactions.

### 4.3 Transactions (Register)

**Description:** The transaction register is the ledger feeding everything. Investment types update Holdings and the investment Account's cash balance; cash/credit types update balances and roll up by Category; Transfers move value between the owner's own Accounts. Realizes UJ-2, UJ-3, UJ-4.

**Functional Requirements:**

#### FR-5: Record investment transactions
The owner can add, edit, and delete Buy, Sell, and Dividend Transactions (security, quantity, price, date, account, optional fees).

**Consequences (testable):**
- A **Buy** decreases the investment Account's cash balance by quantity×price + fees, and increases the Holding's quantity and Cost Basis by the same amount (fees included in basis).
- A **Sell** increases the cash balance by proceeds (quantity×price − fees), decreases quantity, reduces Cost Basis proportionally, and records realized Gain/Loss (= proceeds − basis sold) in the Security's quote Currency.
- A cash **Dividend** credits the investment Account's cash balance and does not change quantity or Cost Basis; a reinvested dividend is entered as a separate Buy.
- A Sell exceeding current quantity is rejected with a validation message. `[ASSUMPTION: rejected, not allowed to go negative]`

#### FR-6: Record cash & credit transactions and transfers
The owner can add, edit, and delete Income, Expense, and Transfer Transactions with amount, date, account, and (for Income/Expense) an optional Category.

**Consequences (testable):**
- An Expense on a credit Account increases its balance owed; an Income/Expense on a cash Account updates its balance.
- A Transfer moves value between two of the owner's Accounts in one operation without double-counting: into a credit Account it reduces balance owed; into a cash or investment Account it increases that balance.
- A **cross-currency Transfer** records both the debited amount (source Currency) and the credited amount (destination Currency) — or an amount plus an explicit transfer-time rate — rather than deriving the destination from the stored current Rate.
- A contribution of new money into an investment Account is recorded as a Transfer from a cash Account (or as Income if the source isn't tracked).

#### FR-7: Manage categories and classify transactions
The owner can create and edit Categories (income-type or expense-type) and assign one to each Income/Expense Transaction.

**Consequences (testable):**
- Income and Expense Transactions can be filtered and summed by Category.
- A Category in use cannot be deleted without reassigning or confirming its Transactions. `[ASSUMPTION: guarded delete]`

#### FR-8: Browse and filter the register
The owner can view Transactions for an Account or across all Accounts, sorted by date, and filter by type, Security, or Category.

**Consequences (testable):**
- The register lists Transactions newest-first by default and supports filtering without a page reload.

### 4.4 Prices & Valuation

**Description:** The app keeps current Prices for the owner's Securities and re-values Holdings, balances, and the Portfolio against them, converting to the Display Currency. This is what makes the numbers live rather than stale. Realizes UJ-1, UJ-6.

**Functional Requirements:**

#### FR-9: Update security prices
The owner enters and updates the current Price for each Security manually. There is no online, automated, or real-time market-data feed.

**Consequences (testable):**
- Entering a Price stores it per Security with a date and re-values affected Holdings.
- The most recent entered Price is used for Valuation; its date is shown so the owner can see how stale it is.
- A Price entered with a past date is retained in the Price history used by value-over-time (FR-12).

#### FR-10: Value the portfolio and net worth
The system computes Valuation and Gain/Loss per Holding and for the whole Portfolio, plus Net Worth, all in the Display Currency.

**Consequences (testable):**
- Portfolio total equals the sum of all Holding Valuations (converted via Exchange Rate).
- Net Worth = Portfolio total + all cash balances (cash Accounts and investment-Account cash) − credit balances owed, excluding archived Accounts.
- Each Holding shows quantity, current Price, Valuation, Cost Basis, unrealized Gain/Loss (absolute and %), and cumulative realized Gain/Loss where any Sell has occurred.

### 4.5 Portfolio Dashboard & Performance

**Description:** The home surface and the product's reason to exist: the owner's whole financial picture at a glance, weighted toward investments. Realizes UJ-1, UJ-5.

**Functional Requirements:**

#### FR-11: Portfolio dashboard
On opening the app, the owner sees total Portfolio value, Net Worth, period change, total Gain/Loss, and a breakdown of Holdings — in the Display Currency.

**Consequences (testable):**
- The dashboard renders with no manual navigation after login.
- Each Holding row links to its detail (Valuation, Cost Basis, Gain/Loss, transaction history).
- Period/today's change shows "—" (or zero) until at least one prior snapshot exists.

#### FR-12: Allocation & performance views
The owner can see Portfolio allocation (by Security and/or Account) and a value-over-time chart.

**Consequences (testable):**
- Allocation percentages sum to 100% of invested value.
- The value-over-time chart plots the Display-Currency value snapshotted at each point using the **then-current** Price and Exchange Rate — not retroactively recomputed at today's rate. `[ASSUMPTION: Price and Exchange Rate snapshots accrue going forward; no backfill of pre-app history in v1]`

**Notes:** `[NOTE FOR PM]` Performance attribution (time-weighted vs money-weighted return) is a known rabbit hole — v1 shows simple value-over-time and absolute/% gain only.

### 4.6 Data Import

**Description:** A low-friction way to seed the app without retyping history — the owner imports existing cash/credit Transactions from bank and card statement exports. Realizes UJ-3.

**Functional Requirements:**

#### FR-13: Import transactions from a file
The owner can import cash/credit Transactions into a chosen Account from a tab-delimited text file whose columns are **date, description, value**.

**Consequences (testable):**
- Each row creates a Transaction in the target Account: **date** parses as `dd/mm/yy` or `dd/mm/yyyy`; **value** parses in Brazilian number format (comma decimal separator, dot thousands); **description** is stored on the Transaction.
- The imported value is in the target Account's Currency (BRL or USD, fixed when the Account was created); a negative value is an Expense, a positive value an Income. `[ASSUMPTION: value sign determines Income vs Expense]`
- The owner previews the parsed rows before committing; a failed/unparseable row reports its reason and does not abort the batch.
- Re-importing the same file does not silently duplicate rows. `[ASSUMPTION: basic duplicate detection on import]`

### 4.7 Access & Data Safety

**Description:** The plumbing that makes a single-owner finance app on the open internet safe to rely on: getting in, and never losing the data. Realizes UJ-1 (and every journey, implicitly).

**Functional Requirements:**

#### FR-14: Authenticate the owner
The owner signs in with credentials to access the app; a session persists across navigation until logout or expiry.

**Consequences (testable):**
- Unauthenticated requests to any data view are rejected.
- Credentials are stored hashed; the session ends on logout and after an inactivity timeout.

#### FR-15: Export & restore data
The owner can export all data to a re-importable file and restore a fresh instance from it.

**Consequences (testable):**
- An export includes Accounts, Securities, Transactions, Categories, Prices, and Exchange Rates.
- A new/empty instance restored from an export reproduces the same balances, Holdings, and Net Worth.

## 5. Non-Goals (Explicit)

- **Not multi-user.** No sharing, household accounts, roles, or collaboration in v1.
- **Not a budgeting app.** Categories classify and roll up spending; they do **not** enforce envelopes/budgets (YNAB/Actual style). `[NON-GOAL for MVP]`
- **No online or real-time market data.** Both Security Prices and Exchange Rates are entered by the owner; the app fetches nothing — no live quotes, no FX feed, no real-time data.
- **Not automated bank/brokerage sync.** No account aggregation, Open Banking, or Direct Connect — data enters by manual entry or file import (FR-13).
- **Not tax software.** No tax-lot optimization, tax forms, or filing.
- **Not a trading platform.** No order placement, real-time tick data, or alerts.

## 6. MVP Scope

### 6.1 In Scope

- Accounts: cash, **credit**, and investment types (investment Accounts carry a cash balance) — FR-1
- **Multi-currency (USD + BRL)** with owner-entered Exchange Rates and a Display Currency — FR-2
- Securities & derived Holdings — FR-3, FR-4
- Transaction register: investment, cash, credit, transfers (incl. cross-currency) — FR-5, FR-6, FR-8
- Categories & transaction classification — FR-7
- Automated + manual prices, portfolio valuation, Net Worth — FR-9, FR-10
- Portfolio dashboard with allocation and value-over-time — FR-11, FR-12
- **Transaction import** (per-account, tab-delimited date/description/value, Brazilian number format) — FR-13
- Single-user authentication, and data export/restore — FR-14, FR-15
- Access from any modern browser (desktop + mobile web)

### 6.2 Out of Scope for MVP

- **Native mobile apps / offline-first PWA** — v1 is responsive web only.
- **Bank/brokerage auto-sync** — out, likely permanently for a personal build.
- **Budgeting / envelope limits** — deferred; possibly never (see Non-Goals).
- **Advanced performance analytics** (time/money-weighted returns, benchmarks) — v2+.

## 7. Success Metrics

*Hobby-scope — kept to what actually signals success.*

**Primary**
- **SM-1**: **Sustained personal use** — the owner opens Financas at least weekly and is still using it 1 month after data is loaded (hasn't drifted back to a spreadsheet/Moneydance). Validates the whole product, esp. FR-11, FR-12.

**Secondary**
- **SM-2**: **Trust in the numbers** — Portfolio total, Net Worth, and gain/loss match the owner's own spot-checks against brokerage/bank statements (including USD↔BRL conversion). Validates FR-4, FR-5, FR-9, FR-10, FR-2.

**Counter-metrics (do not optimize)**
- **SM-C1**: **Feature breadth** — resist adding features to look complete. More screens that go unused is failure, not progress. Counterbalances SM-1.

## 8. Open Questions

None outstanding. The two prior items are resolved: there is no market-data provider — Prices and Exchange Rates are entered manually (FR-9, FR-2) — and the import format is fixed (tab-delimited `date / description / value`, `dd/mm/yy[yy]`, Brazilian number format, per Account, FR-13).

## 9. Assumptions Index

*Every `[ASSUMPTION]` surfaced for confirmation:*

- **§2.2 / §5** — Single-user only; no shared or household access in v1.
- **§4.1 (FR-2) / §4.4 (FR-9)** — Both Exchange Rates and Security Prices are entered manually by the owner; no online or automated market-data feed.
- **§4.6 (FR-13)** — A negative imported value is an Expense, a positive value an Income; imported values are in the target Account's Currency.
- **§4.3 (FR-5)** — A Sell exceeding held quantity is rejected with a validation message.
- **§4.3 (FR-7)** — A Category in use has a guarded delete (reassign/confirm).
- **§4.5 (FR-12)** — Price and Exchange Rate snapshots accrue going forward; no backfill of pre-app history.
- **§4.6 (FR-13)** — Basic duplicate detection on file import.
- **Platform** — Responsive web only; not a native app or installable PWA in v1.
- **Aesthetic & Tone** — Visual style/brand is the builder's choice; no existing design system to honor.

---

## Platform

- **Form factor:** responsive **web application**, usable in any modern desktop and mobile browser. No installation. `[ASSUMPTION: responsive web, not a native app or installable PWA in v1]`
- **v1:** desktop + mobile web, single user, authenticated; deployed to **Azure** (implementation detail in `addendum.md`).
- **v2+ candidates:** installable PWA / offline support, automated FX rates, advanced performance analytics.

## Aesthetic & Tone

*Modern UI is a stated reason this product exists — so it's a requirement, not a nice-to-have.*

- **Feel:** clean, fast, uncluttered; a dashboard you enjoy opening. The anti-reference is Moneydance's dense, dated desktop UI.
- **Priorities:** the Net Worth / portfolio number and gain/loss are the visual hero; charts are legible at a glance; data entry and classification are quick and forgiving.
- **Tone of any app text:** plain, calm, neutral about money — no gamification, no nagging.
- `[ASSUMPTION: visual style/brand is the builder's choice; no existing design system to honor]`

## Cross-Cutting NFRs

*Light — single-user personal app, but it holds financial data on the open internet (Azure-hosted).*

- **Security:** authenticated access only (FR-14); data served over HTTPS; credentials stored hashed. Financial data is private to the single owner.
- **Reliability/Durability:** financial data must persist reliably (PostgreSQL) with the export/restore path of FR-15 so a host loss doesn't lose history.
- **Performance:** dashboard and register feel instant for a single user's data volume (years of personal transactions, not enterprise scale).
- **Accessibility:** reasonable defaults (keyboard-usable forms, legible contrast); no formal WCAG target for a personal build.
- **Correctness:** money and quantities use exact/decimal arithmetic, never floating point (see `addendum.md`).
