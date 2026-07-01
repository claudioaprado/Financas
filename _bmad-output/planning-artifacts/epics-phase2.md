---
phase: 2
extends: "_bmad-output/planning-artifacts/epics.md"
inputDocuments:
  - "_bmad-output/planning-artifacts/prds/prd-Financas-2026-06-28/prd.md"
  - "_bmad-output/planning-artifacts/architecture/architecture-Financas-2026-06-28/ARCHITECTURE-SPINE.md"
baseline_commit: b93e470
lockedDecisions:
  - "Recurring: remind + one-click post (NO background scheduler)."
  - "Categories: FLAT (no subcategories) — bulk-categorize + notes/tags + search instead."
  - "Budget: monthly targets per category WITH rollover (unspent/overspent carries to next month); carryover derived on read (AD-2/AD-10)."
  - "Budget currency: Display Currency; actuals convert-then-sum at effective rates (AD-5/AD-12)."
  - "OFX dedup is FITID-ONLY. Content dedup by (date,description,value) is explicitly REJECTED: two legitimate transactions can share the same date/description/value (e.g. two identical purchases in a day) and must NEVER be discarded. Only an identical OFX FITID means the same transaction. A STMTTRN with no FITID is imported as new (not content-deduped)."
  - "Auto-categorization rules: global 'description contains X → Category Y', SUGGESTED in the import preview (owner confirms/overrides before commit)."
---

# Financas — Phase 2 Epic Breakdown (Epics 7–10)

## Overview

Phase 2 extends the completed MVP (Epics 1–6) with four owner-requested capabilities: smarter statement import (OFX + auto-categorization), a budgeting & spending view (filling the empty `/analytics` page), recurring transactions, and categorization productivity. It reuses the existing spine unchanged — the transaction ledger stays the single source of truth (AD-2), all new figures (budget carryover, spending aggregates, "due" recurrences) are derived on read via canonical `domain` functions (AD-10), money stays decimal end-to-end (AD-4/NFR-5), writes go through one DB transaction per use-case (AD-3), and money is stored native + converted only at read (AD-5/AD-12). No change to Epics 1–6 behavior.

## New Functional Requirements (extending FR-1..15)

FR-16: **Import OFX statements** — import cash/credit Transactions into a chosen Account from an OFX file (bank/credit-card statement export). Parse the OFX `STMTTRN` records (`FITID`, `DTPOSTED`, `TRNAMT`, `NAME`/`MEMO`); `TRNAMT` sign → Income/Expense; amounts in the Account's Currency; preview before commit; failed rows reported without aborting the batch; **idempotent re-import via FITID ONLY** (the bank's unique per-transaction id) — a row whose `(account, FITID)` already exists is the same transaction and is skipped. **Content dedup by `(date, description, value)` is explicitly forbidden**: two legitimate transactions can share the same date/description/value and must never be discarded. A `STMTTRN` with no FITID (rare) is imported as a new transaction — never content-deduped — and the owner is warned that re-importing such rows may duplicate them.

FR-17: **Auto-categorization rules** — the owner defines global rules ("transaction description/memo contains X → Category Y"); during an import preview each matched row shows a **suggested Category** the owner can accept or override before commit; rules never auto-commit a category silently. Guarded management (list, add, delete) of rules.

FR-18: **Monthly category budgets with rollover** — the owner sets a monthly target amount per Category (in the Display Currency); a budget view shows, for a chosen month, **planned (target + carryover) × actual × remaining** per Category, with over/under highlighted. Carryover accumulates the prior months' (planned − actual) and is **derived on read** from the ledger + targets (AD-2/AD-10), never stored. Actuals sum that month's categorized Income/Expense Transactions, converted to the Display Currency (convert-then-sum, AD-12); Transfers and investment trades are excluded (they are not budget spending).

FR-19: **Spending & cash-flow analytics** — on the `/analytics` page, show spending by Category over time and a monthly cash-flow summary (Income vs Expense per month) in the Display Currency, derived from the ledger (AD-2/AD-10/AD-12).

FR-20: **Recurring transactions** — the owner defines recurring templates (Income/Expense/Transfer with amount, account(s), optional Category, cadence = every N weeks/months/years, optional end date); the app surfaces **due** occurrences (next-due ≤ today) and the owner posts each with one click, which **materializes a real Transaction** (AD-9 one row for a transfer) and advances the schedule. No silent/background auto-posting.

FR-21: **Categorization productivity** — bulk-assign a Category to multiple register rows at once; attach a free-text Note and lightweight Tags to a Transaction; search the register by description/note. All honor the existing register read seam (AD-9/AD-2) and the category kind rule (income-type on Income, expense-type on Expense).

## Architectural notes (all Phase 2 stories honor these)

