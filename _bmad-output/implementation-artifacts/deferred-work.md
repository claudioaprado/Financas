# Deferred Work

## ✅ RESOLVED — quality faxina (2026-06-30)

All items below from the 4.x reviews were addressed in three reviewed commits:
- **`c0d7b83`** — http error-swallowing sweep: primary-load failures → HTTP 500 + banner (no misleading empty-200); secondary loads → log + degrade; the account/import `Get` distinguishes 404 (unknown id) from 500 (DB outage); non-oversold Holdings errors surface.
- **`21cbc6a`** — raw `err.Error()` echo removed: validation sentinels → pt-BR messages (`knownErrMsg`), unknown errors → generic message + server-side log.
- **`9e50dea`** — per-security oversold isolation: `DeriveHoldings` no longer aborts the account; an oversold position is excluded + surfaced as a partial-total warning; the Sell guard rejects only the offending security.

Remaining known fragility (NOT a code bug): the `internal/service/settings` suite (`TestDisplayCurrencyLifecycle`) shares the base `financas` DB and assumes `display_currency='USD'` — a live smoke or navigating the app against the base DB can flip it and turn the suite red until restored. Proper fix would be to isolate that suite onto a throwaway DB like the others.

## Deferred from: code review of 4-1-manage-securities (2026-06-29)

- **`List`-error swallowing renders a misleading empty page (HTTP 200) on a DB outage** — `internal/http/router.go` `renderSecurities`. Pre-existing project-wide convention: accounts (`renderAccounts`), categories (`renderCategories`), and exchange-rates (`renderExchangeRates`) all use `if x, err := …; err == nil` and silently drop the error. Should be fixed across the whole `http` layer at once (surface/log the error, return a non-200), not piecemeal in securities.
- **Raw wrapped DB errors echoed to the client via `err.Error()`** — `internal/http/router.go` `securitiesCreate` (and the same pattern in account/category create). On any non-23505 insert failure the internal error string is shown to the user. Low risk for a single-owner private app; clean up project-wide.
- **No max-length / interior-whitespace / charset validation on free-text fields** — `internal/service/security/security.go` `Create` (and the account/category baseline). Columns are unbounded `TEXT`; inputs like `"PE TR 4"` or very long strings are accepted. Consider a shared validation helper + DB length caps if this ever matters.
- **`UNIQUE(symbol)` is byte-exact, not a true case-insensitive backstop** — `db/migrations/00009_securities.sql`. Case-insensitive dedup currently holds only because `service/security.Create` upper-cases before insert (and AD-3 routes all writes through the service). If a non-service write path is ever introduced (e.g. a securities file-import or restore), switch to a functional unique index `UNIQUE (upper(symbol))` so the DB enforces it independently.

## Deferred from: code review of 4-2-investment-transactions-derived-holdings (2026-06-29)

- **One oversold security aborts `DeriveHoldings` for the whole account** — `internal/domain/holding.go`. The fold interleaves all securities and returns `nil, ErrOversold` on the first oversell, so one stranded position (reachable only by deleting a buy under a later sell, now that the Sell guard re-derives in-tx) hides ALL holdings on the page and blocks the sell guard for every security. Follow-up: isolate per-security (return partial holdings + a list of oversold security ids) instead of aborting the whole fold.
- **Non-`ErrOversold` Holdings/Balance errors swallowed in `renderInvestmentDetail`** — `internal/http/router.go`. Same project-wide swallow pattern as accounts/categories/exchange-rates (already tracked above from 4.1): a real DB error renders a blank "successful-looking" page. Fix across the `http` layer at once.
- **Cash leg vs full-precision basis sub-cent divergence for fractional shares** — `internal/service/transaction/transaction.go`. The cash debit/credit is stored at `NUMERIC(19,4)` (`from_amount`/`to_amount`) while `domain.DeriveHoldings` recomputes basis/proceeds at full precision from `quantity(28,10)×price(19,4)`. For fractional quantities whose product carries >4dp, "cash spent" and "cost basis" (and proceeds vs realized) differ by <0.00005. Consistent with the spine's full-precision-intermediates rule; revisit only if cent-exact cash==basis reconciliation is ever required.

## Deferred from: code review of 4-3-manual-security-prices (2026-06-29)

- **`renderPrices` swallows `Prices.List`/`Securities.List` errors → empty "No prices yet." page (HTTP 200) on a DB outage** — `internal/http/router.go` `renderPrices`. Identical to the project-wide swallow pattern already tracked from 4.1 (accounts/categories/exchange-rates/securities all do `if x, err := …; err == nil`). Risk here is slightly higher: on a transient List error the owner could re-enter an already-saved price, appending a silent duplicate "correction" row. Fix across the whole `http` layer at once (surface/log the error, return non-200), not piecemeal.
- **Raw wrapped DB error echoed to the client via `err.Error()`** — `internal/http/router.go` `pricesSubmit` (same as account/category/security create, and the trade handlers). On a non-typed insert failure the internal Postgres/driver error string is shown. Low risk for a single-owner private app; clean up project-wide. (The 4.3 [Med] price-rounding patch already removed the most common sub-cent bad-input raw-error path by rounding to money scale + returning the typed `ErrNonPositivePrice`.)
