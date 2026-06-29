# PRD Quality Review — Financas (2026-06-28)

## Gate verdict: PASS-WITH-FIXES

The PRD is well-structured, strategically coherent, and appropriately lean for a solo/hobby build. Glossary discipline is good, FR numbering is clean and contiguous (FR-1..FR-13), every UJ is referenced, and Non-Goals are honest. **However**, the core promise — an accurate Net Worth and "trust in the numbers" (SM-2) — rests on a money-flow model that is silently undefined: the PRD never says whether buying a security debits a cash balance, nor whether investment accounts hold cash. As written, Sells, Buys, and Dividends can leave Net Worth double-counted or money unaccounted. That one gap is critical-tier and must be decided before architecture. A handful of multi-currency and Dividend behaviors are also under-defined. None require a redesign; all are localized decisions.

## Dimension judgments

- **Decision-readiness** — *adequate.* Real decisions are stated (average-cost, manual FX, single-user, responsive-web-only). The `[NOTE FOR PM]` on performance attribution is a genuine tension, not a safe checkpoint. Weakened by the unmade money-flow decision (below) being invisible rather than surfaced as an Open Question.
- **Substance over theater** — *strong.* One real user, no persona padding. NFRs are product-specific and honestly scoped "light." The differentiation (investment-first + USD/BRL + modern UI) is earned and lives in the addendum, not as template furniture.
- **Strategic coherence** — *strong.* Clear thesis (the Moneydance portfolio view, rebuilt as a web app you'll actually open). Feature priority follows the thesis (4.2/4.4/4.5 named as core). SM-1 measures retention of the thesis, SM-C1 is a real counter-metric.
- **Done-ness clarity** — *thin in spots.* Most FRs carry testable consequences. But several core behaviors resolve to undefined or adjective-level (money-flow on Buy/Sell, Dividend mechanics, cross-currency transfer amounts, "prevented (or merged)").
- **Scope honesty** — *strong.* Non-Goals + `[NON-GOAL for MVP]` + indexed assumptions + NOTE FOR PM. Open-items density is appropriate to stakes. Dinged only by two unindexed inline assumptions.
- **Downstream usability** — *adequate.* IDs clean, cross-refs resolve, Glossary anchors vocabulary. Held back by two in-scope capabilities (export, auth) that have no FR to source-extract.
- **Shape fit** — *strong.* Hobby/solo capability spec with light UJs; correctly calibrated, neither over- nor under-formalized.

---

## Findings by severity

### CRITICAL

- **Money-flow integrity: does a Buy debit cash, and do investment accounts hold cash?** (§3 Glossary, §4.2 FR-4, §4.3 FR-5, §4.4 FR-10) — The product explicitly rejects double-entry (§2.2), and Net Worth = Portfolio + cash balances − credit balances (Glossary; FR-10). For this to be correct, a Buy must reduce *some* cash balance by the purchase amount, and a Sell/Dividend must increase one. But Holdings are derived only from Buy/Sell/Dividend (FR-4) while balances are derived only from Income/Expense/Transfer (FR-6) — the two ledgers never touch. Nothing states that a Buy debits cash. Consequences: either (a) the owner must hand-enter a paired Transfer/Expense for every Buy (tedious, unstated, error-prone), or (b) a Buy silently creates a Holding while the spent cash still sits in checking → **Net Worth double-counts**. Symmetrically, Sell proceeds and cash Dividends have nowhere to land (see High items). This directly undermines SM-2. *Fix:* Decide and state explicitly: give investment Accounts a cash sub-balance that Buy (−), Sell (+), Dividend (+), and fees adjust automatically; add a testable consequence to FR-5 ("a Buy decreases the account's cash balance by qty×price+fees; a Sell increases it by proceeds−fees"). If instead cash movement stays manual, say so and add a NOTE FOR PM about the double-count risk. Either way this must be resolved before architecture.

### HIGH

- **Dividend transaction semantics are undefined.** (§3 Glossary "Transaction"; §4.2 FR-4; §4.3 FR-5) — Dividend is an investment type that FR-4 says feeds "quantity and Cost Basis," but a cash dividend changes neither — it adds cash; only a stock dividend/DRIP changes quantity. The PRD never says what a Dividend does (cash in? to which balance? reinvested?), nor whether dividend income appears in income roll-ups (it carries no Category, being an investment type). Dividends are named in the JTBD, so this is load-bearing. *Fix:* Define Dividend behavior in FR-5 consequences: cash dividend credits the account cash balance and does not change quantity/Cost Basis; (optionally) a separate reinvestment is modeled as a Buy. State whether dividend income is reportable.

- **Cross-currency Transfer amount is modeled incorrectly.** (§4.3 FR-6) — FR-6 says a cross-currency Transfer is handled "via the current Exchange Rate." Real bank/broker conversions use the rate *at transfer time* including spread, not the owner's stored current rate; deriving the destination leg from the current rate will make balances drift from reality and break SM-2. A Transfer currently carries a single "amount" (Glossary), but a cross-currency transfer needs two legs (source amount in source currency, destination amount in destination currency). *Fix:* Let a cross-currency Transfer record both the debited and credited amounts (or amount + explicit transfer-time rate), not a system-derived conversion. Update the Glossary "Transaction" entry and FR-6 consequence accordingly.

- **Historical FX is dropped from value-over-time charts.** (§4.1 FR-2; §4.5 FR-12; addendum "Multi-currency" note) — FR-2 states conversion always "uses the current Exchange Rate," but FR-12 plots historical Display-Currency value. With multi-currency holdings, applying today's rate to all history retroactively distorts the curve (USD holdings' BRL history would swing purely from FX revaluation). The FR-12 assumption mentions only *price* snapshots and silently omits FX-rate history; the addendum flags this as an open decision but the PRD body neither resolves nor surfaces it. *Fix:* Decide whether value-over-time snapshots the Display-Currency value at each point (using the then-current rate) or recomputes at today's rate, and state it in FR-12; extend the snapshot assumption to cover Exchange Rate, not just Price.

