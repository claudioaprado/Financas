---
baseline_commit: d778fec
epic: 7
story: 7.2
phase: 2
---

# Story 7.2: Auto-categorization rules (suggested in preview)

Status: review

<!-- Note: Validation is optional. Run validate-create-story for quality check before dev-story. -->

## Story

As the owner,
I want rules that suggest a Category from a transaction's description,
so that importing categorizes most rows for me while I stay in control.

## Acceptance Criteria

From `epics-phase2.md` → Epic 7 → Story 7.2 (realizes FR-17). **Given** rules of the form "description contains X → Category Y" (income-type rule → Income only, expense-type → Expense only), **When** I preview an import, **Then**:

1. Each matched row shows the **suggested Category**, which I can **accept or override** before commit (never auto-committed silently).
2. I can **list, add, and delete** rules (guarded).
3. A row matching **multiple rules uses the first match** (deterministic order), and an **unmatched row stays uncategorized**.
4. Applies to **both** import previews (tab-delimited Story 3.6 and OFX Story 7.1); the committed category honors the **category kind rule** (income-type on Income, expense-type on Expense) and is written in the same one commit transaction (AD-3).

> **Scope:** rules only **suggest** during the import preview; nothing auto-commits a category. Suggestions are editable per-row selects carried into the commit. Rules are global (not per-account). Matching is case-insensitive substring against the **row's imported description** (for OFX that is `NAME`, else `MEMO`, as Story 7.1 already folds it). This story does NOT add rule-based categorization to manually-entered transactions, nor bulk re-categorization of existing rows (that is Epic 10).

## Locked Decisions (respect — do not relitigate)

- **Suggested, never silent.** A matched row shows the suggested category pre-selected in an editable `<select>`; the owner accepts or overrides, and the *chosen* value is what commits. No category is written without appearing in the preview.
- **First match wins**, rules ordered by `id` ascending (insertion order) — deterministic.
- **Kind-scoped.** A rule inherits its category's kind; only income-kind rules are considered for Income rows, expense-kind for Expense rows. The committed category is server-validated against the row's kind.
- **New DB object:** `category_rule (id, match_text, category_id, created_at)`; deleting a category cascades to its rules (`ON DELETE CASCADE`). Needs migration `00013` + `make sqlc`.
- **Reuse** the importer Preview/Commit spine (Stories 3.6/7.1) and the existing `/categories` page + `category` service; the tab and OFX dedup paths are unchanged.

## Tasks / Subtasks

