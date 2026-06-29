# Financas — PRD Addendum

*Depth that belongs to downstream work (architecture, solution design), kept out of the capability-level PRD. Owner-contributed.*

## Technical Stack & Deployment

*Stated by the owner during Discovery. These are decisions for the architecture phase to build on, not PRD requirements.*

- **Backend language:** Go (Golang).
- **Database:** PostgreSQL.
- **Local development / debugging:** Docker (containerized local environment).
- **Production deployment:** Microsoft Azure.

### Notes / implications for architecture

- Money and quantities should use exact/decimal types (not floats) given financial data — relevant to both Go modeling and PostgreSQL column types.
- Multi-currency: store amounts in each Account/Security native Currency plus the owner-entered Exchange Rate timeline; convert to Display Currency at read time. Decide whether to snapshot converted values for historical charts.
- Holdings are derived (never stored as editable state) — the data model is transaction-sourced; consider whether to materialize Holdings/Valuations for performance or compute on the fly.
- **No external/online integration.** Both Security Prices and Exchange Rates are owner-entered with an effective date — there is no market-data API client or FX feed to build. The data model needs manual Price and Rate entry plus their history.
- Price history and value-over-time charts imply a time-series of owner-entered Price (and Exchange Rate) points, queried by effective date.
- **Import format:** tab-delimited `date <tab> description <tab> value`; date `dd/mm/yy` or `dd/mm/yyyy`; value in Brazilian format (comma decimal, dot thousands); per-Account (Account currency fixes the value's currency); negative = Expense, positive = Income. The Transaction model needs a free-text `description` field.
- Single-user auth over HTTPS on Azure — pick the simplest secure approach (no multi-tenant complexity needed).
- A backup/export path is an explicit NFR — define the export format (e.g. SQL dump and/or CSV/JSON export).

## Reference points

- **Inspiration:** Moneydance (desktop personal finance) — emulate its portfolio/account model and local-data ethos; reject its dated UI.
- **Adjacent self-host/web tools** the owner is implicitly positioned against: Actual Budget (budgeting), Firefly III (double-entry budgeting), Ghostfolio (investments), Maybe, GnuCash. Financas differentiates by being **investment-first with multi-currency (USD/BRL)** for a single owner with a modern UI.