- **Investment-account cash / Sell proceeds have nowhere to go.** (§3 Glossary "Account"; §4.4 FR-10) — Closely tied to the Critical item: Net Worth sums "cash Account balances" but an investment Account's settled cash (from a Sell or Dividend) is not a tracked balance. Until that cash is moved to a cash Account, it vanishes from Net Worth. *Fix:* Same as the Critical fix — model uninvested/settled cash inside investment Accounts, or require/auto-create a Transfer.

### MEDIUM

- **Backup/export is in MVP scope and an NFR but has no FR.** (§6.1; Cross-Cutting NFRs; addendum) — "Persistent storage with a backup/export path" is listed in scope and called an "explicit NFR," and the addendum asks to define the export format — yet no FR covers it. Downstream story creation may drop it. *Fix:* Add FR-14 "Export/backup data" with testable consequences (e.g., full export to a re-importable file; restore path).

- **Single-user authentication has no FR.** (§6.1; NFR Security; UJ-1 "Logged in on his phone") — Auth is in scope and referenced by UJs but exists only as an NFR adjective ("authenticated access only"). *Fix:* Add a brief FR for login/session (or explicitly fold it into an existing FR) so downstream knows it must be built.

- **Realized Gain/Loss is recorded but never displayed, and its currency is undefined.** (§3 Glossary "Gain/Loss"; §4.3 FR-5; §4.4 FR-10) — Sell records realized gain/loss (Glossary, FR-5), but no FR shows cumulative realized gain, and it's unclear whether it's in the Security's quote Currency or Display Currency (cross-currency makes this matter). A computed value with no surface is an untestable orphan. *Fix:* Either add a consequence/FR that surfaces realized gain (e.g., per Holding and/or a realized-gain total) and state its currency, or drop it from the Glossary if v1 won't show it.