- **AD-2/AD-10 (derive on read):** budget carryover, spending aggregates, and recurrence "due" state are computed by canonical `domain` functions from authored rows (targets, recurring templates, the ledger) — never stored as running figures. The only authored new state is: `transaction.fitid`, `category_rule`, `budget` (the monthly target per category), `recurring` (the template), `transaction.note` + `tag`/`transaction_tag`.
- **AD-5/AD-12 (currency):** budgets and analytics report in the Display Currency; actuals are converted from native at each transaction's effective rate then summed (convert-then-sum, banker's round-once), reusing the valuation FX machinery. A missing rate yields a partial total + a notice (AD-6), never a guess.
- **AD-3 (one tx per use-case):** posting a recurrence, committing an import, saving a budget, bulk-categorizing — each is one DB transaction.
- **AD-9 (transfer = one row):** a recurring Transfer materializes exactly one two-account row.
- **NFR-5:** all amounts (targets, actuals, carryover) are decimal, never float, including JSON and rendering.
- **New migrations + sqlc** are required (unlike Epics 5/6): `make sqlc` / `make migrate` and `make nofloat` must stay green.

## New DB objects (summary)

- `transaction.fitid TEXT` (nullable) + partial-unique index `(from_account_id/to_account_id scope, fitid)` — OFX dedup (Epic 7).
- `category_rule (id, match_text, category_id, created_at)` — auto-categorization (Epic 7).
- `budget (id, category_id UNIQUE, amount NUMERIC(19,4), created_at)` — the current monthly target per category (Epic 8). (Effective-dated targets are a noted future refinement; v1 applies the current target uniformly across months for the carryover chain.)
- `recurring (id, type, from_account_id, to_account_id, amount, category_id, cadence, interval_n, start_date, end_date, next_due, created_at)` (Epic 9).
- `transaction.note TEXT` + `tag (id, name)` + `transaction_tag (transaction_id, tag_id)` (Epic 10).

---

## Epic 7: Smarter Import (OFX & Auto-Categorization)

Extend the file import (Epic 3 / FR-13) to real bank statements: parse OFX, deduplicate reliably by the bank's FITID, and let owner-defined rules suggest Categories in the preview so importing a month of statements isn't a manual categorization slog. Reuses the existing preview/commit machinery and register.
**FRs covered:** FR-16, FR-17 (AD-2, AD-3, AD-9, import conventions; NFR-5)

### Story 7.1: Import an OFX statement (FITID dedup)

As the owner,
I want to import a bank/credit-card OFX file into an account,
so that I don't retype statements and re-imports never duplicate.

**Acceptance Criteria:**
**Given** an OFX file and a chosen Account
**When** I preview then commit the import
**Then** each `STMTTRN` maps to an Income/Expense in the Account's Currency (TRNAMT sign → type; DTPOSTED → date; NAME/MEMO → description)
**And** a row whose `(account, FITID)` already exists is shown as a duplicate and not inserted (idempotent re-import) — this is the **only** dedup key
**And** two transactions with the same date, description, and value are BOTH imported (never treated as duplicates — identical fields are legitimate); a `STMTTRN` with no FITID is imported as new, with a visible warning that re-importing it may duplicate
**And** malformed/unparseable records are reported per-row without aborting the batch, and the commit writes in one DB transaction (AD-3).

### Story 7.2: Auto-categorization rules (suggested in preview)

As the owner,
I want rules that suggest a Category from a transaction's description,
so that importing categorizes most rows for me while I stay in control.

**Acceptance Criteria:**
**Given** rules of the form "description contains X → Category Y" (income-type rule → Income only, expense-type → Expense only)
**When** I preview an import
**Then** each matched row shows the suggested Category, which I can accept or override before commit (never auto-committed silently)
**And** I can list, add, and delete rules (guarded)
**And** a row matching multiple rules uses the first match (deterministic order), and an unmatched row stays uncategorized.

## Epic 8: Budgets & Spending View (Orçamento)

Turn the empty `/analytics` page into a budgeting home: monthly targets per Category with rollover, a planned-vs-actual view, and spending/cash-flow trends — all derived on read in the Display Currency. This is where categorized income and expenses become a real budget.
**FRs covered:** FR-18, FR-19 (AD-2, AD-5, AD-10, AD-12; NFR-3, NFR-5)

### Story 8.1: Set monthly budget targets per category

As the owner,
I want to set a monthly target for each Category,
so that I can plan how much to spend/earn per category.

**Acceptance Criteria:**
**Given** my income/expense Categories
**When** I set a monthly target amount (Display Currency) for a Category
**Then** the target is saved (one target per Category), editable and removable (one DB transaction, AD-3)
**And** amounts are decimal (NFR-5); a Category without a target is simply "no budget".

### Story 8.2: Budget view — planned × actual × remaining with rollover

As the owner,
I want a monthly budget view with rollover,
so that I see how each category is tracking, carrying over what I under/overspent.

