# Adversarial Architecture Review — Financas Spine

**Reviewer role:** adversarial architecture reviewer (compatibility-hole hunter)
**Scope reviewed:** `ARCHITECTURE-SPINE.md` against `prd.md`
**Calibration:** single-user, hobby-scope, investment-focused personal finance app (Go + PostgreSQL + Azure). No enterprise rigor demanded. Findings are limited to places where **two units built one level down could each obey every listed AD to the letter yet still build incompatibly.**

**Verdict: PASS-WITH-FIXES.** The bones are right — the onion layering, the ledger-as-truth invariant (AD-2), decimal-everywhere (AD-4), and store-native/convert-on-read (AD-5) are exactly the correct spine for this product. But the derive-on-read model is under-pinned at precisely the points where SM-2 ("trust in the numbers") lives. There are three reconciliation-class holes where two compliant units would produce numbers that **fail to add up against each other**. None require redesign; each closes with one added or tightened AD. They should be closed before any story that touches money math is built.

---

## Critical

### C1 — Cross-currency Transfer has no pinned storage shape; two encodings are offered and the ERD contradicts both
**Divergent units:** `service/transaction` (writes Transfers) vs `service/valuation` (derives each account's balance from transaction rows) vs `service/backup` (export/restore must round-trip them).

**Gap:** The ERD pins `ACCOUNT ||--o{ TRANSACTION` — **one account per Transaction row.** But a Transfer touches *two* accounts, and FR-6 explicitly offers **two different encodings**: "records both the debited amount (source Currency) and the credited amount (destination Currency) — **or** an amount plus an explicit transfer-time rate." Nothing in the spine says whether a Transfer is:
- one row carrying `from_account / to_account / from_amount / to_amount`, or
- two linked rows (one debit, one credit, joined by a transfer-group id), or
- one row + an explicit transfer-rate column.

A balance-derivation unit that expects **two rows** (one per account) will silently miss half of every transfer written by a unit that wrote **one row** — balances on one side will be wrong, and net worth breaks. This is the highest-probability "both obey the ADs, both are wrong together" hole in the document, and it directly threatens SM-2.

**Fix — add AD:** *"A Transfer is stored as exactly one Transaction row attached to a transfer-group, carrying `from_account_id`, `to_account_id`, `from_amount` (source Currency), and `to_amount` (destination Currency). For same-currency transfers `from_amount == to_amount`. The transfer-time rate is **not stored separately** — it is `to_amount / from_amount`, derivable on read. Balance derivation credits `to_account` and debits `from_account` from this single row."* Pick ONE encoding and pin it; delete the FR-6 "or." Update the ERD to show the two-account relationship for Transfer (or a transfer-leg model), so the single-account `ACCOUNT ||--o{ TRANSACTION` cardinality stops contradicting FR-6.

### C2 — Realized Gain/Loss: stored-vs-derived is unpinned and conflicts with AD-2; "basis sold" is not pinned as a single shared computation
**Divergent units:** `service/transaction` (records the Sell, PRD §3/FR-5 say realized gain is "recorded on Sell") vs `service/holding` (derives remaining quantity + cost basis) vs `service/valuation` (FR-10: shows "cumulative realized Gain/Loss").

**Gap (two distinct sub-holes):**
1. **Stored vs derived.** PRD §3 and FR-5 say realized gain is "**recorded** on Sell." AD-2 says **only Transaction rows are authored state; everything else is derived on read.** These conflict. If one unit stores realized gain as a column on the Sell row (computed from basis-at-write-time) and another derives cumulative realized gain by replaying transactions on read, the two diverge the moment an earlier Buy is edited (AD-2's prescribed correction path) — a stored realized gain goes stale; a derived one does not. The spine never resolves this.
2. **Basis-sold reconciliation.** A Sell must satisfy two equations against the **same** "cost basis of the shares sold" number: `realized_gain = proceeds − basis_sold` (FR-5) and `remaining_basis = basis_before − basis_sold` (holding derivation). If `service/valuation` computes `basis_sold` for the realized-gain figure while `service/holding` independently computes the basis reduction for the remaining position — with any difference in average-cost rounding (see H1) — then **remaining basis + basis sold ≠ total basis**, and realized gain won't reconcile against the holding. Two compliant units, numbers that don't tie out.

**Fix — tighten AD-2:** *"Realized Gain/Loss is **derived on read**, never stored — consistent with AD-2 (a stored realized gain would go stale when an antecedent transaction is edited). For any Sell, the cost basis of the shares sold (`basis_sold`) is computed by a **single pure `domain` function** that the holding derivation and the realized-gain derivation both call; `remaining_basis := basis_before − basis_sold` and `realized_gain := proceeds − basis_sold` use that same value. The two figures must reconcile by construction."*

### C3 — No single authoritative home for the derivations; the same derivation is named in `domain`, `service`, and `http`
**Divergent units:** any two of `domain`, `service/holding`, `service/valuation`, and the `http` dashboard/allocation projections — all named as computing the derived figures.

**Gap:** The Capability Map assigns derivations to multiple homes: Holdings to "`domain` (derive) **+** `service/holding`"; valuation/net worth to "`service/valuation`, `money`"; and dashboard/allocation to "**`http`** + `service/valuation` projections." AD-1 even forbids business logic in `http` — yet allocation-percentage and net-worth-rollup math is business logic, and the map puts a projection in `http`. With the derivation home unpinned, Team A can implement Holding derivation as a pure `domain` function while Team B re-derives a balance inline in `service`, and the dashboard re-rounds allocation in a templ helper. Three implementations of "the same number," three rounding/aggregation behaviors, guaranteed drift. This is the master derive-on-read hole that C2/H1/H2 are special cases of.

**Fix — add AD:** *"Every derived figure — Holding (qty + cost basis), account balance, Valuation, Net Worth, realized Gain/Loss, allocation — has exactly **one** canonical implementation: a pure function in `domain`. `service` loads the authored inputs (transactions, prices, rates) and calls the `domain` function; `http` only renders the result. No caller re-derives, re-aggregates, or re-rounds a derived figure independently. `http` performs no financial arithmetic (closes the AD-1 vs Capability-Map contradiction)."*

---

## High

### H1 — Average-cost algorithm: division precision, rounding mode, and round-point are unspecified
**Divergent units:** `service/holding` (cost basis + average cost) vs `service/valuation` (realized gain via `basis_sold`).

**Gap:** PRD says "average-cost method" but nothing pins **how**. Average cost per share = `total_basis / total_quantity` = NUMERIC(19,4) ÷ NUMERIC(28,10). That division does not terminate; the result must be truncated/rounded to *some* precision. `shopspring/decimal` defaults `DivisionPrecision` to 16 digits and rounds half-away-from-zero — but a unit that computes `basis_sold = qty_sold × avg_cost_per_share` will get a different answer than one that computes `basis_sold = total_basis × (qty_sold / qty_held)`, and both differ again depending on where rounding lands. Over many transactions these diverge enough to break SM-2 spot-checks against statements.

**Fix — add convention:** Pin (a) the formula: `basis_sold = total_basis × (qty_sold / qty_held)`, rounded to money scale (4 dp) **once, at the end**; (b) the rounding mode: banker's rounding (half-to-even) end-to-end; (c) `shopspring/decimal.DivisionPrecision` set explicitly (e.g. 12) in one shared place; (d) intermediate values carry full precision and are rounded **only** at the money/quantity column boundary.

### H2 — FX rounding/precision policy is explicitly Deferred — but two converting units need it now
**Divergent units:** `service/valuation` (net worth, portfolio total) vs the allocation/value-over-time projections (FR-12 requires allocation percentages to **sum to 100%**).

**Gap:** The spine's own Deferred list says "Rounding/precision policy for cross-currency display — settle when wiring FR-2/FR-10." That deferral is the hole: AD-5 says "convert at read time" but never says with what rounding, in what mode, or whether you convert-then-sum or sum-then-convert. Convert-each-holding-then-sum vs sum-then-convert give different net-worth totals; independent rounding makes allocation percentages fail to sum to 100% (FR-12's testable consequence). Two compliant units, two answers.

**Fix — promote out of Deferred into an AD:** *"Currency conversion is a single pure `money` function `Convert(amount, rate) → amount` with banker's rounding to the target currency's minor unit, rounded **once at the final display boundary** only. Aggregations sum in a chosen base (Display Currency) by converting each native amount then summing; the same convert-then-sum order is used everywhere. Allocation percentages are computed from the unrounded converted values and the displayed percentages are reconciled to sum to exactly 100%."*

### H3 — ExchangeRate direction and inversion are undefined
**Divergent units:** any unit needing USD→BRL vs one needing BRL→USD.

**Gap:** AD-6/ERD model `ExchangeRate` as `from/to`, append-only, owner-entered. FR-2 says "if no Exchange Rate exists for a needed Currency pair, the system **prompts** rather than guessing." That implies inversion (`1/rate`) is **forbidden** — but nothing states it. One unit could invert a stored USD→BRL row to satisfy a BRL→USD need; another could prompt for a separate row. If both directions get entered independently they can be mutually inconsistent (`r_AB × r_BA ≠ 1`), and no rule says which wins. Also the rate's numeric precision is unpinned (NUMERIC(19,4) is for money; a rate like 5.43219 needs more scale).

**Fix — add convention:** *"`ExchangeRate` rows are directional and never inverted in code; a needed pair with no effective row triggers the FR-2 prompt. Define a canonical storage convention (store one direction per ordered pair; the reverse direction is a separately-entered row). Pin the rate column as `NUMERIC(18,8)` (or similar) — distinct from money scale."*

### H4 — Value-over-time / "period change" has no entity and no governing AD, yet it is the product's reason to exist
**Divergent units:** a unit that **derives** the time series by walking the effective-dated Price/ExchangeRate history vs a unit that **stores periodic snapshots**.

**Gap:** FR-11 needs a "period/today's change" baseline ("shows — until at least one prior snapshot exists"); FR-12 needs a value-over-time chart "snapshotted at each point using the then-current Price and Exchange Rate." AD-6 says reads use the then-current row and never retroactively recompute — but it does **not** say whether the time series is *derived* from the sparse owner-entered price/rate history or *materialized* into a stored snapshot table. There is **no SNAPSHOT entity in the ERD.** Two units will implement this incompatibly: one computes value at each price-change date from history; another writes daily/periodic snapshot rows. They produce different curves and different "today's change" baselines. This is a load-bearing feature (FR-11/FR-12, SM-1) with no AD and no entity.

**Fix — add AD + entity decision:** *Decide and pin: value-over-time is derived from the effective-dated Price/ExchangeRate series (no snapshot table), with the curve sampled at each date where any input changed; "period change" baseline = the portfolio value computed at the prior sample date. If snapshots are instead materialized, add a `Snapshot` entity to the ERD and define its write trigger and contents.* (Derive-from-history is the AD-2-consistent default for this data volume.)

### H5 — Realized-gain conversion date and aggregation order are undefined
**Divergent units:** `service/valuation` computing "cumulative realized Gain/Loss" (FR-10) in Display Currency.

**Gap:** §3 says realized gain is computed in the Security's quote Currency and "shown converted to Display Currency." For a *cumulative* figure spanning Sells in different periods, nothing says whether you convert **each Sell's realized gain at that Sell's effective rate then sum**, or **sum in quote currency then convert at the latest rate**. These give materially different numbers across a USD/BRL swing. Two compliant units diverge.

**Fix — tighten §3/AD-5:** *"Cumulative realized Gain/Loss is the sum of each Sell's realized gain converted at the ExchangeRate effective on that Sell's date (consistent with AD-6 'historical figures use the rate effective at their date'), never re-based at today's rate."*

---

## Medium

### M1 — Import duplicate-detection key is undefined
**Divergent units:** the import **preview** pass vs the import **commit** pass vs a later re-import.

**Gap:** FR-13 says "re-importing the same file does not silently duplicate" with `[ASSUMPTION: basic duplicate detection]`, but the **duplicate key** is never defined: date+value? date+description+value? a row hash? Nor is any import identity persisted, so detection has nothing stable to compare against. Preview could flag by one key and commit dedupe by another. **Fix — add convention:** *"Duplicate detection on import keys on `(account_id, date, description, value)`; store a per-row natural-key hash on the Transaction so re-import is idempotent against committed rows."*

### M2 — Export/restore: 'only authored state' and ID handling are unstated
**Divergent units:** `service/backup` exporter vs importer.

**Gap:** FR-15 requires a restore to reproduce "the same balances, Holdings, Net Worth." Since those are derived (AD-2), the export must contain **only authored state** (accounts, securities, transactions, categories, prices, rates) and let derivation reproduce the rest — but the spine never says so. If an exporter also emits derived Holdings, an importer could persist them and double-count. Separately, `bigint` identity PKs mean restore must decide: **preserve IDs** or **regenerate and remap** all FKs (transaction→account, transaction→security). Two units could choose differently and break referential integrity. **Fix — add convention under AD-2/AD-8:** export contains authored state only; restore preserves PKs (identity insert) so FKs need no remap, OR defines a remap pass — pin one.

### M3 — Archived-account holdings: Portfolio total vs Net Worth inconsistency
**Divergent units:** `service/valuation` Portfolio-total path vs Net-Worth path.

**Gap:** FR-10 says Net Worth "excludes archived Accounts," and Portfolio total = "sum of **all** Holding Valuations." If an archived investment account still holds a Holding, is that Holding in the Portfolio total but out of Net Worth? Two units will answer differently, and FR-12's "allocation sums to 100% of invested value" depends on the answer. **Fix:** state that archived accounts (and their Holdings) are excluded from **both** Portfolio total and Net Worth in current/default views, retained only for history.

### M4 — AD-1 vs Capability Map contradiction (handler doing projection math)
Already folded into C3's fix, but flagging independently: the Capability Map places dashboard/allocation projections in `http`, while AD-1 forbids business logic there. Pin allocation/projection arithmetic in `domain`/`service`; `http` renders only.

### M5 — Money/rate column scales not fully pinned
NUMERIC(19,4) for money and NUMERIC(28,10) for quantity are pinned, but the ExchangeRate scale (H3) and Price scale are not separately pinned. A price of a fractional-quote security or a high-precision rate could be silently truncated differently by two migrations. **Fix:** pin Price as NUMERIC(19,4) (money) explicitly and Rate as its own higher scale.

---

## Low

- **L1 — `dd/mm/yy` century pivot undefined.** FR-13 accepts two-digit years; no rule says `yy=30` → 1930 or 2030. Pin a pivot (e.g. 00–69 → 2000s, 70–99 → 1900s) so two import runs agree.
- **L2 — Category type vs transaction type matching.** FR-7 makes each Category income- or expense-typed and assignable to Income/Expense transactions, but never says an income-type Category may only attach to Income. Pin the constraint or explicitly allow any.
- **L3 — Password hash algorithm is "argon2id/bcrypt" (either).** Only one auth unit, so low risk, but a restore/redeploy needs a single fixed scheme to re-verify stored hashes. Pin one (argon2id).
- **L4 — Signed vs magnitude amount storage.** The convention ("direction derived from `Transaction.type`, not a raw signed amount") implies amounts are stored as non-negative magnitudes; make it explicit ("amount columns are non-negative; sign/direction is a function of `type`") so import and register readers can't disagree.

---

## Version / Reality Check

The stack comment asserts "Go + Postgres verified current June 2026." Two of the pinned numbers do not survive scrutiny:

- **PostgreSQL 18.4 — implausible as of June 2026.** PostgreSQL major versions ship ~Sept/Oct; PG 18 ≈ Sept 2025. Minor (patch) sets ship roughly quarterly (Nov / Feb / May / Aug). By late June 2026 the latest minor would plausibly be **18.3 (~May 2026)**; **18.4 would not land until ~Aug 2026.** The "18.4" pin is ~one minor ahead of reality and directly undercuts the "verified current" claim. **Recommend:** verify against postgresql.org and pin 18.3 (or whatever is actually current), or change the comment to "pin exact minor at scaffold."
- **Go 1.26.4 — plausible but aggressive.** Go majors ship ~Feb/Aug; Go 1.26 ≈ Feb 2026, with monthly-ish patch releases — so 1.26.3/1.26.4 by late June 2026 is believable. Acceptable, but treat the exact patch as "pin at scaffold."
- **`http.CrossOriginProtection` misattributed to Go 1.26.** This CSRF type landed in **Go 1.25**, not 1.26. The convention table credits "Go 1.26 `http.CrossOriginProtection`." Minor, but it's an asserted-as-verified fact that is wrong — correct to "Go 1.25+."
- Azure Container Apps and Azure Database for PostgreSQL Flexible Server are both real, current services — fine. `shopspring/decimal`, chi v5, pgx v5, HTMX 2.x, Tailwind 4.x are all plausible/current — fine.

**Net:** the "verified current" comment claims more certainty than the PG 18.4 number and the CrossOriginProtection attribution can support. Downgrade the assertion or re-verify both.

---

## Summary of ADs/conventions to add or tighten

| # | Severity | Add/Tighten |
| --- | --- | --- |
| C1 | Critical | New AD: single canonical Transfer storage shape (one row, two-account, derived rate); fix ERD cardinality |
| C2 | Critical | Tighten AD-2: realized gain is **derived not stored**; one shared `basis_sold` function; reconciliation by construction |
| C3 | Critical | New AD: one canonical `domain` home per derived figure; `http` does no financial math |
| H1 | High | Convention: average-cost formula, rounding mode (half-even), explicit DivisionPrecision, round-once-at-boundary |
| H2 | High | Promote FX rounding policy out of Deferred into an AD: single `money.Convert`, convert-then-sum, allocation reconciled to 100% |
| H3 | High | Convention: no rate inversion in code; directional storage; dedicated rate column scale |
| H4 | High | New AD: value-over-time derived from effective-dated history (or add a `Snapshot` entity); define period-change baseline |
| H5 | High | Tighten §3/AD-5: cumulative realized gain converted at each Sell's effective rate |
| M1 | Medium | Convention: duplicate-detection key + stored row hash for idempotent import |
| M2 | Medium | Convention: export = authored state only; pin restore ID strategy |
| M3 | Medium | State archived accounts excluded from both Portfolio total and Net Worth |
| M4 | Medium | (folded into C3) projection math out of `http` |
| M5 | Medium | Pin Price and Rate column scales explicitly |
| L1–L4 | Low | Year pivot; category/transaction type rule; single password hash; magnitude+type amount rule |
| Ver | — | Re-verify/relax PG 18.4 (likely 18.3); fix CrossOriginProtection → Go 1.25; soften "verified current" |