- **Buy/Sell fees: effect on Cost Basis and proceeds unspecified.** (§4.3 FR-5) — FR-5 takes "optional fees" but never says fees add to Cost Basis on Buy / reduce proceeds on Sell. This affects gain/loss accuracy, which SM-2 spot-checks. *Fix:* Add a consequence: fees increase Cost Basis on Buy and reduce realized proceeds on Sell.

- **Exchange Rate "timeline" vs "current rate" intent conflict.** (§3 Glossary "Exchange Rate"/"Display Currency"; FR-2; addendum) — Glossary defines rates "at a point in time" and the addendum describes an "Exchange Rate timeline," but every FR-2 consequence uses only "the current Exchange Rate." If only the current rate is ever applied, the timeline is unused; if history matters (FR-12), the FRs contradict it. *Fix:* Reconcile — state explicitly whether historical rates are stored-and-applied or only the latest rate is ever used.

- **Two inline `[ASSUMPTION]` tags are not in the §9 Index.** (§Platform line ~276; §Aesthetic line ~287 vs §9) — §9 says "Every `[ASSUMPTION]` surfaced," but the responsive-web-only assumption and the visual-style assumption (both below the `---` divider) are missing from the index. The responsive-web-only one is a real scope boundary. *Fix:* Add both to §9 (or note that the appendix sections maintain their own inline tags).

- **"Contributions" (JTBD) maps to no Transaction type.** (§2.1; §3 Glossary types) — The JTBD names recording "contributions," but the type list (Buy/Sell/Dividend/Transfer/Income/Expense) has no contribution; new external money into an investment account is ambiguous (Income? Transfer from checking?). *Fix:* State the mapping (e.g., a contribution is a Transfer into the investment account / Income), or drop the word.

### LOW

- **FR-3 "duplicate symbols are prevented (or merged)" offers two behaviors as one.** (§4.2 FR-3) — Untestable as written; pick one. *Fix:* Choose prevent **or** merge.

- **"Today's change" / "period change" undefined before any snapshot exists.** (§4.5 FR-11; FR-12 assumption) — Since history accrues going forward, on day 1 there is no prior valuation. *Fix:* Add a consequence that change shows "—" / 0 until a prior snapshot exists.

- **Manual Price override is wiped by the next successful fetch.** (§4.4 FR-9) — For securities you always price manually but that the provider *does* return (possibly wrong), the manual value is overwritten. *Fix:* Allow a "manual, pin" mode, or note that pinning is out of scope.

- **Transfer sign convention for a credit destination is only spelled out for the card-payment case.** (§4.3 FR-6) — Generalize: a Transfer into a credit Account reduces balance owed; into a cash/investment Account increases balance. *Fix:* State the general rule once.

- **Net Worth treatment of archived accounts unstated.** (§4.1 FR-1; Glossary "Net Worth") — Archiving preserves history but it's unclear if an archived account still contributes to current Net Worth. *Fix:* One sentence — archived accounts are excluded from current Net Worth.

---

## Mechanical notes

- **FR IDs:** FR-1..FR-13 contiguous, unique, no gaps. Good.
- **UJ coverage:** UJ-1..UJ-6 all referenced by feature groups (UJ-1: 4.1/4.4/4.5; UJ-2: 4.2/4.3/4.6; UJ-3: 4.1/4.3/4.6; UJ-4: 4.1/4.3; UJ-5: 4.2/4.5; UJ-6: 4.4). Note references are at feature-group granularity, not per-FR — acceptable for this scope.
- **SM/UJ protagonist:** single named protagonist (Claudio/the owner) carried inline. Fine.
- **Glossary drift:** minor — UJ-1 "credit liabilities" vs Glossary "credit Account balances owed"; both clearly the same concept. No action needed.
- **Assumptions roundtrip:** 7 of ~10 inline tags indexed; two appendix-section tags missing (see Medium finding). No index entry lacks an inline source.
- **Required sections:** all present and appropriately scoped for a hobby capability spec.