**Acceptance Criteria:**
**Given** targets and a chosen month
**When** I open the budget view
**Then** each budgeted Category shows planned (target + carryover), actual (that month's categorized Income/Expense converted to the Display Currency), and remaining, with over/under highlighted
**And** carryover is derived on read as the running sum of prior months' (planned − actual) via one canonical `domain` function (AD-2/AD-10) — never stored
**And** Transfers and investment trades are excluded; a category spanning a currency with no effective rate yields a partial total + a notice (AD-6/AD-12), never a guess.

### Story 8.3: Spending & cash-flow analytics on /analytics

As the owner,
I want spending-by-category and monthly cash-flow charts,
so that I understand where money goes over time.

**Acceptance Criteria:**
**Given** categorized transactions
**When** I open `/analytics`
**Then** I see spending by Category (a breakdown, reconciled) and a monthly cash-flow summary (Income vs Expense per month) in the Display Currency, derived from the ledger (AD-10/AD-12)
**And** the page replaces the "Em breve" placeholder and reuses the design tokens/chart primitives from Epic 5
**And** empty/partial states are honest (no data → calm empty; missing rate → partial + notice).

## Epic 9: Recurring Transactions

Let the owner define recurring income/expenses/transfers and post due ones with one click — no background scheduler, so it fits the existing single-container architecture. The template is authored; "due" is derived; posting materializes a normal Transaction.
**FRs covered:** FR-20 (AD-2, AD-3, AD-9; NFR-5)

### Story 9.1: Define & manage recurring templates

As the owner,
I want to define recurring transactions,
so that regular income/expenses/transfers are set up once.

**Acceptance Criteria:**
**Given** the recurring form
**When** I create a template (type Income/Expense/Transfer, amount, account(s), optional Category, cadence = every N weeks/months/years, start date, optional end date)
**Then** it is saved, listable, editable, and removable (one DB transaction, AD-3; category kind rule honored; transfer needs two accounts)
**And** amounts are decimal (NFR-5).

### Story 9.2: Post due recurrences (remind + one-click)

As the owner,
I want the app to remind me of due recurrences and post them in one click,
so that nothing silently posts, but upkeep is trivial.

**Acceptance Criteria:**
**Given** templates with a next-due date ≤ today (derived from cadence + last post, up to today or the end date)
**When** I open the app
**Then** due occurrences are surfaced (a list + a dashboard nudge), and posting one **materializes a real Transaction** (a Transfer as one two-account row, AD-9) and advances the schedule to the next due date (one DB transaction, AD-3)
**And** I can skip an occurrence (advance without posting); posting is idempotent (no double-post of the same occurrence).

## Epic 10: Categorization Productivity

Make classifying and finding transactions fast: bulk-categorize from the register, add notes/tags, and search. Small, high-frequency quality-of-life features on the existing register.
**FRs covered:** FR-21 (AD-2, AD-3, AD-9; NFR-5)

### Story 10.1: Bulk-categorize from the register

As the owner,
I want to select several register rows and set their Category at once,
so that categorizing a backlog is fast.

**Acceptance Criteria:**
**Given** the register
**When** I select multiple Income/Expense rows and choose a Category
**Then** all selected rows get that Category in one DB transaction (AD-3), honoring the category kind rule (income-type on Income, expense-type on Expense; transfers/trades not categorizable)
**And** the update reuses the existing edit path (no new financial math, AD-10).

### Story 10.2: Notes & tags on transactions

As the owner,
I want to add a note and tags to a transaction,
so that I can annotate and group beyond a single Category.

**Acceptance Criteria:**
**Given** a Transaction
**When** I add a free-text Note and one or more Tags
**Then** they are saved and shown in the register/detail (one DB transaction, AD-3); Tags are reusable labels (create-on-use), removable
**And** notes/tags are presentation metadata — they never affect balances, budgets, or valuation (AD-2).

### Story 10.3: Search the register

As the owner,
I want to search the register by description/note,
so that I can find a transaction quickly.

**Acceptance Criteria:**
**Given** the register
**When** I type a search term
**Then** the list filters to Transactions whose description or note matches (case-insensitive), combinable with the existing account/type/category filters, without a page reload (HTMX, reusing the register read seam, AD-9)
**And** an empty result shows a calm empty state.

## Suggested execution order

7 → 8 → 9 → 10. Epic 7 (import) makes it easy to load real data to budget against; Epic 8 (budgets) is the highest owner-value; Epics 9–10 are quality-of-life. Each story follows the MVP cadence: bmad-create-story (lock decisions) → bmad-dev-story (TDD) → independent Opus review (separate lane) → fixes → one commit/story on `main`.

## Out of scope (Phase 2) / noted future refinements

- Effective-dated budget targets (v1 uses one current target per category for the carryover chain).
- Subcategories/hierarchical categories (owner chose flat).
- Background/automatic recurrence posting (owner chose remind + confirm).
- CSV import, receipt attachments, savings goals, exportable PDF reports, notifications — candidates for a later phase.

## Flag: existing tab-delimited import dedup (FR-13, MVP)

The MVP's tab-delimited importer dedups on a `(account, date, description, value)` content hash (`transaction.import_hash`). By the same reasoning above, that key produces **false positives** — two legitimate transactions with identical date/description/value would be wrongly treated as one and the second silently dropped. OFX (Epic 7) deliberately avoids this by using FITID only. Whether to revise the tab-delimited importer (e.g. drop content-hash dedup, or only dedup within a single re-import of the same file) is a **separate decision** for the owner — NOT changed in Phase 2 unless requested, since it alters committed MVP behavior.