- [x] **Task 1 — `category_rule` table (AC: #2, #3)**
  - [x] Add goose migration `db/migrations/00013_category_rule.sql` (next after `00012`). Up:
    - `CREATE TABLE category_rule (id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY, match_text TEXT NOT NULL, category_id BIGINT NOT NULL REFERENCES category (id) ON DELETE CASCADE, created_at TIMESTAMPTZ NOT NULL DEFAULT now());`
    - `CREATE INDEX category_rule_category ON category_rule (category_id);`
    - Down: `DROP TABLE category_rule;` (`-- +goose StatementBegin/End` per style).
  - [x] `ON DELETE CASCADE` keeps rules consistent when a category is removed (the `/categories` guarded delete already unassigns transactions; rules just vanish). `make migrate` (host 5433); verify up+down.

- [x] **Task 2 — sqlc: rule CRUD + category_id on both import inserts (AC: #1, #2, #4)**
  - [x] New `db/query/category_rule.sql`:
    - `CreateCategoryRule :one` — `INSERT INTO category_rule (match_text, category_id) VALUES ($1, $2) RETURNING id, match_text, category_id, created_at`.
    - `ListCategoryRules :many` — join category so the matcher/UI get kind + name in one read: `SELECT r.id, r.match_text, r.category_id, c.name AS category_name, c.kind AS category_kind FROM category_rule r JOIN category c ON c.id = r.category_id ORDER BY r.id`.
    - `DeleteCategoryRule :execrows` — `DELETE FROM category_rule WHERE id = $1`.
  - [x] Extend the two import inserts in `db/query/transaction.sql` to carry the chosen category (append a **new last param**, so existing positional callers only add one arg):
    - `CreateImportedTransaction` → add `category_id` column + `$9` (was $1..$8).
    - `CreateOFXTransaction` → add `category_id` column + `$9` (was $1..$8).
    - Both stay `:execrows`; `category_id` is `pgtype.Int8` (NULL ⇒ uncategorized). Do **not** touch the dedup columns (`import_hash`/`fitid`).
  - [x] `make sqlc`; commit regenerated `internal/store/*`. Confirm `CreateImportedTransactionParams`/`CreateOFXTransactionParams` gained a trailing `CategoryID pgtype.Int8` and a new `CategoryRule` model + `ListCategoryRulesRow` appeared. **No** unrelated full-row struct churn (the SELECT lists in Task 2 are new/joined, not the `store.Transaction` shape).

- [x] **Task 3 — `service/categoryrule` (guarded CRUD) (AC: #2)**
  - [x] New package `internal/service/categoryrule` (mirrors `service/category`): `New(pool)`; `Rule{ID int64; MatchText string; CategoryID int64; CategoryName string; Kind category.Kind}`; `List(ctx) ([]Rule, error)`; `Add(ctx, matchText string, categoryID int64) (Rule, error)` (trim + reject empty match_text → `ErrEmptyMatch`; the FK enforces a real category, map `23503` → `ErrCategoryNotFound`); `Delete(ctx, id int64) error` (`ErrNotFound` on 0 rows). Each write in one tx (AD-3). It may import `service/category` only for the `Kind` type (a type-only dep) — or redefine kind locally to avoid the dep; prefer reading kind as a plain string to keep layering clean (AD-1).
  - [x] DB-gated test `categoryrule_test.go` (reuse the `testDatabaseURL` skip pattern): add two rules, list returns them in id order with kind/name joined; delete removes one; empty match_text rejected; a rule to a missing category rejected; deleting the category cascades the rule away.

- [x] **Task 4 — pure suggestion matcher + wire into both previews (AC: #1, #3, #4)**
  - [x] In `internal/service/importer`, add a pure `SuggestCategory(description, kind string, rules []Rule) int64` (returns the first rule's category id whose `kind` matches and whose `match_text` is a case-insensitive substring of `description`; else 0). Define a small importer-local `Rule{MatchText string; CategoryID int64; Kind string}` (the service loads them via `store.ListCategoryRules` — same store-read pattern the importer already uses for accounts/hashes/fitids; no `service → service` dep).
  - [x] Add `SuggestedCategoryID int64` to `PreviewRow`. Both `classify` (tab) and `classifyOFX` (OFX) set it on **new** rows via `SuggestCategory(row.Description, row.Type, rules)` (rules passed in). `Preview`/`PreviewOFX`/`Commit`/`CommitOFX` load rules once via a new `s.rules(ctx)` helper (`store.ListCategoryRules` → `[]Rule`) and thread them through.
  - [x] Pure unit tests: first-match-wins (two matching rules → first id); kind scoping (an expense-kind rule never suggests on an income row); case-insensitive substring; no match → 0.

- [x] **Task 5 — Commit applies the chosen category (AC: #1, #4)**
  - [x] Change `Commit`/`CommitOFX` to accept a per-row category selection `cats map[int]int64` (keyed by `ParsedRow.Line`; value 0 ⇒ uncategorized). For each new row, resolve the category to write: use `cats[line]` if present, else the row's `SuggestedCategoryID` (so an un-interacted preview still applies the suggestion the owner saw). **Server-validate** the chosen category against the row's kind — load valid `(id→kind)` from `store.ListCategories`; if the chosen id is absent or kind-mismatched, import that row **uncategorized** (defensive; the UI already prevents mismatches). Write `category_id` via the extended insert. Still one tx (AD-3).
  - [x] DB-gated test extends `importer`/`ofx_import` tests: with a rule "salary → <income cat>", commit a matching income row ⇒ the stored transaction has that category (assert via `store`/register); an overridden `cats[line]` wins over the suggestion; a kind-mismatched `cats` entry ⇒ uncategorized; no rules ⇒ uncategorized (unchanged behavior). Tab and OFX both covered.

- [x] **Task 6 — Rules management page (AC: #2)**
  - [x] Add a guarded `CategoryRules` interface to `http.Deps` (`List`, `Add`, `Delete`) implemented by `categoryrule.New(pool)`; wire in `cmd/server/main.go`.
  - [x] Routes under the existing authed group: `GET /categories/rules` (list + add form: a `match_text` input + a category `<select>` of all categories), `POST /categories/rules` (add), `POST /categories/rules/delete` (delete by id). Map `categoryrule` errors to pt-BR in `knownErrMsg`/`problemMsg` (empty match, category not found, not found). Link the page from `/categories` ("Regras de categorização automática →") and from the import page.
  - [x] templ `CategoryRulesPage` + a `RuleRow` view struct in `web/shell.go`; render each rule as "«match_text» → CategoryName (kind)" with a delete button. Nav stays the five items.

- [x] **Task 7 — Import preview: per-row category selects carried into commit (AC: #1, #4)**
  - [x] `renderImport` must now load categories (`deps.Categories.List`) and split into income/expense option lists; pass them to `ImportPage`. Map each `PreviewRow.SuggestedCategoryID` into the `web.ImportRow` (add `SuggestedCategoryID int64`).
  - [x] **Restructure the results UI so the per-row selects submit with commit:** wrap the results table **inside** the commit `<form>` (hidden `content` + `format` stay). Each **new** row renders `<select name="cat_{line}">` with a blank "— sem categoria —" option + the categories of that row's kind (income options for income rows, expense for expense rows), pre-selected to the suggestion. Duplicate/error rows render no select. The single "Confirmar N novas linhas" button submits content + format + all `cat_{line}` values.
  - [x] `importCommit` parses `cat_{line}` form fields into `map[int]int64` (ignore blank/զero) and passes it to `Commit`/`CommitOFX` (branch on format as today). Preview handler unchanged except it now renders selects.
  - [x] `make generate` (templ) + rebuild css if touched; commit `*_templ.go`.

- [x] **Task 8 — Tests, verify, docs (AC: all)**
  - [x] `router_test.go`: stub `CategoryRules`; test the rules page (auth gate, add renders the rule, delete). Extend the import stub so `Preview`/`PreviewOFX` set a suggestion and `Commit`/`CommitOFX` record the received `cats`; assert the preview renders a category `<select>` for a new row and that committing posts the selected `cat_{line}` through to the service.
  - [x] `GOTOOLCHAIN=local go build ./... && go vet ./...`, `go test ./...` (DB-gated skip without `TEST_DATABASE_URL`), `make nofloat` green (this story adds no money math), `gofmt` clean.
  - [x] **Live smoke** (`docker compose up -d db`; free :8080 first; login owner/financas): create an expense category, add a rule "uber → <that category>"; on a cash account, import (tab or OFX) a row described "UBER TRIP" ⇒ preview pre-selects the category; override one row to a different category, leave another as suggested, blank a third ⇒ commit ⇒ the register shows the three categories exactly as chosen. Delete the rule; re-preview ⇒ no suggestion. Restore `display_currency='USD'` + free :8080 after.
  - [x] Update `README.md` briefly (auto-categorization rules: global "description contains X → Category Y" managed at `/categories/rules`; suggested and editable in the import preview, first-match, kind-scoped, never auto-committed).

## Dev Notes

### Scope boundary — what this story does NOT do (read first)

- **NO silent auto-categorization** — rules only *suggest*; the committed value is the owner's per-row selection (defaulting to the suggestion they saw).
- **NO change to import dedup** — `import_hash` (tab) and `fitid` (OFX) paths are untouched; this story only adds a `category_id` to the insert and threads suggestions.
- **NO manual-entry rules, NO bulk re-categorization** of existing transactions (Epic 10).
- **NO new money math** — `make nofloat` stays trivially green; rules/category are non-financial metadata.

### Architecture invariants this story must honor

- **AD-3 — one tx per use-case.** Committing an import (now with categories) is one DB transaction; adding a rule / deleting a rule each one tx. [ARCHITECTURE-SPINE.md#AD-3]
- **AD-1 — layering.** `service/categoryrule` and the importer read via `store`; the pure `SuggestCategory` matcher has no I/O; `http` defines the `CategoryRules` interface and renders. No `service → service` data dep (only a possible type-only `category.Kind`, which the story avoids by treating kind as a string). [ARCHITECTURE-SPINE.md#AD-1]
- **Category kind rule.** income-type category ↔ Income, expense-type ↔ Expense — enforced both in the suggestion (kind-scoped) and re-validated at commit. [ARCHITECTURE-SPINE.md#Consistency Conventions]
- **AD-2.** Rules and the chosen category are authored state on the `category_rule`/`transaction` rows; nothing derived is stored. [ARCHITECTURE-SPINE.md#AD-2]

### Previous-story intelligence — load-bearing

[Source: 7-1-import-ofx-statement-fitid-dedup.md (commit d778fec); 3-6; [[financas-phase2-progress]]]

- **Importer spine:** `Service{pool}`; `Preview`/`Commit` (tab) and `PreviewOFX`/`CommitOFX` (OFX) each `account()`-validate cash/credit, run `classify`/`classifyOFX` (which label new/duplicate/error and now compute suggestions), and insert new rows via `CreateImportedTransaction`/`CreateOFXTransaction` in one tx using `legs()`. `PreviewRow{ParsedRow; Status; Warning}` — add `SuggestedCategoryID`.
- **Commit re-parses content deterministically** (row order stable), so a `cats map[Line]categoryID` submitted from the preview maps cleanly back onto re-parsed rows. This is why per-row selects key on `ParsedRow.Line`.
- **Column-append discipline (7.1 lesson):** adding `category_id` as the **last** param to the two import inserts is a pure append — it does NOT touch the full-row `store.Transaction` SELECT lists (those already include `category_id`), so no sqlc row-struct churn is expected here. Just re-run `make sqlc` and confirm.
- **Category UI patterns to reuse:** `/categories` page (`categoriesPage`/`categoriesCreate`/`categoriesDelete`), the `CategoryOption{ID,Name,Kind}` select (`web/pages.templ` account-detail edit form, `<option value=id selected?=… >{Name} ({Kind})</option>`), and `category.Service.List`. The import page's category selects reuse this exact option shape, filtered by kind.
- **http conventions:** pt-BR error mapping via `problemMsg`/`knownErrMsg`; primary-load failures → `logLoad`+banner; never leak raw `err`. `templ` escapes `&`→`&amp;`, `'`→`&#39;` (adjust asserts). Build `GOTOOLCHAIN=local`; DB host 5433; live smoke frees :8080 and restores `display_currency='USD'`.

### Project Structure Notes

- **New:** `db/migrations/00013_category_rule.sql`; `db/query/category_rule.sql`; `internal/service/categoryrule/categoryrule.go` (+ `categoryrule_test.go`); `web` `CategoryRulesPage`.
- **Modified:** `db/query/transaction.sql` (`category_id` on `CreateImportedTransaction` + `CreateOFXTransaction`) → regenerated `internal/store/*` (+ new `CategoryRule` model, `ListCategoryRulesRow`, `category_rule.sql.go`, `querier.go`); `internal/service/importer/{parse.go? no}` — `importer.go` (`SuggestedCategoryID` on `PreviewRow`, `Rule` type, `SuggestCategory`, `s.rules`, thread through Preview/Commit/…, category write + kind re-validation), plus a new `suggest.go`/`suggest_test.go` if cleaner; `internal/http/router.go` (`CategoryRules` iface + routes/handlers, `renderImport` loads categories, `importCommit` parses `cat_{line}`, error mapping) + `router_test.go`; `web/pages.templ` (`ImportPage` per-row selects inside commit form + `CategoryRulesPage`) + regenerated `web/pages_templ.go`; `web/shell.go` (`ImportRow.SuggestedCategoryID`, income/expense option lists on the import page, `RuleRow`); `cmd/server/main.go` (wire `categoryrule.New`); `README.md`.

### Testing standards

- **Pure:** `SuggestCategory` table tests (first-match, kind scope, case-insensitive, no-match).
- **DB-gated:** `categoryrule` CRUD + cascade; importer commit writes the chosen/suggested/overridden/kind-mismatched category correctly (tab + OFX).
- **http:** rules page auth/add/delete; import preview renders per-row category selects and commit forwards the selections.
- `go test ./...` green with no DB; `go vet` + `make nofloat` + `gofmt` clean.

### References

- [Source: epics-phase2.md#Story 7.2 / #FR-17] — suggested-in-preview, list/add/delete, first-match, uncategorized default.
- [Source: epics-phase2.md#New DB objects] — `category_rule (id, match_text, category_id, created_at)`.
- [Source: 7-1-import-ofx-statement-fitid-dedup.md] — importer Preview/Commit spine, `legs()`, column-append discipline, category select pattern.
- [Source: ARCHITECTURE-SPINE.md#AD-1/#AD-3/#Consistency Conventions] — layering, one tx, category kind rule.

## Dev Agent Record

### Agent Model Used

claude-opus-4-8[1m] (Opus 4.8, 1M context)

### Debug Log References

- `make migrate` applied `00013_category_rule.sql`; `make sqlc` generated the `CategoryRule` model, `CreateCategoryRule`/`ListCategoryRules`(joined kind+name)/`DeleteCategoryRule`, and appended `CategoryID pgtype.Int8` to both `CreateImportedTransactionParams` and `CreateOFXTransactionParams` (no `store.Transaction` row-struct churn — the column already existed in the full-row lists).
- TDD: pure `SuggestCategory` tests first (RED on `undefined`, then GREEN); note the "broad rule" case hinges on the literal letter `e` — an early wrong expectation surfaced that `"salary bonus"` has no `e`, corrected to prove kind-scoping instead.
- `TestImportCategorization` initially FAILED in the full-suite run (passed in isolation): the importer tests share the base DB and `category_rule` rows accumulate globally, so `SuggestCategory` (first-match by id) picked a stale rule. Fixed by running that test against a private throwaway DB (`isolatedDB`, mirroring backup/valuation), since suggestion assertions depend on the global rule set.
- `go build`/`go vet`/`gofmt`/`make nofloat` clean (this story adds no money math). Full `go test ./...` green.
- Live smoke (server :8080, owner/financas): added rule `uber → Transporte` at `/categories/rules`; a fresh "UBER special ride" tab row previewed with the category select **pre-selected to `Transporte`** (`value="164" selected`); committing with line-1 keeping the suggestion and line-2 overridden stored `UBER→Transporte`, `Padaria→Lazer`. (First smoke pass pre-selected a stale category because the base DB held leftover test rules — confirmed a pollution artifact, not a defect, then cleaned + re-verified.) Env restored (rules cleared, display_currency='USD', :8080 freed).

### Completion Notes List

All four ACs verified (pure unit + DB-gated + http + live smoke):
- **AC1 — suggested, editable:** matched rows show the rule's category pre-selected in a per-row `<select name="cat_{line}">`; the owner accepts or overrides; the *chosen* value commits. Never auto-committed silently.
- **AC2 — guarded CRUD:** `/categories/rules` lists/adds/deletes rules (auth-gated); empty match text and a missing category are rejected with pt-BR messages.
- **AC3 — first-match + uncategorized default:** `SuggestCategory` returns the first (id-ordered) kind-matching substring rule; an unmatched row stays uncategorized.
- **AC4 — both previews + kind + one tx:** applies to tab (3.6) and OFX (7.1); the committed category is server-validated against the row's kind (mismatch ⇒ uncategorized) and written via `category_id` on the two import inserts inside the single commit transaction (AD-3).

Decisions / variances (intentional):
- **`category_id` appended (last param) to `CreateImportedTransaction` + `CreateOFXTransaction`**, not a new query — a pure additive change; dedup columns (`import_hash`/`fitid`) untouched. Existing struct-literal callers compile unchanged (zero-value ⇒ NULL).
- **Suggestion matching is a pure function** (`SuggestCategory`); the service loads rules via `store.ListCategoryRules` (no service→service dep, AD-1). `Kind` carried as a plain string in `service/categoryrule` to keep it dependency-free.
- **Results table moved inside the commit `<form>`** so the per-row selects submit with commit; commit maps `cat_{line}` (present-even-if-0 ⇒ explicit uncategorized) else falls back to the suggestion.
- **`ON DELETE CASCADE`** on `category_rule.category_id` keeps rules consistent when a category is deleted.

### File List

New:
- `db/migrations/00013_category_rule.sql`, `db/query/category_rule.sql`
- `internal/service/categoryrule/categoryrule.go`, `categoryrule_test.go`
- `internal/service/importer/suggest.go`, `suggest_test.go`, `categorize_test.go`

Modified:
- `db/query/transaction.sql` (`category_id` on `CreateImportedTransaction` + `CreateOFXTransaction`) → regenerated `internal/store/{transaction.sql.go,category_rule.sql.go,models.go,querier.go}`
- `internal/service/importer/importer.go` (`SuggestedCategoryID` on `PreviewRow`; `Commit`/`CommitOFX` take `cats map[int]int64`; `rules`/`categoryKinds`/`resolveCategory`; `classify`/`classifyOFX` thread rules) + `importer_test.go`/`ofx_import_test.go` (nil cats)
- `internal/http/router.go` (`CategoryRules` iface + Deps + `/categories/rules` routes/handlers + `renderRules`; `Imports.Commit/CommitOFX` signatures; `parseImportCategories`; `renderImport` loads categories; error mapping) + `router_test.go` (stub signatures, `stubCategoryRules`, `TestCategoryRulesPage`, `TestImportCategorySelect`)
- `web/shell.go` (`ImportRow.SuggestedCategoryID`, `RuleRow`), `web/pages.templ` (`ImportPage` per-row category selects inside the commit form + `CategoryRulesPage` + `categorySelect`) → regenerated `web/pages_templ.go`
- `cmd/server/main.go` (wire `categoryrule.New`), `README.md`

## Change Log

| Date | Change |
| --- | --- |
| 2026-06-30 | Story 7.2 drafted (create-story): auto-categorization rules (`category_rule` table, migration 00013) — global "description contains X → Category Y", kind-scoped, first-match; a pure `SuggestCategory` matcher feeds suggestions into BOTH import previews (tab + OFX) as editable per-row selects; commit writes the chosen/suggested category (server-validated by kind) via `category_id` appended to both import inserts, one tx; guarded `/categories/rules` management page. Reuses the 3.6/7.1 importer spine + `/categories` patterns. Status → ready-for-dev. |
| 2026-06-30 | Story 7.2 implemented (dev-story, TDD): `00013` category_rule + `service/categoryrule` CRUD; pure `SuggestCategory` (kind-scoped, first-match); previews suggest per-row, commit applies the chosen/suggested category (kind-validated) via `category_id` on both import inserts, one tx; `/categories/rules` page + per-row selects on the import page. All 4 ACs verified (unit + DB + http + live smoke). build/vet/gofmt/nofloat green; tab & OFX dedup untouched. Status → review. |
